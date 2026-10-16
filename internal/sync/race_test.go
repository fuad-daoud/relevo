package sync

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The cut-extension race: an origin that appended under the old key before it
// learned of the rekey is held by every reader until it unlocks, its unlock
// vouches only the tail its own transport confirmed, and a forgery above that
// head stays refused.

var racePassphrase = []byte("the rekey's new passphrase")

const (
	// raceKey1 is raw bytes, because the epoch-1 keys this test's machines
	// hold are the round-1 fixtures' shape: what the local secret table
	// stores and the exporter signs with.
	raceKey1 = "0123456789abcdef0123456789abcdef"
)

func TestUnlockVouchesItsOwnTailAndTheFleetAppliesIt(t *testing.T) {
	t.Parallel()
	key1 := []byte(raceKey1)
	aShared, aLocal := joinPair(t, "m1")
	seedJoin(t, aShared, joinBinding("a1", "m1"))
	bShared, bLocal := joinPair(t, "m2")
	seedJoin(t, bShared, joinBinding("b1", "m2"))

	// One log, two machines, both unlocked at epoch 1 and talking.
	log := synclog.NewMemTransport("m1")
	m2Log := log.OnLog("m2")
	if err := MarkEnabled(aLocal, true, time.Unix(0, 0)); err != nil {
		t.Fatalf("MarkEnabled m1: %v", err)
	}
	if err := MarkEnabled(bLocal, true, time.Unix(0, 0)); err != nil {
		t.Fatalf("MarkEnabled m2: %v", err)
	}
	if err := SetKey(aLocal, key1, time.Now()); err != nil {
		t.Fatalf("SetKey m1: %v", err)
	}
	if err := SetKey(bLocal, key1, time.Now()); err != nil {
		t.Fatalf("SetKey m2: %v", err)
	}
	if res := (&Runner{Client: log, Local: aLocal, Timeout: time.Second}).SyncOnce(context.Background(), aShared); res.Err != nil {
		t.Fatalf("m1's first attempt: %v", res.Err)
	}
	if res := (&Runner{Client: m2Log, Local: bLocal, Timeout: time.Second}).SyncOnce(context.Background(), bShared); res.Err != nil {
		t.Fatalf("m2's first attempt: %v", res.Err)
	}

	// m2 rekeys the shared log to epoch 2 with real material, so a later
	// unlock derives the same key and passes the remote's check.
	salt := makeKey2Salt(t)
	key2 := DeriveKey(racePassphrase, salt, DefaultKDF)
	wrapped, err := WrapKey(key2, 1, key1)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	rekeyLogTo2(t, m2Log, key1, key2, wrapped, salt, seedHeads(t, m2Log))

	// m1 appends under the OLD key, entries its own transport confirmed
	// appending, before it reads the remote again: the extension's own-head
	// fact is written by this export path itself, which is what makes the
	// later vouch honest.
	seedJoin(t, aShared, joinBinding("a2", "m1"))
	staleOutboxExport(t, aShared, log, key1)
	aHead, ok, err := synclog.OwnHead(aShared.LocalOrSelf())
	if err != nil || !ok {
		t.Fatalf("own head = (%d, %v, %v)", aHead, ok, err)
	}

	// The next attempt finds the epoch moved and stops both halves.
	res := (&Runner{Client: log, Local: aLocal, Timeout: time.Second}).SyncOnce(context.Background(), aShared)
	if res.Err != nil || res.Exported != 0 || res.Applied != 0 {
		t.Fatalf("stale attempt = %+v, %v", res, res.Err)
	}

	// The fleet refuses m1's stale tail: m2's import holds m1 above the cut.
	if cRes := (&Runner{Client: m2Log, Local: bLocal, Timeout: time.Second}).SyncOnce(context.Background(), bShared); cRes.Err != nil {
		t.Fatalf("m2's attempt: %v", cRes.Err)
	}
	if mark := helperMarkOf(t, bShared, "m1"); mark >= aHead {
		t.Fatalf("m2's mark on m1 = %d, want it to rest under %d", mark, aHead)
	}

	// m1 unlocks with the new passphrase, stores the whole keyring, and the
	// unlock's extension vouches its own confirmed tail.
	if _, err := Unlock(context.Background(), aLocal, log, racePassphrase, false, rand.Reader); err != nil {
		t.Fatalf("m1's unlock: %v", err)
	}
	PublishOwnCut(context.Background(), aShared, aLocal, log)

	// The vouch is on the remote, tagged under the new key, at the head this
	// machine itself confirmed.
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if cut := state.EpochCuts["m1"]; cut.Seq != aHead || cut.Epoch != 2 {
		t.Fatalf("remote cut for m1 = %+v, want epoch 2 at the head it confirmed (%d)", cut, aHead)
	}
	if !synclog.VerifyEpochCut(key2, "m1", 2, aHead, state.EpochCuts["m1"].Tag) {
		t.Fatal("the extension's tag does not verify under the new key")
	}

	// The fleet applies the vouched tail: m2's unlock gives it the whole
	// ring, its import raises its cut for m1 and verifies the old-key
	// entries by trial below it.
	if _, err := Unlock(context.Background(), bLocal, m2Log, racePassphrase, false, rand.Reader); err != nil {
		t.Fatalf("m2's unlock: %v", err)
	}
	ring, ok, err := ReadKeyring(bLocal)
	if err != nil || !ok {
		t.Fatalf("m2's keyring ok=%v err=%v", ok, err)
	}
	imp := synclog.NewImporter(bShared, m2Log)
	imp.SetKeyring(synclog.Keyring{Epoch: ring.Epoch, Keys: ring.Keys})
	if _, err := imp.Import(); err != nil {
		t.Fatalf("m2's import after the vouch: %v", err)
	}
	if mark := helperMarkOf(t, bShared, "m1"); mark != aHead {
		t.Fatalf("m2's mark on m1 = %d, want %d: the vouched tail applied", mark, aHead)
	}

	// And the vouch's bound holds: an entry above m1's own head, MAC'd under
	// the old key a token holder would hold, stays refused and moves no mark.
	forgeOldKey(t, m2Log, "m1", key1)
	imp2 := synclog.NewImporter(bShared, m2Log)
	imp2.SetKeyring(synclog.Keyring{Epoch: ring.Epoch, Keys: ring.Keys})
	if _, err := imp2.Import(); err != nil {
		t.Fatalf("the forged import: %v", err)
	}
	if mark := helperMarkOf(t, bShared, "m1"); mark != aHead {
		t.Fatalf("the forged import moved m2's mark to %d, want it at %d", mark, aHead)
	}
}

