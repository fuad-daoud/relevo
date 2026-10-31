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
// marker that was already there. resumed is false, and nothing changes, for any
// round that does not owe the nudge.
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
// waits for its runner to exit before relevo gives up on it: a runner writes
// its final message just after the marker and then exits, so two minutes is
// generous, and a hung one must not hold the round forever.
const readerFinalMessageGrace = 2 * time.Minute

// readerSummaryEarlyNote is the note a reader round closes with when its
// runner outlived readerFinalMessageGrace: the summary is the stream as it was,
// not a completed final message.
const readerSummaryEarlyNote = "runner still running after its marker; summary taken early"

// holdReaderOnMarker reads a reader round's marker once and reports what the
// close must do with it. A reader's marker is not the end of its stream: the
// runner writes its final message just after the marker and then exits, and
// that message is the round's summary. So the round waits for the exit -- held
// is true while the process is alive inside readerFinalMessageGrace -- and a
// runner still alive after the grace is stopped the way `relevo stop` stops
// one, with early true so the caller closes on the stream as it is.
//
// present is false when the marker is not on disk this tick: the round is
// still live, and the caller must close nothing at all. That is what keeps the
// reader's marker read and its close on one observation: a marker written
// between this read and closeOnMarker's own would otherwise close the round
// the instant it appeared, before the runner printed the final message.
//
// A marker whose round has no report yet is held inside the grace even when
// its process is gone: the message can land after the marker while the
// process exits, so an empty stream must not close as noreport the moment the
// marker is seen. Past the grace a genuinely empty round is left to the
// ordinary close. An unreadable liveness check holds this tick, as the
// unmarked path treats it as alive.
func holdReaderOnMarker(ctx context.Context, rt Runtime, b store.Binding) (present, held, early bool, err error) {
	fi, serr := os.Stat(rt.Store.DonePath(b.Name, b.Round))
	if serr != nil {
		return false, false, false, nil // no marker yet: the round is live, close nothing
	}
	present = true
	withinGrace := rt.Now().Sub(fi.ModTime()) < readerFinalMessageGrace

	// A marker can appear before the runner prints its final message and
	// before it exits: while the round has no report and the grace has not
	// run out, hold the close so a message still coming is not lost to a
	// noreport entry. A live process is recorded so a later tick never
	// classifies it as lost to a restart.
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

// markerClose is the marker branch of reconcileHeadless: it calls
// closeOnMarker and, when the marker is present and the gate is done, runs the
// post-close sequence (served-round close, process clear, verify consult,
// delivery, repair round). The normal read and the re-check in the
// exited branch share it, so a marker that appears inside one tick is handled
// by one implementation instead of a copy of the post-close block.
//
// closed and gating are reported so the caller returns exactly what the marker
// branch does; a marker-absent read comes back unchanged with both false.
func markerClose(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, markerNote string, wantVerify bool) (store.Binding, bool, bool, error) {
	// A reader round closes on its runner's exit, not on the marker: the runner
	// may have written its output near the end, so the summary is only complete
	// once the process has gone. While the runner is alive the round stays
	// open; past the grace the runner is stopped and the round closes with the
	// summary taken early.
	//
	// The reader's marker is read exactly here, and a marker that is absent
	// this tick closes nothing: the round is still live and the next tick
	// re-reads it. Reading it here and again in closeOnMarker would let a
	// marker written between the two reads close the round the instant it
	// appeared, before the runner printed the final message the summary is
	// taken from.
	if b.Shape == store.ShapeReader {
		present, held, early, err := holdReaderOnMarker(ctx, rt, b)
		if err != nil {
			return b, false, false, err
		}
		if !present || held {
			return b, false, false, nil
		}
		if early {
			markerNote = joinNotes(markerNote, readerSummaryEarlyNote)
		} else if next, resumed, nerr := nudgePlanOnMarker(ctx, rt, tx, b, entries); nerr != nil {
			return b, false, false, nerr
		} else if resumed {
			return next, false, true, nil
		}
	}
	base := b.RoundBaselineTree
	next, closed, gating, rec, err := closeOnMarker(ctx, rt, tx, b, entries, markerNote)
	if err != nil {
		return b, false, false, err
	}
	if gating || !closed {
		return next, closed, gating, nil
	}

	closedRound := b.Round
	if next.Owner != "" && rt.CloseServedRound != nil {
		next = rt.CloseServedRound(ctx, rt, next)
	}
	next.Builder = clearProcess(next.Builder)
	// A runner that wrote its marker and then died inside the same tick is
	// gone; end its scope now, best effort. A live runner is left alone:
	// its own supervisor reaps the scope as it exits. The scope check leads,
	// so a scopes-off close pays for no liveness read.
	if rt.Scope != nil && !roundRunnerAlive(ctx, rt, b) {
		endRoundScope(ctx, rt, b, closedRound)
	}
	next.StalledSince = time.Time{}
	// The gate result -> report queued -> verify consult started ->
	// delivery, exactly as the pane path orders it: the reviewer
	// sees the gate's output, so it starts after the gate and before the
	// mastermind is told.
	if wantVerify && next.Shape != store.ShapeReader {
		gateLogPath := ""
		if rec != nil {
			gateLogPath = rec.LogPath
		}
		next, err = consult.StartVerify(ctx, consultDeps(rt), tx, next, closedRound, consult.VerifyDiffCommand(base, next.RoundClosedTree), gateLogPath)
		if err != nil {
			return next, true, false, err
		}
	}
	next, err = deliverAndSettle(ctx, rt, tx, next)
	if err != nil {
		return next, true, false, err
	}
	// The report is queued; a failing gate may now open round N+1, as a
	// fresh process, exactly as Send would. The failed
	// round's own report, diff and gate=fail stand.
	//
	// A chain member's red gate is the chain's to spend: the wiring already
	// decided in this tick whether a repair can run, on this binding and this
	// gate log, so the two agree. It opens the round only when that decision
	// allowed one; when it did not, the red event went to the chain's reviewer
	// and the member must be left alone rather than halted.
	//
	// Only a done round may buy a repair: a chain member that reported halted,
	// blocked or unstructured halts the chain instead, whatever its gate says
	// (the gate runs on the done marker, not on the report).
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
			next, err = startRepairRound(ctx, rt, tx, next, *rec, closedRound)
			if err != nil {
				return next, true, false, err
			}
		}
	}
	return next, true, false, nil
}

// closedReportOutcome is the outcome the just-closed round's report entry
// recorded, or "" when the log holds no such entry. The chain wiring reads it
// for the same round the close wrote, so the repair decision and the close
// agree on what the builder actually reported.
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

// appendUnique returns s with v appended, unless it is already present.
func appendUnique(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}
