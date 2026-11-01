package relevo

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// builderSessionOf names the harness session that built the binding's closed
// round, for the report entry: a headless builder's stream id when the
// round's stream announced one. Remote builders and any binding with no
// headless session answer nil -- never guessed.
func builderSessionOf(b store.Binding) *store.BuilderSession {
	if !b.Builder.Headless() || b.Builder.StreamSessionID == "" {
		return nil
	}
	return &store.BuilderSession{Kind: b.Builder.Kind, ID: b.Builder.StreamSessionID}
}
