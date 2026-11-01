package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// reconcileHeadless is one tick of a headless binding: halt on the round cap,
// then decide. A report file finishes the round whatever the process did --
// the report is the contract.
func reconcileHeadless(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, error) {
	if b.Round > b.RoundCap {
		return haltAndSettle(ctx, rt, tx, b, fmt.Sprintf("%s: %s of %d", b.Name, ErrRoundCap, b.RoundCap))
	}

	// Drain before anything below reads the log, so the exit entry's tail and
	// the limit scan see a current log.
	b = drainStream(rt, b)

	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return b, err
	}
	if !store.RoundOpen(entries, b.Round) {
		return reconcileIdleNoRound(ctx, rt, tx, b)
	}

	// A queued round has no process and no clocks: nothing to reconcile until
	// relevo.Admit starts it. This precedes the PID == 0 branch below, or a
	// queued round would read as a failed spawn.
	if !b.QueuedAt.IsZero() {
		return b, nil
	}

	// The marker close runs in reconcileMarkedClose below.
	// queueReport's reset clears RoundVerify: read the verify flag before the
	// close consumes it, as the pane path does.
	wantVerify := b.RoundVerify
	next, done, err := reconcileMarkedClose(ctx, rt, tx, b, entries, wantVerify)
	if err != nil {
		return b, err
	}
	if done {
		return next, nil
	}

	if b.Builder.PID == 0 {
		// Round open, no process, no report: a send whose Start failed. Send
		// already put the binding in NEEDS YOU with the spawn_failed in the
		// ledger; nothing to observe until the human acts.
		return b, nil
	}
	if rt.Runner == nil {
		slog.Warn("headless binding but no Runner configured", "binding", b.Name)
		return b, nil
	}

	switchable := b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero()
	if switchable {
		if g, gated := gatedBuilder(rt, b); gated {
			reason := "rate-limited"
			if g.Note != "" {
				reason = "rate-limited: " + g.Note
			}
			return switchBuilder(ctx, rt, tx, b, reason, true, false)
		}
	}

	alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
	if err != nil {
		// An OS hiccup is not evidence the builder stopped: treat it as alive
		// this tick and say so.
		slog.Warn("headless liveness check failed; treating as alive", "binding", b.Name, "pid", b.Builder.PID, "err", err)
		alive = true
	}
	if err == nil && alive {
		// A sighting: this daemon now knows the process is alive, so no
		// later tick classifies it as lost to a restart.
		rt.Watched.Mark(b.Builder.PID, b.Builder.StartedAt)
	}
	if alive {
		return reconcileLiveBuilder(ctx, rt, tx, b, entries)
	}

	// The exited builder is reconciled in reconcileExitedBuilder below.
	return reconcileExitedBuilder(ctx, rt, tx, b, entries, wantVerify, switchable)
}

// reconcileIdleNoRound settles a tick with no open round: a stray process is
// stopped rather than left editing a tree nobody is watching, a binding
// broken by a fault already over goes active again, and the tick delivers.
func reconcileIdleNoRound(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, error) {
	// Idle is normal between rounds. One with no round is a stray whose
	// round closed some other way, and relevo stops it rather than let
	// it edit a tree nobody is watching.
	if b.Builder.PID != 0 {
		if rt.Runner != nil {
			// No round is open, nothing reads this handle's exit, and
			// b.Round may be 0, which is why Kill gets "".
			if err := rt.Runner.Kill(ctx, handleOf(b.Builder), ""); err != nil {
				slog.Warn("stray headless process not killed", "binding", b.Name, "pid", b.Builder.PID, "err", err)
			} else {
				slog.Warn("killed stray headless process", "binding", b.Name, "pid", b.Builder.PID)
				b = abandonSession(b)
				// Belt for the close hook this stray's round already ran: no
				// kill route leaves a round's temp dir behind.
				housekeep.RemoveRoundTmp(HousekeepDeps(rt), b, b.Round)
			}
		}
		b.Builder = clearProcess(b.Builder)
		b.StalledSince = time.Time{}
	}
	if b.State == store.StateBroken {
		b.State = store.StateActive
		// The fault that broke the binding is over, so the halt that
		// recorded it goes with it: left in place the next send is
		// refused with 409 round_halted naming a switch already replaced.
		b = ClearHaltFields(b)
	}
	return deliverAndSettle(ctx, rt, tx, b)
}

// reconcileMarkedClose runs the marker close for an open round: the marker is
// the contract, so a present marker closes the round whatever the process did.
// done reports whether the tick ends here with next to return.
func reconcileMarkedClose(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, wantVerify bool) (store.Binding, bool, error) {
	// The marker is the contract: the process is done with whatever it exits
	// as, and it exits on its own. Never Kill here. A report without a marker
	// is a process still working. The escape check runs before closeOnMarker,
	// not inside
	// it, and only when the marker is there: queueReport clears
	// RoundBaselineTree, and the snapshot must be compared while still on b.
	// A live round pays no git subprocess every tick for it.
	markerNote := ""
	if _, _, ok, _ := rt.Store.StatFile(rt.Store.DonePath(b.Name, b.Round)); ok {
		if escapeCheck(ctx, rt, b, true) == git.EscapeNote {
			markerNote = git.EscapedNote
		}
	}
	next, closed, gating, err := markerClose(ctx, rt, tx, b, entries, markerNote, wantVerify)
	if err != nil {
		return b, false, err
	}
	if gating || closed {
		return next, true, nil
	}
	return b, false, nil
}

