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

func exitScopeAndCode(ctx context.Context, rt Runtime, b store.Binding) string {
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
	return codeText
}

func exitLateMarker(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, wantVerify bool) (store.Binding, bool, error) {
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
			return b, true, err
		}
		return next, true, nil
	}
	return b, false, nil
}

func exitUnmarkedReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, codeText string) (store.Binding, bool, error) {
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
				return b, true, err
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
				return b, true, err
			}
			if next.Owner != "" && rt.CloseServedRound != nil {
				// An unmarked close is still a close: record the served round's
				// facts as markerClose does, or the owner sees "idle" and
				// never fetches the report.
				next = rt.CloseServedRound(ctx, rt, next)
			}
			next.Builder = clearProcess(next.Builder)
			next.StalledSince = time.Time{}
			delivered, derr := deliverAndSettle(ctx, rt, tx, next)
			return delivered, true, derr
		}
	}
	return b, false, nil
}

func exitSignalsAndEntry(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, codeText string) (oom bool, oomPeak int64, tail, denialLine string, isDenial bool, now time.Time, err error) {
	now = rt.Now().UTC()
	h, _ := harness.Lookup(b.Builder.Kind)
	c := builderCandidateOf(rt, b)
	tail = currentBuilderTail(rt, b, availability.LimitScanLines)
	denialLine, isDenial = matchDenial(tail, denialPatterns(c, h))
	suffix := text.DenialSuffix(denialLine, isDenial)

	// Detect the oom kill before the exit entry, so the entry says it; a
	// requested stop still wins and is checked below. A cgroup kill reads as
	// no trailer (unknown) or the supervisor's own 128+SIGKILL (137), so
	// both are probed.
	if codeText == "unknown" || codeText == "137" {
		if peak, killed := housekeep.OOMKilled(ctx, HousekeepDeps(rt), b); killed {
			oom, oomPeak = true, peak
			suffix += "; " + housekeep.OOMWords(peak)
		}
	}

	if err = tx.AppendLog(b.Name, exitEntry(now, b.Round, b.Builder.LogPath, codeText, suffix, builderTail(rt, b, logTailLines), b.Shape)); err != nil {
		return oom, oomPeak, tail, denialLine, isDenial, now, err
	}
	slog.Info("headless builder exited without a report", "binding", b.Name, "round", b.Round, "pid", b.Builder.PID, "code", codeText)
	return oom, oomPeak, tail, denialLine, isDenial, now, nil
}

func exitCloseStopped(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, bool, error) {
	// A stop requested for a round that then exited without a report closes
	// without a report and without a switch, because a stop is not a
	// failure. Belt-and-braces only, for a process killed between the request
	// and the close.
	if b.StopRequestedAt.IsZero() {
		return b, false, nil
	}
	next, err := closeStopped(ctx, rt, tx, b, "killed")
	if err != nil {
		return next, true, err
	}
	if next.Owner != "" && rt.CloseServedRound != nil {
		// A served round that closed here must record its facts, or the
		// owner sees "idle" and never collects it.
		next = rt.CloseServedRound(ctx, rt, next)
	}
	return next, true, nil
}

func exitRequeueOOM(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, oom bool, oomPeak int64, now time.Time) (store.Binding, bool, error) {
	// An oom kill is not the candidate's failure, so re-queue on the same
	// candidate with no switch and no exclusion.
	if !oom {
		return b, false, nil
	}
	next, err := housekeep.RequeueOOM(ctx, HousekeepDeps(rt), tx, b, oomPeak, now)
	return next, true, err
}

func exitLostHead(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, now time.Time) (store.Binding, bool, error) {
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
			return b, true, err
		}
		slog.Info("headless builder re-queued after daemon restart", "binding", b.Name, "round", b.Round)
		return b, true, nil
	}

	// A candidate the configured set no longer holds cannot be resumed or
	// relaunched: pick again the way a switch does, at the accepted cost
	// that switchBuilder restarts RoundStartedAt.
	if staleBuilder(rt, b) {
		next, err := switchBuilder(ctx, rt, tx, b,
			"lost to a daemon restart; candidate "+b.BuilderCandidate+" is no longer configured", false, false)
		return next, true, err
	}
	return b, false, nil
}

func exitLostResume(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, now time.Time) (store.Binding, error) {
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

func exitHaltGate(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, codeText, denialLine string, isDenial bool) (store.Binding, bool, error) {
	// The escape halt comes before gateOnLimit: a limit line in an escaped
	// round's log must not turn a halt into a switch.
	if escapeCheck(ctx, rt, b, false) == git.EscapeHalt {
		next, err := haltAndSettle(ctx, rt, tx, b, escapeDiagnosis(b, codeText))
		return next, true, err
	}

	scanText, scanResetMS := limitScan(ctx, rt, b)
	next, _, handled, err := gateOnLimit(ctx, rt, tx, b, scanText, scanResetMS, false)
	if handled {
		return next, true, err
	}

	if isDenial {
		next, err := haltAndSettle(ctx, rt, tx, b, fmt.Sprintf(
			"%s: builder exited (code %s) %s after a permission denial (%q); not switched -- re-send with a higher tier (relevo send --name %s --file <plan> --tier edit|yolo [--allow-yolo]) or extend the harness's allow list; log: %s",
			b.Name, codeText, text.WithoutArtifact(b.Shape == store.ShapeReader), denialLine, b.Name, text.ShowCommand(b.Name, b.Round, "log")))
		return next, true, err
	}
	return b, false, nil
}

func exitTailSwitch(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, codeText string, now time.Time, switchable bool) (store.Binding, error) {
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
