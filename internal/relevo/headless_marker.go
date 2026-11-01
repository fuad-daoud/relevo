package relevo

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
)

// nudgePlanOnMarker resumes an exited plan-writing reader whose marker is down
// but whose output holds no plan steps. The marker is removed once the resume
// has started, so the round closes on the resumed runner's exit and not on the
// marker already there. resumed is false, with nothing changed, for any round
// that does not owe the nudge.
func nudgePlanOnMarker(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry) (store.Binding, bool, error) {
	if rt.Runner == nil {
		return b, false, nil
	}
	codeText := "unknown"
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(b.Builder), rt.Store.StreamPath(b.Name, b.Round)); ok {
		codeText = strconv.Itoa(code)
	}
	if !readerPlanNudgeDue(rt, b, entries, codeText) {
		return b, false, nil
	}
	next, resumed, err := nudgeResume(ctx, rt, tx, b, entries, codeText, rt.Now().UTC())
	if err != nil || !resumed {
		return b, false, err
	}
	if rerr := os.Remove(rt.Store.DonePath(b.Name, b.Round)); rerr != nil && !os.IsNotExist(rerr) {
		slog.Warn("plan nudge could not clear the marker", "binding", b.Name, "round", b.Round, "err", rerr)
	}
	return next, true, nil
}

// readerFinalMessageGrace is how long a reader round whose marker is present
// waits for its runner to exit: a runner writes its final message just after
// the marker and then exits, so two minutes is generous, and a hung one must
// not hold the round forever.
const readerFinalMessageGrace = 2 * time.Minute

// readerSummaryEarlyNote is the note a reader round closes with when its
// runner outlived readerFinalMessageGrace: the summary is the stream as it was,
// not a completed final message.
const readerSummaryEarlyNote = "runner still running after its marker; summary taken early"

// holdReaderOnMarker reads a reader round's marker once and reports what the
// close must do with it. A reader's marker is not the end of its stream: the
// runner writes its final message just after the marker and then exits, and
// that message is the round's summary, so the round waits for the exit. A
// marker absent this tick closes nothing, and a marker that appears between
// this read and closeOnMarker's own must not close the round before the final
// message lands. held is true while the process is alive inside the grace; a
// runner still alive after the grace is stopped the way `relevo stop` stops
// one, with early true, so the caller closes on the stream as it is. Inside
// the grace a report-less round is held even when its process is gone, and an
// unreadable liveness check holds the tick.
func holdReaderOnMarker(ctx context.Context, rt Runtime, b store.Binding) (present, held, early bool, err error) {
	fi, serr := os.Stat(rt.Store.DonePath(b.Name, b.Round))
	if serr != nil {
		return false, false, false, nil // no marker yet: the round is live, close nothing
	}
	present = true
	withinGrace := rt.Now().Sub(fi.ModTime()) < readerFinalMessageGrace

	// While the round has no report and the grace has not run out, hold the
	// close so a message still coming is not lost; a live process is
	// recorded so a later tick never classifies it as lost to a restart.
	if withinGrace && !readerHasReport(rt, b) {
		if b.Builder.PID != 0 && rt.Runner != nil {
			alive, aerr := rt.Runner.Alive(ctx, handleOf(b.Builder))
			if aerr != nil {
				slog.Warn("reader liveness check failed; holding the round", "binding", b.Name, "pid", b.Builder.PID, "err", aerr)
			} else if alive {
				rt.Watched.Mark(b.Builder.PID, b.Builder.StartedAt)
			}
		}
		return present, true, false, nil
	}

	if b.Builder.PID == 0 || rt.Runner == nil {
		return present, false, false, nil
	}
	alive, aerr := rt.Runner.Alive(ctx, handleOf(b.Builder))
	if aerr != nil {
		slog.Warn("reader liveness check failed; holding the round", "binding", b.Name, "pid", b.Builder.PID, "err", aerr)
		return present, true, false, nil
	}
	if !alive {
		return present, false, false, nil
	}
	// A sighting: this daemon now knows the process is alive, so a later tick
	// never classifies it as lost to a restart.
	rt.Watched.Mark(b.Builder.PID, b.Builder.StartedAt)
	if withinGrace {
		return present, true, false, nil
	}
	if _, err := stopProcess(ctx, rt, b, "stop"); err != nil {
		return present, false, false, err
	}
	return present, false, true, nil
}

// markerClose is the marker branch of reconcileHeadless: closeOnMarker and,
// when the marker is present and the gate is done, the post-close sequence.
// The normal read and the re-check in the exited branch share it, so a marker
// appearing inside one tick is handled by one implementation. closed and
// gating are the two bools reported: a read with no marker comes back
// unchanged with both false.
func markerClose(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, markerNote string, wantVerify bool) (store.Binding, bool, bool, error) {
	out, note, closed, gating, done, err := markerCloseReader(ctx, rt, tx, b, entries, markerNote)
	if err != nil {
		return b, false, false, err
	}
	if done {
		return out, closed, gating, nil
	}
	next, closed, gating, rec, err := markerCloseVerify(ctx, rt, tx, out, entries, note, wantVerify)
	if err != nil {
		return next, closed, gating, err
	}
	if gating || !closed {
		return next, closed, gating, nil
	}
	return markerCloseRepair(ctx, rt, tx, b, next, rec)
}

