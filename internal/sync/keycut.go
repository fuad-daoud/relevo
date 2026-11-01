package sync

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The own cut extension: how a machine vouches for the tail it appended before
// it learned of a rekey. The head it vouches to is the own-head fact, written
// after every confirmed append; the vouch is forward only and the log's own
// head bounds it from above.

// PublishOwnCut extends this origin's cut to the vouch target recorded at
// unlock from stale. It publishes at most up to that target, never the live own
// head, and only while this origin's record is below it; once the record
// reaches the target, the fact is cleared.
//
// A machine never stale, or a fleet at epoch 1, records no target and publishes
// nothing. The steady-path call stays as the retry of a vouch whose publish
// failed.
func PublishOwnCut(ctx context.Context, shared *db.DB, kv db.KV, t synclog.LogTransport) {
	if shared == nil || t == nil || kv == nil {
		return
	}
	target, ok, err := vouchTarget(kv)
	if err != nil {
		slog.Warn("sync: read the vouch target for the own cut", "err", err)
		return
	}
	if !ok || target <= 0 {
		return
	}
	ring, ok, err := heldKeyring(kv)
	if err != nil {
		slog.Warn("sync: read the keyring for the own cut", "err", err)
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
	origin := shared.Origin()
	record, recorded := records[origin]
	if recorded && (ring.Epoch < record.Epoch || (ring.Epoch == record.Epoch && record.Seq >= target)) {
		if err := kv.KVDelete(SyncVouchTarget); err != nil {
			slog.Warn("sync: clear the vouch target", "err", err)
		}
		return
	}
	cut := synclog.EpochCut{
		Epoch: ring.Epoch,
		Seq:   target,
		Tag:   synclog.EpochCutTag(ring.Keys[ring.Epoch], origin, ring.Epoch, target),
	}
	if err := t.PublishEpochCut(ctx, cut); err != nil {
		slog.Warn("sync: publish the own cut extension", "err", err)
		return
	}
	if err := shared.RaiseEpochCut(origin, cut.Epoch, cut.Seq); err != nil {
		slog.Warn("sync: record the own cut extension", "err", err)
		return
	}
	if err := kv.KVDelete(SyncVouchTarget); err != nil {
		slog.Warn("sync: clear the vouch target", "err", err)
	}
}

// vouchTarget reads the sequence up to which this origin vouches for its
// pre-unlock tail, recorded at unlock from stale.
func vouchTarget(kv db.KV) (int, bool, error) {
	value, ok, err := kv.KVGet(SyncVouchTarget)
	if err != nil || !ok {
		return 0, false, err
	}
	target, err := strconv.Atoi(string(bytes.TrimSpace(value)))
	if err != nil || target < 1 {
		return 0, false, nil
	}
	return target, true, nil
}

// BindVerifier sets an importer's verification to the machine's stored keyring,
// newest epoch first. Every verification site binds with it, and a machine set
// through the old one-key path verifies exactly as it did, because its ring is
// one epoch deep.
func BindVerifier(imp *synclog.Importer, kv db.KV) error {
	ring, ok, err := heldKeyring(kv)
	if err != nil {
		return err
	}
	if ok {
		imp.SetKeyring(synclog.Keyring{Epoch: ring.Epoch, Keys: ring.Keys})
	}
	return nil
}
