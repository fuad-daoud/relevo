package sync

import (
	"context"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The own cut extension: how a machine vouches for the tail it appended before
// it learned of a rekey. The head it vouches to is the own-head fact, written
// after every append its transport confirmed; the vouch is forward only and
// the log's own head bounds it from above.

// PublishOwnCut extends this origin's cut to sync.own_head.v1, when that head
// is above the cut this machine's record holds.
//
// It runs on the unlock -- the act that stores the new keyring and with it the
// right to tag under the newest key -- and on every steady attempt whose own
// head has grown past the fence, which is what keeps a machine unlocked at an
// old cut from re-fencing its own fresh appends every tick. The record it
// reads is the machine-local one the import raised from every verified
// remote row, so the extension only ever raises, and a failed write leaves
// the fence where it stood: the next attempt repeats the vouch.
func PublishOwnCut(ctx context.Context, shared *db.DB, kv db.KV, t synclog.LogTransport) {
	if shared == nil || t == nil {
		return
	}
	origin := shared.Origin()
	ring, ok, err := heldKeyring(kv)
	if err != nil {
		slog.Warn("sync: read the keyring for the own cut", "err", err)
		return
	}
	if !ok {
		return
	}
	head, ok, err := synclog.OwnHead(shared.LocalOrSelf())
	if err != nil {
		slog.Warn("sync: read the own-head fact for the own cut", "err", err)
		return
	}
	if !ok {
		return
	}
	records, err := shared.EpochCuts()
	if err != nil {
		slog.Warn("sync: read the epoch cuts for the own cut", "err", err)
		return
	}
	record, recorded := records[origin]
	if recorded && (ring.Epoch < record.Epoch || (ring.Epoch == record.Epoch && head <= record.Seq)) {
		return
	}
	cut := synclog.EpochCut{
		Epoch: ring.Epoch,
		Seq:   head,
		Tag:   synclog.EpochCutTag(ring.Keys[ring.Epoch], origin, ring.Epoch, head),
	}
	if err := t.PublishEpochCut(ctx, cut); err != nil {
		slog.Warn("sync: publish the own cut extension", "err", err)
		return
	}
	if err := shared.RaiseEpochCut(origin, cut.Epoch, cut.Seq); err != nil {
		slog.Warn("sync: record the own cut extension", "err", err)
	}
}