// markerCloseReader is the reader-shape prologue of markerClose. A reader
// round closes on its runner's exit, not on the marker: the summary is only
// complete once the process has gone, so the marker is read exactly here and
// a marker written between this read and closeOnMarker's own cannot close the
// round before the final message lands. done reports whether the tick ends
// here with out, closed and gating to return; otherwise out and note continue
// into the marker close below.
func markerCloseReader(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, markerNote string) (out store.Binding, note string, closed, gating, done bool, err error) {
	if b.Shape != store.ShapeReader {
		return b, markerNote, false, false, false, nil
	}
	present, held, early, herr := holdReaderOnMarker(ctx, rt, b)
	if herr != nil {
		return b, markerNote, false, false, false, herr
	}
	if !present || held {
		return b, markerNote, false, false, true, nil
	}
	if early {
		return b, joinNotes(markerNote, readerSummaryEarlyNote), false, false, false, nil
	}
	next, resumed, nerr := nudgePlanOnMarker(ctx, rt, tx, b, entries)
	if nerr != nil {
		return b, markerNote, false, false, false, nerr
	}
	if resumed {
		return next, markerNote, false, true, true, nil
	}
	return b, markerNote, false, false, false, nil
}

// markerCloseVerify closes the round on its marker and runs the post-close
// sequence: the served-round record, the scope end, the verify consult and
// the delivery. rec is the gate record the repair below reads; it is nil on
// the paths that return before the close.
func markerCloseVerify(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, markerNote string, wantVerify bool) (next store.Binding, closed, gating bool, rec *store.GateRecord, err error) {
	base := b.RoundBaselineTree
	next, closed, gating, rec, err = closeOnMarker(ctx, rt, tx, b, entries, markerNote)
	if err != nil {
		return b, false, false, nil, err
	}
	if gating || !closed {
		return next, closed, gating, nil, nil
	}

	closedRound := b.Round
	if next.Owner != "" && rt.CloseServedRound != nil {
		next = rt.CloseServedRound(ctx, rt, next)
	}
	next.Builder = clearProcess(next.Builder)
	// A runner that wrote its marker and died inside the same tick is gone;
	// end its scope now, best effort. A live runner is left to its own
	// supervisor, and the scope check leads so a scopes-off close pays for no
	// liveness read.
	if rt.Scope != nil && !roundRunnerAlive(ctx, rt, b) {
		endRoundScope(ctx, rt, b, closedRound)
	}
	next.StalledSince = time.Time{}
	// Verify starts after the gate and before the mastermind is told, as the
	// pane path orders it, so the reviewer sees the gate's output.
	if wantVerify && next.Shape != store.ShapeReader {
		gateLogPath := ""
		if rec != nil {
			gateLogPath = rec.LogPath
		}
		next, err = consult.StartVerify(ctx, consultDeps(rt), tx, next, closedRound, consult.VerifyDiffCommand(base, next.RoundClosedTree), gateLogPath)
		if err != nil {
			return next, true, false, nil, err
		}
	}
	next, err = deliverAndSettle(ctx, rt, tx, next)
	if err != nil {
		return next, true, false, nil, err
	}
	return next, true, false, rec, nil
}

// markerCloseRepair runs the repair decision after a marked close: a halt the
// environment gate owes, then a failing gate's repair round. b is the binding
// as the tick read it, naming the member and round the wiring decided on;
// next is the closed binding the repair stages onto.
func markerCloseRepair(ctx context.Context, rt Runtime, tx *store.Tx, b, next store.Binding, rec *store.GateRecord) (store.Binding, bool, bool, error) {
	closedRound := b.Round
	// The report is queued; a failing gate may now open round N+1, exactly as
	// Send would. A chain member's red gate is the chain's to spend: the
	// wiring already decided this tick whether a repair can run, on this
	// binding and this gate log, so the two agree. It opens the round only
	// when that decision allowed one; when it did not, the red event went to
	// the chain's reviewer and the member must be left alone rather than
	// halted. Only a done round may buy a repair: a member that reported
	// halted, blocked or unstructured halts the chain instead, whatever its
	// gate says.
	if halted, ok, herr := housekeep.HaltOnEnvironmentGate(ctx, HousekeepDeps(rt), tx, next, rec, closedRound); ok || herr != nil {
		return halted, true, false, herr
	}
	if rec != nil && rec.Result == "fail" && next.Regate > 0 && next.State != store.StateNeedsYou {
		repair := true
		if chain.OwnsMember(tx, b.Name) {
			repair, _ = repairDecision(next, gateSignature(rt.Store.ReadFile, rec.LogPath))
			repair = repair && closedReportOutcome(tx, b.Name, closedRound) == reporttail.OutcomeDone
		}
		if repair {
			var err error
			next, err = startRepairRound(ctx, rt, tx, next, *rec, closedRound)
			if err != nil {
				return next, true, false, err
			}
		}
	}
	return next, true, false, nil
}

// closedReportOutcome is the outcome the just-closed round's report entry
// recorded, or "" when the log holds none; the chain wiring reads it for the
// same round the close wrote, so the repair decision and the close agree.
func closedReportOutcome(tx *store.Tx, name string, round int) string {
	entries, err := tx.ReadLog(name)
	if err != nil {
		return ""
	}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Round == round && e.Direction == store.DirToMasterMind && e.Kind == store.KindReport {
			return e.Outcome
		}
	}
	return ""
}

func appendUnique(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}
