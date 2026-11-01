package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/pathscope"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
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
	// The runner exited, so whatever the round's scope still holds is an
	// abandoned straggler: end it before anything below relaunches, switches
	// or nudges under the same unit name. Best effort -- the close must not
	// fail because a scope would not die.
	endRoundScope(ctx, rt, b, b.Round)

	// The exit code is read once, from the stream's trailer.
	codeText := "unknown"
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(b.Builder), rt.Store.StreamPath(b.Name, b.Round)); ok {
		codeText = strconv.Itoa(code)
	}

	// The marker can be written between the closeOnMarker read above and this
	// observation: a builder that writes its marker and exits inside one tick
	// must close as marked. Re-check the marker now.
	if _, _, ok, _ := rt.Store.StatFile(rt.Store.DonePath(b.Name, b.Round)); ok {
		slog.Debug("marker appeared before exit was observed", "binding", b.Name, "round", b.Round, "pid", b.Builder.PID)
		markerNote := ""
		if escapeCheck(ctx, rt, b, true) == git.EscapeNote {
			markerNote = git.EscapedNote
		}
		next, _, _, err := markerClose(ctx, rt, tx, b, entries, markerNote, wantVerify)
		if err != nil {
			return b, err
		}
		return next, nil
	}

	// Exited after writing a report but without the marker: an exited process
	// cannot be mid-write, so the report is trusted and the omission noted. A
	// reader whose deliverable is absent falls through to the
	// exit-without-report path.
	if b.Shape != store.ShapeReader || (readerDeliverablePresent(rt, b) && !readerPlanNudgeDue(rt, b, entries, codeText)) {
		reportPath, fellBack, serr := writeReaderSummary(rt, b)
		if serr != nil {
			slog.Warn("reader output not written", "binding", b.Name, "round", b.Round, "err", serr)
		}
		if _, _, ok, _ := rt.Store.StatFile(reportPath); ok {
			scanText, scanResetMS := limitScan(ctx, rt, b)
			_, m, _, err := gateOnLimit(ctx, rt, tx, b, scanText, scanResetMS, false)
			if err != nil {
				return b, err
			}
			slog.Warn("headless builder exited with a report but no marker", "binding", b.Name, "round", b.Round, "pid", b.Builder.PID, "code", codeText, "note", "unmarked")
			payload := fmt.Sprintf(
				"Builder exited (code %s) after writing its %s but never confirmed completion (no %s). %s.",
				codeText, artifactNoun(rt, b), filepath.Base(rt.Store.DonePath(b.Name, b.Round)), closeClause(rt, b, b.Round))
			if m.Line != "" {
				payload += fmt.Sprintf(" Provider rate-limited: %s; gated until %s.", m.Line, availability.GateTimeText(m.Until))
			}
			note := "unmarked"
			if fellBack {
				note = joinNotes(note, view.ReaderFallbackNote)
			}
			if escapeCheck(ctx, rt, b, true) == git.EscapeNote {
				note = joinNotes(note, git.EscapedNote)
			}
			next, err := queueReport(ctx, rt, tx, b, entries, reportPath, payload, note, nil, nil, nil, nil, "", false, "", pathscope.Verdict{})
			if err != nil {
				return b, err
			}
			if next.Owner != "" && rt.CloseServedRound != nil {
				// An unmarked close is still a close: record the served round's
				// facts as markerClose does, or the owner sees "idle" and
				// never fetches the report.
				next = rt.CloseServedRound(ctx, rt, next)
			}
			next.Builder = clearProcess(next.Builder)
			next.StalledSince = time.Time{}
			return deliverAndSettle(ctx, rt, tx, next)
		}
	}

	now := rt.Now().UTC()
	h, _ := harness.Lookup(b.Builder.Kind)
	c := builderCandidateOf(rt, b)
	tail := currentBuilderTail(rt, b, availability.LimitScanLines)
	denialLine, isDenial := matchDenial(tail, denialPatterns(c, h))
	suffix := text.DenialSuffix(denialLine, isDenial)

	// Detect the oom kill before the exit entry, so the entry says it; a
	// requested stop still wins and is checked below. A cgroup kill reads as
	// no trailer (unknown) or the supervisor's own 128+SIGKILL (137), so
	// both are probed.
	var oomPeak int64
	oom := false
	if codeText == "unknown" || codeText == "137" {
		if peak, killed := housekeep.OOMKilled(ctx, HousekeepDeps(rt), b); killed {
			oom, oomPeak = true, peak
			suffix += "; " + housekeep.OOMWords(peak)
		}
	}

	if err := tx.AppendLog(b.Name, exitEntry(now, b.Round, b.Builder.LogPath, codeText, suffix, builderTail(rt, b, logTailLines), b.Shape)); err != nil {
		return b, err
	}
	slog.Info("headless builder exited without a report", "binding", b.Name, "round", b.Round, "pid", b.Builder.PID, "code", codeText)

	// A stop requested for a round that then exited without a report closes
	// without a report and without a switch, because a stop is not a
	// failure. Belt-and-braces only, for a process killed between the request
	// and the close.
	if !b.StopRequestedAt.IsZero() {
		next, err := closeStopped(ctx, rt, tx, b, "killed")
		if err != nil {
			return next, err
		}
		if next.Owner != "" && rt.CloseServedRound != nil {
			// A served round that closed here must record its facts, or the
			// owner sees "idle" and never collects it.
			next = rt.CloseServedRound(ctx, rt, next)
		}
		return next, nil
	}

	// An oom kill is not the candidate's failure, so re-queue on the same
	// candidate with no switch and no exclusion.
	if oom {
		return housekeep.RequeueOOM(ctx, HousekeepDeps(rt), tx, b, oomPeak, now)
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
		if b.Owner != "" {
			// On a server, a box reboot must not relaunch every builder past
			// the cap: re-queue at the head of the queue instead of
			// relaunching. The local daemon keeps the relaunch
			// below.
			b.QueuedAt = b.RoundStartedAt
			b.RoundStartedAt = time.Time{}
			b = abandonSession(b)
			b.Builder = clearProcess(b.Builder)
			if err := tx.AppendLog(b.Name, store.LogEntry{
				TS: now, Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true,
				Note: "re-queued (builder lost to a restart)",
			}); err != nil {
				return b, err
			}
			slog.Info("headless builder re-queued after daemon restart", "binding", b.Name, "round", b.Round)
			return b, nil
		}

		// A candidate the configured set no longer holds cannot be resumed or
		// relaunched: pick again the way a switch does, at the accepted cost
		// that switchBuilder restarts RoundStartedAt.
		if staleBuilder(rt, b) {
			return switchBuilder(ctx, rt, tx, b,
				"lost to a daemon restart; candidate "+b.BuilderCandidate+" is no longer configured", false, false)
		}

		text := roundPrompt(rt, tx, b, rt.Store.PromptPath(b.Name, b.Round), rt.Store.ReportPath(b.Name, b.Round), rt.Store.DonePath(b.Name, b.Round)) +
			"\n\n" + interruptedNote(rt.StartedAt)
		keep := b.RoundStartedAt
		// Read the round's session before anything clears it: startProcess
		// clears StreamSessionID, because a new process begins a new session.
		sess := b.Builder.StreamSessionID
		oldKind := b.Builder.Kind
		prior := peekUsage(ctx, rt, b, now)
		var (
			next store.Binding
			err  error
			how  = "relaunched"
		)
		if sess != "" {
			next, err = resumeRound(ctx, rt, tx, b, sess, text, false)
			switch {
			case err == nil:
				how = "resumed session " + sess
			case errors.Is(err, harness.ErrResumeUnsupported):
				// An unresumable kind falls back to a fresh relaunch, which is round
				// 1's behaviour and needs no note.
				next, err = startRound(ctx, rt, tx, b, text, false)
			default:
				var sf spawnFailure
				if errors.As(err, &sf) {
					// The harness would not start with the resume selector:
					// one fresh attempt, then the halt below if that fails too.
					slog.Warn("resume failed; relaunching fresh", "binding", b.Name, "round", b.Round, "session", sess, "err", err)
					next, err = startRound(ctx, rt, tx, b, text, false)
				}
			}
		} else {
			next, err = startRound(ctx, rt, tx, b, text, false)
		}
		if err != nil {
			b = abandonSessionID(b, oldKind, sess)
			return haltAndSettle(ctx, rt, tx, b, fmt.Sprintf("%s: builder lost to a daemon restart and could not be relaunched: %v", b.Name, err))
		}
		b = next
		// The new process has StreamSessionID "": reapable waits until it
		// announces its own session, because the fork reads the old one.
		b = abandonSessionID(b, oldKind, sess)
		// The round's budget clock survives the restart: the interruption is
		// relevo's, so it must not buy the round more time than it had.
		b.RoundStartedAt = keep
		if err := tx.AppendLog(b.Name, store.LogEntry{
			TS: now, Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindSwitch, Confirmed: true,
			Usage: prior,
			Note: fmt.Sprintf("%s builder (lost to a daemon restart at %s): picked %s for builder: same candidate, not counted",
				how, rt.StartedAt.UTC().Format(time.RFC3339), b.BuilderCandidate),
		}); err != nil {
			return b, err
		}
		b.State = store.StateActive
		// The round is running again, so a halt the attempt lost to the daemon
		// restart left goes with it, every field: the notification key it still
		// carries dedups the next halt of this same round. OwedHalt is left
		// alone -- a pending notification for an already closed round, still
		// owed -- as the other revive paths leave it (bind.go).
		b = ClearHaltFields(b)
		slog.Info("headless builder relaunched after daemon restart", "binding", b.Name, "round", b.Round, "candidate", b.BuilderCandidate)
		return b, nil
	}

	// The escape halt comes before gateOnLimit: a limit line in an escaped
	// round's log must not turn a halt into a switch.
	if escapeCheck(ctx, rt, b, false) == git.EscapeHalt {
		return haltAndSettle(ctx, rt, tx, b, escapeDiagnosis(b, codeText))
	}

	scanText, scanResetMS := limitScan(ctx, rt, b)
	next, _, handled, err := gateOnLimit(ctx, rt, tx, b, scanText, scanResetMS, false)
	if handled {
		return next, err
	}

	if isDenial {
		return haltAndSettle(ctx, rt, tx, b, fmt.Sprintf(
			"%s: builder exited (code %s) %s after a permission denial (%q); not switched -- re-send with a higher tier (relevo send --name %s --file <plan> --tier edit|yolo [--allow-yolo]) or extend the harness's allow list; log: %s",
			b.Name, codeText, text.WithoutArtifact(b.Shape == store.ShapeReader), denialLine, b.Name, text.ShowCommand(b.Name, b.Round, "log")))
	}

	// A builder that ended its turn cleanly without a report has left a session
	// nothing will wake: resume it once with a nudge rather than spend a
	// switch on work that is nearly done. Only a clean end earns it, under
	// nudgeResume's own code gate.
	if classifyRoundEnd(roundEndCase{TurnEnd: roundTurnEnd(rt, b), Code: codeText}) == roundEndNudge {
		if next, resumed, err := nudgeResume(ctx, rt, tx, b, entries, codeText, now); err != nil {
			return next, err
		} else if resumed {
			slog.Info("headless builder nudged to finish", "binding", b.Name, "round", b.Round, "session", b.Builder.StreamSessionID)
			return next, nil
		}
	}

	if b.Shape == store.ShapeReader && codeText == "0" {
		return haltAndSettle(ctx, rt, tx, b, readerUndeliveredReason(rt, b, nudgesSincePlan(entries, b.Round)))
	}

	if !switchable {
		return haltAndSettle(ctx, rt, tx, b, fmt.Sprintf("%s: builder exited (code %s) %s; see %s", b.Name, codeText, text.WithoutArtifact(b.Shape == store.ShapeReader), text.ShowCommand(b.Name, b.Round, "log")))
	}
	if next, ok, err := sameSessionRecovery(ctx, rt, tx, b, entries, now); err != nil {
		return next, err
	} else if ok {
		return next, nil
	}
	// A provider-side outage behind this exit is a provider gate, not a builder
	// failure: one rate_limited entry keeping the cause, and the builder
	// switched uncounted, so the candidate is eligible again as soon as the
	// provider recovers. It sits at the exclusion decision and nowhere
	// earlier, so every path above keeps its order, and a tail naming no
	// provider fault falls through to the exclusion below. No repeat-record
	// guard is needed: the switch restarts RoundStartedAt and the stream
	// cursor, so the next decision scans a different process's bytes.
	outaged, gated, gerr := gateOnOutage(ctx, rt, tx, b, limitText(ctx, rt, b))
	if gerr != nil {
		return outaged, gerr
	}
	if gated {
		return outaged, nil
	}
	// The exclusion rides the b switchBuilder receives so the replacement
	// inherits it and the field persists with the switch: a builder that
	// exited without a report is excluded from the pick for the rest of this
	// round.
	b.RoundExcluded = appendUnique(b.RoundExcluded, b.BuilderCandidate)
	return switchBuilder(ctx, rt, tx, b, fmt.Sprintf("exited (code %s) %s", codeText, text.WithoutArtifact(b.Shape == store.ShapeReader)), false, true)
}
