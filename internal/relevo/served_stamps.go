package relevo

import (
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// admitNotePrefix opens the note Admit writes when a queued round's builder
// actually starts. A round the server hands straight to a builder writes no
// such entry, so its prompt handoff is its start.
const admitNotePrefix = "started after "

// serveQueueNotePrefix opens the note the server writes when it accepts a round
// into its builder queue, so a round that waited carries both ends of its wait.
const serveQueueNotePrefix = "queued ("

// servedRoundStamps is round n's queue wait on the server: when the round was
// accepted into the queue, and when its builder began.
//
// Both come from the round's own log entries rather than from the binding,
// because the binding's stamps are cleared and overwritten as later rounds run:
// only the log keeps what each round of this member waited and when it began. A
// round the server never queued -- handed straight to a builder -- has no accept
// entry, so it reports no wait and its handoff is its start.
func servedRoundStamps(entries []store.LogEntry, n int) (start, queued time.Time) {
	if n <= 0 {
		return time.Time{}, time.Time{}
	}
	var handoff time.Time
	for _, e := range entries {
		if e.Round != n {
			continue
		}
		if e.Direction == store.DirToBuilder && store.IsPromptKind(e.Kind) {
			if handoff.IsZero() || e.TS.Before(handoff) {
				handoff = e.TS
			}
			continue
		}
		if e.Kind == store.KindQueue && strings.HasPrefix(e.Note, serveQueueNotePrefix) {
			if queued.IsZero() || e.TS.Before(queued) {
				queued = e.TS
			}
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Round == n && e.Kind == store.KindQueue && strings.HasPrefix(e.Note, admitNotePrefix) {
			return e.TS, queued
		}
	}
	return handoff, queued
}
