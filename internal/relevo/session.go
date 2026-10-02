package relevo

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// builderSessionOf names the harness session that built the binding's closed
// round (#147), for the report entry: a headless builder's stream id when the
// round's stream announced one. Remote builders and any binding with no
// headless session answer nil -- never guessed.
func builderSessionOf(b store.Binding) *store.BuilderSession {
	if !b.Builder.Headless() || b.Builder.StreamSessionID == "" {
		return nil
	}
	return &store.BuilderSession{Kind: b.Builder.Kind, ID: b.Builder.StreamSessionID}
}

// roundSession names the harness session that built a closed round (#147
// part 2), read off the round's own report entry: the newest KindReport entry
// for that round carrying a BuilderSession. ok is false when the round has no
// report entry at all, or when its report names no session -- relevo never
// guesses, and a resumed session must be the one that built the round.
func roundSession(entries []store.LogEntry, round int) (*store.BuilderSession, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Round == round && e.Kind == store.KindReport && e.BuilderSession != nil {
			return e.BuilderSession, true
		}
	}
	return nil, false
}
