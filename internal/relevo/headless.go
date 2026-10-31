package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// ErrBuilderBusy reports a send against a headless binding whose previous
// round's process is still running (headless spec §5.2). One process per
// round is the model; two at once in one tree would race each other's
// edits. Being a refusal.Sentinel, anything wrapping it also unwraps to
// refusal.ErrRefused, so the CLI classifies the busy send as refused (exit 2) rather
// than internal without each call site naming both sentinels.
var ErrBuilderBusy = refusal.Sentinel("the previous process is still running; wait for the round to close, or relevo done")

// ErrReportPending reports a send refused because the current round already
// has its completion marker or report on disk, but the daemon has not yet
// ingested the close. The round is over: restaging its plan and starting a
// second builder would make the daemon close on the stale marker and deliver
// the old report. The caller retries once the report is delivered.
var ErrReportPending = refusal.Sentinel("the round's output is on disk but not yet delivered; relevo wait delivers it, then send the next round")

// ErrScopeActive reports a send refused because this round's systemd scope
// unit is still loaded: a builder for the round is already alive, most
// likely started by an earlier send whose bookkeeping failed (#445). Nothing
// was spawned and nothing was saved.
var ErrScopeActive = refusal.Sentinel("this round's builder scope is still running")

// isScopeBusy reports whether a Start error is the host refusing this round's
// scope unit rather than the candidate failing to run: systemd-run's own
// already-loaded wording, and its variant about an existing fragment file.
// Both are text matching because systemd surfaces the collision
// as a wrapped command error, not as a sentinel this binary can type.
//
// A host-side collision says nothing about the candidate, which is exactly
// why startProcess must not treat one as a spawn failure.
func isScopeBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "already loaded") || strings.Contains(msg, "has a fragment file")
}

// handleOf is the endpoint's stored process fields as the Runner's handle.
// StartedAt is Unix seconds on the endpoint (store spec §3.1, amended).
func handleOf(e store.Endpoint) spawn.ProcHandle {
	return spawn.ProcHandle{PID: e.PID, StartedAt: time.Unix(e.StartedAt, 0)}
}

// scopeKind is what a scoped spawn is: the word between "relevo-" and the
// owner in its unit name (#313).
type scopeKind string

const (
	scopeRound   scopeKind = "round"
	scopeGate    scopeKind = "gate"
	scopeConsult scopeKind = "consult"
	scopeVerify  scopeKind = "verify"
)

// scopeOwner8 is the owner component of a unit name: "local" for a binding
// with no owner; the first 8 hex characters of the owning client's id when
// Owner parses as a ClientID; "unknown" otherwise (which never happens for
// what the server itself wrote, but must never block a spawn).
func scopeOwner8(owner string) string {
	if owner == "" {
		return "local"
	}
	if dir, ok := remote.ClientID(owner).Dir(); ok {
		return dir[:8]
	}
	return "unknown"
}

// scopeUnitNameFor is the systemd scope unit's base name (the runner appends
// ".scope") for any scoped spawn (#244, #216, #313): "relevo-<kind>-<owner8>-
// <name>-<round>", followed by "-<id>" when id is non-empty. owner8 and the
// sanitising are shared with the round path, so relevo-round-* names are
// byte-identical to before.
func scopeUnitNameFor(kind scopeKind, owner, name string, round int, id string) string {
	unit := "relevo-" + string(kind) + "-" + scopeOwner8(owner) + "-" + safeUnitPart(name) + "-" + strconv.Itoa(round)
	if id != "" {
		unit += "-" + safeUnitPart(id)
	}
	return unit
}

// scopeUnitName is the per-round systemd scope unit's base name (the
// runner appends ".scope"): "relevo-round-<owner8>-<name>-<round>" (#244,
// #216).
func scopeUnitName(b store.Binding) string {
	return scopeUnitNameFor(scopeRound, b.Owner, b.Name, b.Round, "")
}