// reconcileLiveBuilder advances a round whose builder is still alive: the
// round timeout, the progress clock with its stall handling, and the
// rate-limit gate scan, then the delivery.
func reconcileLiveBuilder(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry) (store.Binding, error) {
	now := rt.Now().UTC()
	next, halted, err := checkRoundTimeout(ctx, rt, tx, b)
	if halted {
		return next, err // the halt queued its entry and settled delivery itself
	}
	if err != nil {
		return b, err
	}
	// The progress clock reads the tree and the stream as two signals, and
	// a stall is quiet on both. The read is gated on the interval: a tick
	// inside it samples nothing.
	if view.ProgressDue(next, now, rt.Policy.ProgressInterval()) {
		next = progressStep(rt, next, now, sampleSignals(ctx, rt, next))
		if err := noteResumedIdle(tx, b, next, now); err != nil {
			return next, err
		}
		if acted, handled, err := actOnSilence(ctx, rt, tx, next, entries, now); err != nil || handled {
			return acted, err
		}
	}
	// A builder quiet on both signals may sit on a provider rate-limit
	// answer it never got to exit and report, so the stall is the gate:
	// scan the current process's own harness lines and, on a match, take
	// the gateOnLimit -> switchBuilder path, uncounted and with closeOld
	// (the call's bare `true`), so the replacement starts on the same
	// round. Only a stalled binding is scanned, and the
	// scan is limit patterns only, keeping escape and denial precedence at
	// exit. stallLimitGateRecorded skips the block once this round has
	// recorded one, since a repeat tick reads the same unchanged tail.
	if !next.StalledSince.IsZero() && !stallLimitGateRecorded(rt, next) {
		scanText, scanResetMS := limitScan(ctx, rt, next)
		gated, _, handled, gerr := gateOnLimit(ctx, rt, tx, next, scanText, scanResetMS, true)
		if gerr != nil {
			return next, gerr
		}
		if handled {
			// A switch tick does not deliver: replacing the builder is the
			// whole of what this tick did.
			return gated, nil
		}
	}
	return deliverAndSettle(ctx, rt, tx, next)
}

// reconcileExitedBuilder reconciles a round whose builder exited: the marker
// re-check, the unmarked-report close, and the exit without a report path
// below it. The save and deliver order of the original tick is kept, and the
// escape halt keeps its precedence before the rate-limit gate.
func reconcileExitedBuilder(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, wantVerify, switchable bool) (store.Binding, error) {
	codeText := exitScopeAndCode(ctx, rt, b)
	if next, done, err := exitLateMarker(ctx, rt, tx, b, entries, wantVerify); err != nil || done {
		return next, err
	}
	if next, done, err := exitUnmarkedReport(ctx, rt, tx, b, entries, codeText); err != nil || done {
		return next, err
	}
	oom, oomPeak, tail, denialLine, isDenial, now, err := exitSignalsAndEntry(ctx, rt, tx, b, codeText)
	if err != nil {
		return b, err
	}
	if next, done, err := exitCloseStopped(ctx, rt, tx, b); err != nil || done {
		return next, err
	}
	if next, done, err := exitRequeueOOM(ctx, rt, tx, b, oom, oomPeak, now); err != nil || done {
		return next, err
	}
	// A daemon kill (systemd restart, kill -9 of the process tree) takes the
	// supervisor before it writes the relevo-exit: trailer, which reads exactly
	// like a real builder death: code unknown. The daemon tells the two apart
	// by its own start time and which processes it has seen alive. Recovery
	// keeps the same candidate on the same round, uncounted, so a daemon
	// restart does not spend the round's switch budget. A lost local builder
	// first resumes its own harness session, falling back to a fresh relaunch
	// when the harness cannot resume. Both carry the interrupted note, stay
	// uncounted and keep RoundStartedAt.
	lost := codeText == "unknown" && lostToRestart(rt, b.Builder.PID, b.Builder.StartedAt)

	b.Builder.PID, b.Builder.StartedAt = 0, 0 // LogPath stays: status and the entry point at it
	b.StalledSince = time.Time{}              // the process is gone: not stalled any more

	if next, halted, herr := housekeep.HaltOnDiskFullExit(ctx, HousekeepDeps(rt), tx, b, tail); halted || herr != nil {
		return next, herr
	}
	if lost && switchable {
		if next, done, err := exitLostHead(ctx, rt, tx, b, now); err != nil || done {
			return next, err
		}
		return exitLostResume(ctx, rt, tx, b, now)
	}
	if next, done, err := exitHaltGate(ctx, rt, tx, b, codeText, denialLine, isDenial); err != nil || done {
		return next, err
	}
	return exitTailSwitch(ctx, rt, tx, b, entries, codeText, now, switchable)
}
