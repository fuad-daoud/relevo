package verb

import (
	"context"
	"crypto/rand"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// rekey rotates the log's key from the epoch the remote holds to the next one.
//
// The two passphrases arrive in the frame's raw tail behind their own lengths
// and are split once, here, from the end -- the same rule the enable's three
// secrets follow. Neither is ever formatted into a result: the refusals below
// are the sync package's own sentences, written in this repository, and a wrong
// passphrase reaches the caller as a code plus the sentence that names the fix.
//
// The tail is the one part of a frame a reader of a captured stream never sees,
// so two secrets on it are two secrets a log cannot leak.
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