// scopeFor builds a spawn's ScopeSpec from the runtime's template (#313, #314):
// nil when the template is nil (scopes off), else a copy of the template with
// Unit set, GateCPUQuota zeroed, and AllowedCPUs set to cpus when cpus is
// non-empty. When kind is scopeGate and the template sets GateCPUQuota, the
// gate's spec uses it as CPUQuota; every other kind keeps the template's
// CPUQuota. It never mutates rt.Scope.
func scopeFor(rt Runtime, kind scopeKind, unit, cpus string) *spawn.ScopeSpec {
	if rt.Scope == nil {
		return nil
	}
	s := *rt.Scope
	s.Unit = unit
	s.GateCPUQuota = ""
	if cpus != "" {
		s.AllowedCPUs = cpus
	}
	if kind == scopeGate && rt.Scope.GateCPUQuota != "" {
		s.CPUQuota = rt.Scope.GateCPUQuota
	}
	return &s
}

// safeUnitPart replaces every rune outside [A-Za-z0-9:_.-] with '-', for a
// string destined for a systemd unit name.
func safeUnitPart(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ':', r == '_', r == '.', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	return sb.String()
}

// defaultRoundBudget mirrors the store's default round timeout, for a
// binding that was never saved. Store.Save fills RoundTimeoutMS on every
// real binding, so this is a guard, not a policy.
const defaultRoundBudget = 24 * time.Hour

// roundBudget is the binding's round budget as a duration: what agy's
// --print-timeout gets, so the harness's own default (5m) never cuts a
// round short (headless spec §3.5).
func roundBudget(b store.Binding) time.Duration {
	if b.RoundTimeoutMS <= 0 {
		return defaultRoundBudget
	}
	return time.Duration(b.RoundTimeoutMS) * time.Millisecond
}

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
	// delivery (#144), exactly as the pane path orders it: the reviewer
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
	// fresh process, exactly as Send would (#132 part 2). The failed
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

// interruptedNoteFormat is the relaunch prompt's note (#370, spec §4.3): a
// constant text with the interruption time interpolated. It tells the
// builder three things: the round was interrupted by a relevo daemon restart
// at T; the working tree may already hold partial edits from an earlier
// attempt at this same plan, and those edits are its own; and it should run
// git status and git diff first, keep what is correct and finish the plan.
const interruptedNoteFormat = `This round was interrupted at %s by a relevo daemon restart. The working tree may already hold partial edits from an earlier attempt at this same plan, and those edits are your own work, not someone else's. Run "git status" and "git diff" first, keep whatever is correct, and finish the plan.`

// interruptedNote renders interruptedNoteFormat for the daemon restart at t
// (#370, spec §4.3). t is rendered as UTC RFC 3339, the same shape the
// relaunch log entry uses.
func interruptedNote(t time.Time) string {
	return fmt.Sprintf(interruptedNoteFormat, t.UTC().Format(time.RFC3339))
}

// ErrStopFailed reports that relevo marked a binding done or unbound but
// could not stop its headless process (spec §4.6). The state change stands;
// the pid stays on the endpoint (done) or in the result (unbind) so the
// human can find the process.
var ErrStopFailed = errors.New("could not stop the builder process")

// stopProcess kills a headless endpoint's live process, if it has one. It
// returns the pid it addressed -- 0 when there was nothing to stop -- and
// Kill's error. A binding with no headless process, or one between rounds, is
// a no-op. Runner nil with a pid recorded is an error: relevo cannot say the
// process is stopped.
func stopProcess(ctx context.Context, rt Runtime, b store.Binding, why string) (int, error) {
	if !b.Builder.Headless() || b.Builder.PID == 0 {
		return 0, nil
	}
	if rt.Runner == nil {
		return b.Builder.PID, spawn.ErrRunnerUnavailable
	}
	if err := rt.Runner.Kill(ctx, handleOf(b.Builder), rt.Store.StreamPath(b.Name, b.Round)); err != nil {
		return b.Builder.PID, err
	}
	// The kill took the round's process, so its temp dir goes with it: a
	// later start recreates it under the same name, which is what makes
	// removing on a kill safe for a round that resumes.
	housekeep.RemoveRoundTmp(HousekeepDeps(rt), b, b.Round)
	return b.Builder.PID, nil
}

