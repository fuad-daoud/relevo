package relevo

import (
	"context"
	"crypto/rand"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// unlock derives the sync key from the passphrase in the frame's tail. The
// passphrase is never formatted into a result.
func (v *VerbRunner) unlock(ctx context.Context, verb *wire.SyncVerb, passphrase []byte) *wire.SyncResult {
	transport, err := v.drive(ctx)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	created, err := relevosync.Unlock(ctx, v.Local, transport, passphrase, verb.New, rand.Reader)
	if err != nil {
		return verbRefusal(verbClassify(err), err)
	}
	return &wire.SyncResult{OK: true, Created: created}
}
