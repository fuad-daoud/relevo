package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// sameSessionNotePrefix marks the switch entry a same-session resume writes,
// followed by the session id, so the once-per-session cap can count it.
const sameSessionNotePrefix = "resumed same session"

// transportPromptFormat is sent into a session whose last turn a connection
// error cut off.
const transportPromptFormat = `Your previous turn was cut off by a connection error. Continue from the tree as it stands and do not redo finished work. ` + backgroundDiedSentence + ` Finish in the foreground: write the report to %s, then create %s.`

// transportReaderNote is the extra sentence a reader's continuation gains.
const transportReaderNote = "Your previous turn was cut off by a connection error."

// sameSessionResumes counts this round's same-session resume entries naming sess.
func sameSessionResumes(entries []store.LogEntry, round int, sess string) int {
	prefix := sameSessionNotePrefix + " " + sess + " "
	var n int
	for _, e := range entries {
		if e.Round == round && e.Kind == store.KindSwitch && strings.HasPrefix(e.Note, prefix) {
			n++
		}
	}
	return n
}

// transportPrompt renders the message for a session a transport error cut off.
func transportPrompt(rt Runtime, b store.Binding) string {
	done := rt.Store.DonePath(b.Name, b.Round)
	if b.Shape == store.ShapeReader {
		return fmt.Sprintf(readerContinuationPromptFormat, done) + " " + transportReaderNote
	}
	return fmt.Sprintf(transportPromptFormat, rt.Store.ReportPath(b.Name, b.Round), done) + opencodeTimeoutNote
}

// resumeSameSession resumes b's session once, on the same candidate and
// uncounted against max_switches. It refuses, returning b untouched, when no
// session is known, when this session has had its one resume, or when the
// resume cannot start; the caller's exit path then runs as it always did.
func resumeSameSession(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, cause, prompt string, now time.Time) (store.Binding, bool, error) {
	sess := b.Builder.StreamSessionID
	if sess == "" || sameSessionResumes(entries, b.Round, sess) >= 1 {
		return b, false, nil
	}
	keep := b.RoundStartedAt
	prior := recordUsage(ctx, rt, roundSource(rt, b, b.RoundStartedAt, now))
	next, err := resumeRound(ctx, rt, tx, b, sess, prompt, false)
	if err != nil {
		slog.Warn("same-session resume failed; falling through to the exit path",
			"binding", b.Name, "round", b.Round, "session", sess, "err", err)
		return b, false, nil
	}
	note := fmt.Sprintf("%s %s of %s after %s: same candidate, not counted",
		sameSessionNotePrefix, sess, b.BuilderCandidate, cause)
	return settleResumed(tx, b, next, keep, prior, note, now)
}