// statusTailLines is how much of the log `relevo status` shows under a
// headless builder line (spec §4.8).
const statusTailLines = 3

// headlessStatus is what `relevo status` says about a headless endpoint: the
// status word and the process details. Idle between rounds; otherwise a live
// Alive check, then, for an exited process, the trailer's code. No Runner
// means relevo cannot say. A live process whose stream has gone quiet
// (binding.StalledSince, #252) reads "stalled <age>" in place of "working".
//
// detail false builds no tail: the read is a whole-file logTail or a 64 KiB
// window per row per tick, and the fleet path paints none of it. The flag
// reaches here rather than the call site so the read is never made and then
// thrown away.
//
// alive is the report's batched liveness reading, so a refresh probes ps once
// rather than once per row. A nil map is the single-row path, which probes its
// one handle directly.
func headlessStatus(ctx context.Context, rt Runtime, b store.Binding, detail bool, alive map[spawn.ProcHandle]aliveAnswer) (string, *view.HeadlessInfo) {
	e := b.Builder
	info := &view.HeadlessInfo{PID: e.PID, LogPath: e.LogPath}
	if e.StartedAt != 0 {
		info.StartedAt = time.Unix(e.StartedAt, 0)
	}
	// The tail is read before the pid and runner checks, not after them: a
	// binding keeps its LogPath after its builder exits (reconcileHeadless
	// zeroes the pid and leaves the log), so a row with no process is still a
	// row whose log a human reads to find out why it stopped. Skipping the
	// read here would drop that tail from the surfaces that show it.
	if detail && e.LogPath != "" {
		if tail := builderTail(rt, b, statusTailLines); tail != "" {
			info.Tail = strings.Split(tail, "\n")
		}
	}
	if e.PID == 0 {
		return "idle", info
	}
	if rt.Runner == nil {
		return "unknown", info
	}
	h := handleOf(e)
	running, known := builderAlive(ctx, rt, h, alive)
	if !known {
		return "unknown", info
	}
	if !running {
		return exitedStatus(ctx, rt, b, info), info
	}
	// #252's label is now one of #135's progress labels, so a live
	// headless builder can also read "exploring <age>".
	working, _ := view.LabelsOf(b, rt.Now())
	if working = silenceLabel(rt, b, rt.Now(), working); working != "" {
		return working, info
	}
	return "working", info
}

// exitedStatus is the word and the code for a process that is no longer there:
// the trailer's code when the supervisor wrote one, else "unknown". Split out of
// headlessStatus so the tail read and the exit read stay legible next to each
// other.
func exitedStatus(ctx context.Context, rt Runtime, b store.Binding, info *view.HeadlessInfo) string {
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(b.Builder), rt.Store.StreamPath(b.Name, b.Round)); ok {
		info.ExitCode = strconv.Itoa(code)
		return "exited " + info.ExitCode
	}
	info.ExitCode = "unknown"
	return "exited"
}

// builderAlive is the report's batched liveness answer for h, or a direct probe
// when the report carries none. It reports ok=false when the process could not
// be probed at all, which is how "unknown" stays distinct from "exited": a ps
// that could not run says nothing about the process, and the row says so.
func builderAlive(ctx context.Context, rt Runtime, h spawn.ProcHandle, alive map[spawn.ProcHandle]aliveAnswer) (bool, bool) {
	if got, ok := alive[h]; ok {
		return got.alive, got.known
	}
	got, err := rt.Runner.Alive(ctx, h)
	if err != nil {
		return false, false
	}
	return got, true
}
