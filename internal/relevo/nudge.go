package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// nudgeNotePrefix marks the switch entry a nudge writes, so the once-per-plan
// check can tell it apart from the restart resume's own "resumed session ..."
// note: the two share a kind and a shape, and only this prefix says the plan
// has already had its one nudge.
const nudgeNotePrefix = "nudged builder"

// nudgePromptFormat is the message sent into the session relevo resumes
// because its builder ended its turn without a report. The two paths are
// spelled out because nothing will wake the process otherwise: finishing in
// the foreground is the whole point.
const nudgePromptFormat = `You ended your turn before writing the report, and nothing will wake you: this process exits when your turn ends. Finish now, in the foreground: run any pending check to completion and wait for it, write the report to %s, then create %s. Do not start background tasks and do not end your turn before both files exist.`

// readerContinuationPromptFormat is the continuation prompt for a reader:
// the reader's final message is the deliverable itself, so it is told to
// continue and complete its deliverable in its final message ending with the
// relevo block, followed by the marker.
const readerContinuationPromptFormat = `you stopped before your deliverable; continue, and make your final message the complete deliverable. The final message must end with the relevo block, then create %s.`

// readerFenceNote is the sentence a reader's continuation gains when the round
// carries a bare `relevo` line whose block names no readable value: the backtick
// fences were lost, and the only thing that can put them back is the reader. It
// is a fixed string, so the same loss is worded the same way every round.
const readerFenceNote = "Your block's opening and closing ``` fences are missing, so none of its values could be read: put ``` on its own line above the block and again below it."

// readerNudgeLimit is how many continuations a reader is granted before halting.
const readerNudgeLimit = 2

// writerNudgeLimit is how many nudges a writer is granted before switching.
const writerNudgeLimit = 1

// nudgeLimit returns the continuation/nudge limit for b's shape.
func nudgeLimit(shape string) int {
	if shape == store.ShapeReader {
		return readerNudgeLimit
	}
	return writerNudgeLimit
}

// nudgePrompt renders nudgePromptFormat for one round's report and done paths.
func nudgePrompt(reportPath, donePath string) string {
	return fmt.Sprintf(nudgePromptFormat, reportPath, donePath)
}

// nudgePromptFor renders the nudge prompt for b: a writer is told its round's
// report and done paths, a reader is told to continue to its deliverable. A
// reader whose block lost its fences is told so, because the generic wording
// would send it looking for a missing block it in fact wrote.
func nudgePromptFor(rt Runtime, b store.Binding) string {
	done := rt.Store.DonePath(b.Name, b.Round)
	if b.Shape != store.ShapeReader {
		return nudgePrompt(rt.Store.ReportPath(b.Name, b.Round), done)
	}
	prompt := fmt.Sprintf(readerContinuationPromptFormat, done)
	if readerLostFences(rt, b) {
		return prompt + " " + readerFenceNote
	}
	return prompt
}

// nudgesSincePlan returns the count of nudge switch entries that follow the
// latest prompt sent for round: a prompt is nudged up to its shape limit, and a
// resend of the same round (a newer prompt entry) resets the count. A round
// with no prompt entry in entries has had none.
func nudgesSincePlan(entries []store.LogEntry, round int) int {
	plan := -1
	for i, e := range entries {
		if e.Round == round && e.Direction == store.DirToBuilder && store.IsPromptKind(e.Kind) {
			plan = i
		}
	}
	if plan < 0 {
		return 0
	}
	var count int
	for _, e := range entries[plan+1:] {
		if e.Round == round && e.Kind == store.KindSwitch && strings.HasPrefix(e.Note, nudgeNotePrefix) {
			count++
		}
	}
	return count
}

// nudgeResume resumes the session of a builder that ended its turn with code
// 0 without writing either the report or the done marker: the process it left
// behind is gone and nothing will wake it, so relevo sends one fixed nudge
// into the same session and hands the round back to it. It is once per plan --
// a resend of the round allows another -- and is not counted against
// max_switches, because the builder did not fail, its turn simply ended.
//
// The caller's existing exit path is the fallback: a missing precondition, or
// a resume that cannot start, returns the binding untouched and lets the
// caller switch (or halt) exactly as it did before. relevo does not attempt a
// fresh relaunch from here, because that is what the caller's switch already
// does.
func nudgeResume(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, codeText string, now time.Time) (store.Binding, bool, error) {
	if codeText != "0" || b.Builder.StreamSessionID == "" {
		return b, false, nil
	}
	if nudgesSincePlan(entries, b.Round) >= nudgeLimit(b.Shape) {
		return b, false, nil
	}
	sess := b.Builder.StreamSessionID
	keep := b.RoundStartedAt
	prior := peekUsage(ctx, rt, b, now)
	prompt := nudgePromptFor(rt, b)
	next, err := resumeRound(ctx, rt, tx, b, sess, prompt, false)
	if err != nil {
		slog.Warn("nudge resume failed; falling through to the exit path",
			"binding", b.Name, "round", b.Round, "session", sess, "err", err)
		return b, false, nil
	}
	next.RoundStartedAt = keep
	next.State = store.StateActive
	// The round is running again, so a halt a previous attempt in it left goes
	// with it, every field: the notification key it still carries dedups the
	// next halt of this same round, so leaving it behind swallows it silently.
	// OwedHalt is left alone -- a pending notification for an already closed
	// round, still owed -- as the other revive paths leave it (bind.go).
	next = clearHaltFields(next)
	if err := tx.AppendLog(b.Name, store.LogEntry{
		TS: now, Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindSwitch, Confirmed: true,
		Usage: prior,
		Note: fmt.Sprintf("%s (ended its turn %s): resumed session %s of %s: same candidate, not counted",
			nudgeNotePrefix, withoutArtifact(b.Shape), sess, b.BuilderCandidate),
	}); err != nil {
		return next, true, err
	}
	return next, true, nil
}
