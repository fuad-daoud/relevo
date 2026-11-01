package verb

import (
	"context"
	"crypto/rand"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// unlock derives the sync key from the passphrase in the frame's tail. The
// passphrase is never formatted into a result.
func (v *Runner) unlock(ctx context.Context, verb *wire.SyncVerb, passphrase []byte) *wire.SyncResult {
	transport, err := v.drive(ctx)
	if err != nil {
		return Refusal(Classify(err), err)
	}
	created, err := relevosync.Unlock(ctx, v.Local, transport, passphrase, verb.New, rand.Reader)
	if err != nil {
		return Refusal(Classify(err), err)
	}
	// The key just stored is what lets this machine vouch for the tail it appended
	// before it learned of the rekey; the extension goes out before the parked
	// docs drain, so the fence this machine believes in is on the remote while
	// it unblocks itself.
	relevosync.PublishOwnCut(ctx, v.Shared, v.Local, transport)
	if _, err := relevosync.DrainParkedDocs(v.Shared, v.Local, transport); err != nil {
		return Refusal(Classify(err), err)
	}
	// Secrets stored keyless were never signalled, so without this they would never
	// travel. It follows the drain, so a newer parked peer value is not overwritten
	// by this machine's older one.
	if err := v.Local.SignalDoc(relevosync.SecretsDocName); err != nil {
		return Refusal(Classify(err), err)
	}
	// A join run while locked still owes its own history, sendable only with the
	// now-held key.
	owed := &relevosync.Enabler{Request: relevosync.EnableRequest{Local: v.Local, Shared: v.Shared}}
	if _, err := owed.FinishJoin(transport); err != nil {
		return Refusal(Classify(err), err)
	}
	return &wire.SyncResult{OK: true, Created: created}
}
