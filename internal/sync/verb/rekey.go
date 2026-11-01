package verb

import (
	"context"
	"crypto/rand"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// rekey rotates the log's key to the next epoch. Passphrases arrive in the raw
// tail behind their own lengths and split once here; neither is ever formatted
// into a result. The tail is the one frame part a captured stream never shows, so
// two secrets on it are two secrets no log leaks.
func (v *Runner) rekey(ctx context.Context, verb *wire.SyncVerb, tail []byte) *wire.SyncResult {
	current, newPass, err := RekeyIntake(verb, tail)
	if err != nil {
		return Refusal(wire.SyncCodeInvalid, err)
	}
	transport, err := v.drive(ctx)
	if err != nil {
		return Refusal(Classify(err), err)
	}
	if err := relevosync.Rekey(ctx, v.Local, transport, current, newPass, rand.Reader); err != nil {
		return Refusal(Classify(err), err)
	}
	return &wire.SyncResult{OK: true}
}