// makeKey2Salt is the fresh salt a rekey rolls.
func makeKey2Salt(t *testing.T) []byte {
	t.Helper()
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		t.Fatalf("salt: %v", err)
	}
	return salt
}

// rekeyLogTo2 rekeys log to epoch 2, one cut per logged origin at the heads
// given, the new key's material on the log and the old key sealed for holders
// of it.
func rekeyLogTo2(t *testing.T, log *synclog.MemTransport, key1, key2, wrapped, salt []byte, heads map[string]int) {
	t.Helper()
	cuts := make(map[string]synclog.EpochCut, len(heads))
	for origin, head := range heads {
		cuts[origin] = synclog.EpochCut{Epoch: 2, Seq: head, Tag: synclog.EpochCutTag(key2, origin, 2, head)}
	}
	if _, err := log.KeyRekey(context.Background(), synclog.KeyRekeySpec{
		From: 1,
		Key: synclog.KeyMeta{
			Salt: salt, KDF: DefaultKDF.String(), Check: KeyCheck(key2), Present: true, Epoch: 2,
			Wrapped: map[int][]byte{1: wrapped},
		},
		Cuts: cuts,
	}); err != nil {
		t.Fatalf("KeyRekey: %v", err)
	}
}

// seedHeads is every logged origin's head, which is what a rekey's cuts sit
// at. The rekey reads them itself and refuses stale claims, so the test asks
// the log for what it holds rather than hard-coding the numbers.
func seedHeads(t *testing.T, log *synclog.MemTransport) map[string]int {
	t.Helper()
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	return state.Heads
}

// staleOutboxExport is m1 appending its pending rows under the old key, the
// append an attempt that had not yet noticed the rekey would perform. It is
// the export path itself, so the own-head fact it writes is the fact the
// extension vouches for.
func staleOutboxExport(t *testing.T, d *db.DB, log *synclog.MemTransport, key []byte) {
	t.Helper()
	e := synclog.NewExporter(d, log)
	e.SetKey(key)
	if _, err := e.Export(); err != nil {
		t.Fatalf("the stale export: %v", err)
	}
}

// forgeOldKey appends one more entry for origin, MAC'd under the old key,
// above its own confirmed head -- the forgery a token holder with the leaked
// old key would write.
func forgeOldKey(t *testing.T, log *synclog.MemTransport, origin string, key []byte) {
	t.Helper()
	e, err := synclog.NewUpsert(origin, "binding", `["forged"]`, 1,
		[]byte(`{"id":"forged","name":"forged","cwd":"/f","builder_mode":"local","created_at":"t","ingest_source":"manual"}`),
		time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("build the forged entry: %v", err)
	}
	e.MAC = synclog.EntryMAC(key, e)
	if _, err := log.OnLog(origin).Append([]synclog.Entry{e}); err != nil {
		t.Fatalf("append the forged entry: %v", err)
	}
}

// helperMarkOf is the mark m has published for origin, zero when none.
func helperMarkOf(t *testing.T, d *db.DB, origin string) int {
	t.Helper()
	mark, _, err := d.ImportMark(origin)
	if err != nil {
		t.Fatalf("read the mark for %s: %v", origin, err)
	}
	return mark
}
