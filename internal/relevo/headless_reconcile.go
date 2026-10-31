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

// reconcileHeadless is one tick of a headless binding: reconcile hands off
// here right after the DONE gate, and the pane path never runs for a
// headless endpoint.
//
// Order: halt on the round cap, then decide. A report file finishes the round
// whatever the process did (the report is the contract). Otherwise: a gated
// candidate is switched with its process killed; a live process is left alone,
// its budget the only thing that can halt it; a process that exited is logged
// with its code and log tail and the builder is switched, up to max_switches.
// No round open means idle -- never BROKEN -- and a process lingering with no
// round is a stray relevo stops.
func reconcileHeadless(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, error) {
	if b.Round > b.RoundCap {
		return haltAndSettle(ctx, rt, tx, b, fmt.Sprintf("%s: %s of %d", b.Name, ErrRoundCap, b.RoundCap))
	}

	// Render what the builder streamed since the last tick before anything
	// below reads the log, so the exit entry's tail and the limit scan see
	// a current log.
	b = drainStream(rt, b)

	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return b, err
	}
	roundOpen := store.RoundOpen(entries, b.Round)

	if !roundOpen {
		// Idle is normal: between rounds there is no process.
		// One with no round is a stray -- a send whose round closed some
		// other way -- and relevo stops it rather than let it edit a tree
		// nobody is watching.
		if b.Builder.PID != 0 {
			if rt.Runner != nil {
				// No round is open, nothing reads this handle's exit, and
				// b.Round may be 0.
				if err := rt.Runner.Kill(ctx, handleOf(b.Builder), ""); err != nil {
					slog.Warn("stray headless process not killed", "binding", b.Name, "pid", b.Builder.PID, "err", err)
				} else {
					slog.Warn("killed stray headless process", "binding", b.Name, "pid", b.Builder.PID)
					b = abandonSession(b)
					// Belt for the close hook this stray's round already ran:
					// no kill route leaves a round's temp dir behind.
					housekeep.RemoveRoundTmp(HousekeepDeps(rt), b, b.Round)
				}
			}
			b.Builder = clearProcess(b.Builder)
			b.StalledSince = time.Time{}
		}
		if b.State == store.StateBroken {
			b.State = store.StateActive
			// The fault that broke the binding is over, so the halt that
			// recorded it goes with it. Left in place it reads as the reason the
			// round is still stopped: the next send is refused with 409
			// round_halted naming a switch that has already been replaced, and
			// the round classifies as halted for as long as the binding lives.
			b = ClearHaltFields(b)
		}
		return deliverAndSettle(ctx, rt, tx, b)
	}

	// A queued round has no process and no clocks: nothing to reconcile
	// until relevo.Admit starts it. This precedes the PID == 0 "spawn failed
	// earlier" branch below, or a queued round would be mistaken for a
	// failed spawn.
	if !b.QueuedAt.IsZero() {
		return b, nil
	}

	// The marker is the contract: the process is done with whatever it
	// exits as, and it exits on its own. Never Kill here. A report without
	// a marker is a process still working.
	//
	// The escape check runs before closeOnMarker, not inside it: queueReport
	// clears RoundBaselineTree, and the snapshot must be compared against it
	// while it is still on b. It runs only when the marker is there --
	// the close is the only reader of its answer -- so a live round pays two
	// git subprocesses once, at its close, not every tick under the state
	// lock: the close pays two git subprocesses once, instead of every tick
	// paying two under the state lock -- the largest cost of a live headless
	// tick, warning every tick when git cannot read the repository.
	markerNote := ""
	if _, _, ok, _ := rt.Store.StatFile(rt.Store.DonePath(b.Name, b.Round)); ok {
		if escapeCheck(ctx, rt, b, true) == git.EscapeNote {
			markerNote = git.EscapedNote
		}
	}
	// queueReport's reset block clears RoundVerify: read the round's verify
	// flag before the close consumes it, as the pane path does.
	wantVerify := b.RoundVerify
	next, closed, gating, err := markerClose(ctx, rt, tx, b, entries, markerNote, wantVerify)
	if err != nil {
		return b, err
	}
	if gating || closed {
		return next, nil
	}

	if b.Builder.PID == 0 {
		// Round open, no process, no report: a send whose Start failed.
		// Send already put the binding in NEEDS YOU and the ledger has the
		// spawn_failed; nothing to observe until the human acts.
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
		// An OS hiccup is not evidence the builder stopped: treat it as
		// alive this tick and say so.
		slog.Warn("headless liveness check failed; treating as alive", "binding", b.Name, "pid", b.Builder.PID, "err", err)
		alive = true
	}
	if err == nil && alive {
		// A sighting: this daemon now knows the process is alive, so no
		// later tick classifies it as lost to a restart.
		rt.Watched.Mark(b.Builder.PID, b.Builder.StartedAt)
	}
	if alive {
		now := rt.Now().UTC()
		next, halted, err := checkRoundTimeout(ctx, rt, tx, b)
		if halted {
			return next, err // the halt queued its entry and settled delivery itself
		}
		if err != nil {
			return b, err
		}
		// The process is alive; the progress clock reads the tree and the
		// stream as two signals, and a stall is quiet on both. The read is
		// gated on the interval: a tick inside it samples nothing.
		if view.ProgressDue(next, now, rt.Policy.ProgressInterval()) {
			next = progressStep(rt, next, now, sampleSignals(ctx, rt, next))
			if err := noteResumedIdle(tx, b, next, now); err != nil {
				return next, err
			}
			if acted, handled, err := actOnSilence(ctx, rt, tx, next, entries, now); err != nil || handled {
				return acted, err
			}
		}
		// A builder that went quiet on both signals is not necessarily
		// working: it may be sitting on a provider's rate-limit
		// answer it never got to report, because it never got to exit. The
		// exit path already gates on the same tail, but only once the
		// process is gone -- a stalled one can sit there until the round's
		// budget runs out. So the stall is the gate: scan the current
		// process's own harness lines (limitText) and, on a match, take the
		// existing gateOnLimit -> switchBuilder path, uncounted and with
		// closeOld, so the replacement starts on the same round.
		//
		// Nothing else about this tick changes: only a stalled binding is
		// scanned, so an alive tick inside stall_after_ms still pays no
		// stream read, and a stall with no limit line in its tail falls
		// through to deliverAndSettle exactly as before. Escape and denial
		// text keep their precedence at exit -- this scan is limit patterns
		// only, and never reads anything the exit path would have handled.
		//
		// A repeat tick on the same stall reads the same unchanged tail, so
		// it must not record the same gate again: stallLimitGateRecorded
		// skips the whole block once this round has recorded one.
		if !next.StalledSince.IsZero() && !stallLimitGateRecorded(rt, next) {
			scanText, scanResetMS := limitScan(ctx, rt, next)
			gated, _, handled, gerr := gateOnLimit(ctx, rt, tx, next, scanText, scanResetMS, true)
			if gerr != nil {
				return next, gerr
			}
			if handled {
				// A switch tick does not deliver, like every other switch:
				// replacing the builder is the whole of what this tick did.
				return gated, nil
			}
		}
		return deliverAndSettle(ctx, rt, tx, next)
	}

	// The runner exited, so whatever the round's scope still holds is an
	// abandoned straggler: end it before anything below relaunches, switches
	// or nudges under the same unit name. Best effort -- the round's close
	// must not fail because a scope would not die.
	endRoundScope(ctx, rt, b, b.Round)

	// Exited. The exit code is read once, from the stream's trailer.
	codeText := "unknown"
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(b.Builder), rt.Store.StreamPath(b.Name, b.Round)); ok {
		codeText = strconv.Itoa(code)
	}

	// The marker can be written between the closeOnMarker read above and this
	// liveness observation: a builder that writes its marker and exits inside
	// one tick must close as marked, not as unmarked. Re-check the marker
	// now and close through the marker path if it is there.
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

	// Exited after writing a report but without the marker: an exited
	// process cannot be mid-write, so the report is trusted and the
	// omission noted. A reader whose deliverable is absent
	// skips this and falls through to the exit-without-report path.
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
				// facts as markerClose does, or the owner sees "idle" and never
				// fetches the report.
				next = rt.CloseServedRound(ctx, rt, next)
			}
			next.Builder = clearProcess(next.Builder)
			next.StalledSince = time.Time{}
			return deliverAndSettle(ctx, rt, tx, next)
		}
	}

	// Exited without a report.
	now := rt.Now().UTC()
	h, _ := harness.Lookup(b.Builder.Kind)
	c := builderCandidateOf(rt, b)
	tail := currentBuilderTail(rt, b, availability.LimitScanLines)
	denialLine, isDenial := matchDenial(tail, denialPatterns(c, h))
	suffix := text.DenialSuffix(denialLine, isDenial)

	// Detect the oom kill before the exit entry, so the entry says it. A
	// requested stop still wins and is checked below. The kernel reports a
	// cgroup kill either as a process that left no trailer (unknown) or as the
	// supervisor's own 128+SIGKILL (137), so both are probed.
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

	// A stop requested for a round that then exited without a report: close
	// it without a report and without a switch, because a stop is not a
	// failure. Belt-and-braces only -- Stop on a headless round closes
	// synchronously and never sets the request -- for a process killed
	// between the request and the close.
	if !b.StopRequestedAt.IsZero() {
		next, err := closeStopped(ctx, rt, tx, b, "killed")
		if err != nil {
			return next, err
		}
		if next.Owner != "" && rt.CloseServedRound != nil {
			// Same as Stop and markerClose: a served round that closed here
			// must record its facts, or the owner sees "idle" and never
			// collects it.
			next = rt.CloseServedRound(ctx, rt, next)
		}
		return next, nil
	}

	// An oom kill is not the candidate's failure, so re-queue on the same
	// candidate with no switch and no exclusion.
	if oom {
		return housekeep.RequeueOOM(ctx, HousekeepDeps(rt), tx, b, oomPeak, now)
	}

	// A cgroup/group kill of the daemon (systemd restart, kill -9 of the
	// process tree) takes the supervisor with it before it can write the
	// relevo-exit: trailer, which reads exactly like a real builder death:
	// code unknown. The daemon can tell the two apart because it knows its
	// own start time and which processes it has seen alive: a
	// process that started before this daemon and that this daemon never
	// saw is the one this daemon's restart must have killed. Recovery keeps
	// the same candidate on the same round, uncounted, so a daemon restart
	// does not spend the round's switch budget. A lost local builder
	// first resumes its own harness session (claude --resume, agy
	// --conversation, opencode --session --fork), and falls back to a fresh
	// relaunch when the harness cannot resume (codex), announced no session,
	// or failed to spawn with the resume selector. Both carry the
	// interrupted note, stay uncounted and keep RoundStartedAt.
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

		// A candidate the configured set no longer holds cannot be resumed
		// or relaunched: pick again the way a switch does. Accepted cost:
		// switchBuilder restarts RoundStartedAt, unlike the same-candidate
		// relaunch below.
		if staleBuilder(rt, b) {
			return switchBuilder(ctx, rt, tx, b,
				"lost to a daemon restart; candidate "+b.BuilderCandidate+" is no longer configured", false, false)
		}

		text := roundPrompt(rt, tx, b, rt.Store.PromptPath(b.Name, b.Round), rt.Store.ReportPath(b.Name, b.Round), rt.Store.DonePath(b.Name, b.Round)) +
			"\n\n" + interruptedNote(rt.StartedAt)
		keep := b.RoundStartedAt
		// Read the round's session before anything clears it: the old
		// process announced it on its stream, and startProcess -- which the
		// fresh relaunch calls -- clears StreamSessionID, because a new
		// process begins a new session.
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
				// codex, and any kind relevo cannot resume: the fresh
				// relaunch is round 1's behaviour and needs no note.
				next, err = startRound(ctx, rt, tx, b, text, false)
			default:
				var sf spawnFailure
				if errors.As(err, &sf) {
					// The harness would not start with the resume
					// selector. One fresh attempt, then the halt below if
					// that fails too.
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
		// The round's budget clock survives the restart: the interruption
		// is relevo's, so it must not buy the round more
		// time than it had.
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
		// alone -- a pending notification for an already closed round, still owed
		// -- as the other revive paths leave it (bind.go).
		b = ClearHaltFields(b)
		slog.Info("headless builder relaunched after daemon restart", "binding", b.Name, "round", b.Round, "candidate", b.BuilderCandidate)
		return b, nil
	}

	// The escape halt comes before gateOnLimit: a limit line in the log of
	// an escaped round must not turn a halt into a switch.
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

	// A builder that ended its turn cleanly without a report has left a
	// session nothing will wake: resume it once with a nudge and hand the
	// round back, rather than spend a switch on work that is nearly done.
	// Only a clean end earns it: a turn the stream shows cut off inside
	// itself is a runner failure even at code 0, and a stream that says
	// nothing keeps today's path, which is nudgeResume's own code gate.
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
	// A provider-side outage behind this exit is a provider gate, not a
	// builder failure, so it is offered the timed gate rather than the
	// until-cleared exclusion below: one rate_limited entry whose note keeps
	// the cause, and the builder switched uncounted, so the candidate is
	// eligible again as soon as the provider recovers instead of staying
	// locked out of the round until a human clears it.
	//
	// It sits at the exclusion decision and nowhere earlier, so a stop, an
	// oom kill, a loss to a restart, an escape, a denial, a nudge, an
	// undelivered reader and a !switchable halt all keep their existing order
	// and outcomes -- and gateOnLimit above, which is the quota-shaped case,
	// still runs first. A tail that names no provider fault falls through
	// untouched, so a genuine crash keeps the exclusion below.
	//
	// No repeat-record guard is needed here, unlike the stalled-live scan's:
	// this tick is on the exited path, and the switch restarts RoundStartedAt
	// and the stream cursor, so the next decision point scans a different
	// process's own bytes.
	outaged, gated, gerr := gateOnOutage(ctx, rt, tx, b, limitText(ctx, rt, b))
	if gerr != nil {
		return outaged, gerr
	}
	if gated {
		return outaged, nil
	}
	// The exclusion is appended to the b that switchBuilder receives so the
	// replacement inherits it and the field is persisted with the switch:
	// a headless builder that exited without a report is excluded from the
	// pick for the rest of this round.
	b.RoundExcluded = appendUnique(b.RoundExcluded, b.BuilderCandidate)
	return switchBuilder(ctx, rt, tx, b, fmt.Sprintf("exited (code %s) %s", codeText, text.WithoutArtifact(b.Shape == store.ShapeReader)), false, true)
}
