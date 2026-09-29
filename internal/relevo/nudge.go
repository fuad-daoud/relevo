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

// readerNudgePromptFormat is nudgePromptFormat for a reader: it names the
// actor's own output label and the file that label resolves to, because a
// reader writes no report.
const readerNudgePromptFormat = `You ended your turn before writing your %[1]s, and nothing will wake you: this process exits when your turn ends. Finish now, in the foreground: run any pending check to completion and wait for it, write your %[1]s to %[2]s, then create %[3]s. Do not start background tasks and do not end your turn before both files exist.`

// nudgePrompt renders nudgePromptFormat for one round's report and done paths.
func nudgePrompt(reportPath, donePath string) string {
	return fmt.Sprintf(nudgePromptFormat, reportPath, donePath)
}

// nudgePromptFor renders the nudge for b: a writer is told its round's report
// and done paths, a reader its output label and the file that label resolves
// to.
func nudgePromptFor(rt Runtime, b store.Binding) string {
	done := rt.Store.DonePath(b.Name, b.Round)
	if b.Shape != store.ShapeReader {
		return nudgePrompt(rt.Store.ReportPath(b.Name, b.Round), done)
	}
	label := readerOutputLabel(rt, b)
	return fmt.Sprintf(readerNudgePromptFormat, label, rt.Store.OutputPath(b.Name, b.Round, bindingRole(b), label), done)
}

// nudgedSincePlan reports whether a nudge switch entry already follows the
// latest prompt sent for round: a prompt is nudged once, and a resend of the
// same round (a newer prompt entry) allows another. A round with no prompt
// entry in entries has not had one.
func nudgedSincePlan(entries []store.LogEntry, round int) bool {
	plan := -1
	for i, e := range entries {
		if e.Round == round && e.Direction == store.DirToBuilder && store.IsPromptKind(e.Kind) {
			plan = i
		}
	}
	if plan < 0 {
		return false
	}
	for _, e := range entries[plan+1:] {
		if e.Round == round && e.Kind == store.KindSwitch && strings.HasPrefix(e.Note, nudgeNotePrefix) {
			return true
		}
	}
	return false
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
	if nudgedSincePlan(entries, b.Round) {
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
