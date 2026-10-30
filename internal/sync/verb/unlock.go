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
	// The key this unlock stored is what lets the machine vouch for the tail
	// it appended before it learned of the rekey; the extension goes out
	// before the parked docs drain, so the fence this machine believes in is
	// on the remote while this machine unblocks itself.
	relevosync.PublishOwnCut(ctx, v.Shared, v.Local, transport)
	if _, err := relevosync.DrainParkedDocs(v.Shared, v.Local, transport); err != nil {
		return Refusal(Classify(err), err)
	}
	// Secrets stored before this machine held a key were never signalled, and an
	// export only reads a signalled doc, so without this they would never
	// travel. It follows the drain: a peer's newer value parked while locked is
	// applied first, so the export does not send this machine's older one over
	// it under a fresh stamp.
	if err := v.Local.SignalDoc(relevosync.SecretsDocName); err != nil {
		return Refusal(Classify(err), err)
	}
	// A join that ran while this machine was locked still owes its own history,
	// and the key it now holds is what lets that history be sent.
	owed := &relevosync.Enabler{Request: relevosync.EnableRequest{Local: v.Local, Shared: v.Shared}}
	if _, err := owed.FinishJoin(transport); err != nil {
		return Refusal(Classify(err), err)
	}
	return &wire.SyncResult{OK: true, Created: created}
}
