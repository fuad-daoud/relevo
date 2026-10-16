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

var (
	// racePassphrase is the rekey's new passphrase, derived for real so a
	// later unlock produces the same key and passes the remote's check.
	racePassphrase = []byte("the rekey's new passphrase")
	// raceKey1 is raw bytes, because the epoch-1 keys this test's machines
	// hold are the round-1 fixtures' shape: what the local secret table
	// stores and the exporter signs with.
	raceKey1 = []byte("0123456789abcdef0123456789abcdef")
)

func TestUnlockVouchesItsOwnTailAndTheFleetAppliesIt(t *testing.T) {
	t.Parallel()
	fleet, cert := raceFleet(t)
	raceStale(t, fleet, cert)
	raceHeldByFleet(t, fleet, cert)
	raceVouch(t, fleet, cert)
	raceApplied(t, fleet, cert)
}

// racePair is the two machines of the race on one shared log, converged at
// epoch 1: both unlocked, each having run one steady attempt against the
// other's history. fleet.key2 and fleet.log carry into every later act.
type racePair struct {
	aShared *db.DB
	aLocal  Local
	bShared *db.DB
	// bLocal is named Local to stay beside the others' shape.
	bLocal Local
	log    *synclog.MemTransport
}

// raceCert is the epoch-2 material the rekey wrote: the derived key, which a
// later tag check verifies against.
type raceCert struct {
	key2 []byte
}

func raceFleet(t *testing.T) (racePair, raceCert) {
	t.Helper()
	aShared, aLocal := joinPair(t, "m1")
	seedJoin(t, aShared, joinBinding("a1", "m1"))
	bShared, bLocal := joinPair(t, "m2")
	seedJoin(t, bShared, joinBinding("b1", "m2"))
	log := synclog.NewMemTransport("m1")
	if err := MarkEnabled(aLocal, true, time.Unix(0, 0)); err != nil {
		t.Fatalf("MarkEnabled m1: %v", err)
	}
	if err := MarkEnabled(bLocal, true, time.Unix(0, 0)); err != nil {
		t.Fatalf("MarkEnabled m2: %v", err)
	}
	if err := SetKey(aLocal, raceKey1, time.Now()); err != nil {
		t.Fatalf("SetKey m1: %v", err)
	}
	if err := SetKey(bLocal, raceKey1, time.Now()); err != nil {
		t.Fatalf("SetKey m2: %v", err)
	}
	if res := (&Runner{Client: log, Local: aLocal}).SyncOnce(context.Background(), aShared); res.Err != nil {
		t.Fatalf("m1's first attempt: %v", res.Err)
	}
	if res := (&Runner{Client: log.OnLog("m2"), Local: bLocal}).SyncOnce(context.Background(), bShared); res.Err != nil {
		t.Fatalf("m2's first attempt: %v", res.Err)
	}

	// m2 rekeys the shared log to epoch 2 with real material, so a later
	// unlock derives the same key and passes the remote's check.
	salt := raceSalt(t)
	key2 := DeriveKey(racePassphrase, salt, DefaultKDF)
	wrapped, err := WrapKey(key2, 1, raceKey1)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	raceRekeyTo2(t, log, key2, wrapped, salt, raceHeads(t, log))
	return racePair{aShared: aShared, aLocal: aLocal, bShared: bShared,
		bLocal: bLocal, log: log}, raceCert{key2: key2}
}

// raceStale is m1 appending under the old key before it reads the remote
// again, through the export path itself, then being stopped whole by its next
// attempt.
func raceStale(t *testing.T, fleet racePair, cert raceCert) {
	t.Helper()
	seedJoin(t, fleet.aShared, joinBinding("a2", "m1"))
	raceOldKeyExport(t, fleet.aShared, fleet.log, raceKey1)
	aHead, ok, err := synclog.OwnHead(fleet.aShared.LocalOrSelf())
	if err != nil || !ok {
		t.Fatalf("own head = (%d, %v, %v)", aHead, ok, err)
	}
	res := (&Runner{Client: fleet.log, Local: fleet.aLocal, Timeout: time.Second}).
		SyncOnce(context.Background(), fleet.aShared)
	if res.Err != nil || res.Exported != 0 || res.Applied != 0 {
		t.Fatalf("stale attempt = %+v, %v", res, res.Err)
	}
}

// raceHeldByFleet pins that the fleet refuses m1's stale tail: m2's import
// rests under the head the stale export confirmed, above the fence.
func raceHeldByFleet(t *testing.T, fleet racePair, cert raceCert) {
	t.Helper()
	res := (&Runner{Client: fleet.log.OnLog("m2"), Local: fleet.bLocal, Timeout: time.Second}).
		SyncOnce(context.Background(), fleet.bShared)
	if res.Err != nil {
		t.Fatalf("m2's attempt: %v", res.Err)
	}
	if mark := raceMarkOf(t, fleet.bShared, "m1"); mark >= 2 {
		t.Fatalf("m2's mark on m1 = %d, want it to rest under the stale tail's head", mark)
	}
	_ = cert
}

// raceVouch is m1 unlocking with the new passphrase and the extension that
// lands on the remote at the head this machine itself confirmed, tagged under
// the new key.
func raceVouch(t *testing.T, fleet racePair, cert raceCert) {
	t.Helper()
	if _, err := Unlock(context.Background(), fleet.aLocal, fleet.log, racePassphrase, false, rand.Reader); err != nil {
		t.Fatalf("m1's unlock: %v", err)
	}
	aHead, ok, err := synclog.OwnHead(fleet.aShared.LocalOrSelf())
	if err != nil || !ok {
		t.Fatalf("own head = (%d, %v, %v)", aHead, ok, err)
	}
	PublishOwnCut(context.Background(), fleet.aShared, fleet.aLocal, fleet.log)
	state, err := fleet.log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if cut := state.EpochCuts["m1"]; cut.Seq != aHead || cut.Epoch != 2 {
		t.Fatalf("remote cut for m1 = %+v, want epoch 2 at the confirmed head (%d)", cut, aHead)
	}
	if !synclog.VerifyEpochCut(cert.key2, "m1", 2, aHead, state.EpochCuts["m1"].Tag) {
		t.Fatal("the extension's tag does not verify under the new key")
	}
}

// raceApplied is the fleet applying the vouched tail and refusing a forgery
// above it: m2 unlocks, its import raises its cut for m1, the old-key entries
// verify by trial, and an entry above the vouched head moves no mark.
func raceApplied(t *testing.T, fleet racePair, cert raceCert) {
	t.Helper()
	if _, err := Unlock(context.Background(), fleet.bLocal, fleet.log.OnLog("m2"), racePassphrase, false, rand.Reader); err != nil {
		t.Fatalf("m2's unlock: %v", err)
	}
	ring, ok, err := ReadKeyring(fleet.bLocal)
	if err != nil || !ok {
		t.Fatalf("m2's keyring ok=%v err=%v", ok, err)
	}
	imp, close_ := raceM2Importer(t, fleet, ring)
	defer close_()
	if _, err := imp.Import(); err != nil {
		t.Fatalf("m2's import after the vouch: %v", err)
	}
	if mark := raceMarkOf(t, fleet.bShared, "m1"); mark != 2 {
		t.Fatalf("m2's mark on m1 = %d, want %d: the vouched tail applied", mark, 2)
	}
	forgeOldKey(t, fleet.log, "m1", raceKey1)
	imp2, close2 := raceM2Importer(t, fleet, ring)
	defer close2()
	if _, err := imp2.Import(); err != nil {
		t.Fatalf("the forged import: %v", err)
	}
	if mark := raceMarkOf(t, fleet.bShared, "m1"); mark != 2 {
		t.Fatalf("the forged import moved m2's mark to %d, want it at %d", mark, 2)
	}
}

// raceM2Importer is m2's verifier over the log, with its keyring bound.
func raceM2Importer(t *testing.T, fleet racePair, ring Keyring) (*synclog.Importer, func()) {
	t.Helper()
	imp := synclog.NewImporter(fleet.bShared, fleet.log.OnLog("m2"))
	imp.SetKeyring(synclog.Keyring{Epoch: ring.Epoch, Keys: ring.Keys})
	return imp, func() {}
}

// raceMarkOf is m's published mark for origin, zero when none.
func raceMarkOf(t *testing.T, d *db.DB, origin string) int {
	t.Helper()
	mark, _, err := d.ImportMark(origin)
	if err != nil {
		t.Fatalf("read the mark for %s: %v", origin, err)
	}
	return mark
}

// raceSalt is the fresh salt a rekey rolls.
func raceSalt(t *testing.T) []byte {
	t.Helper()
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		t.Fatalf("salt: %v", err)
	}
	return salt
}

// raceRekeyTo2 rekeys log to epoch 2, one cut per logged origin at the heads
// given, the new key's material on the log and the old key sealed for holders
// of it.
func raceRekeyTo2(t *testing.T, log *synclog.MemTransport, key2, wrapped, salt []byte, heads map[string]int) {
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

// raceHeads is every logged origin's head, which is what a rekey's cuts sit
// at. The rekey reads them itself and refuses stale claims, so the test asks
// the log for what it holds rather than hard-coding the numbers.
func raceHeads(t *testing.T, log *synclog.MemTransport) map[string]int {
	t.Helper()
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	return state.Heads
}

// raceOldKeyExport is m1 appending its pending rows under the old key, the
// append an attempt that had not yet noticed the rekey would perform. It is
// the export path itself, so the own-head fact it writes is the fact the
// extension vouches for.
func raceOldKeyExport(t *testing.T, d *db.DB, log *synclog.MemTransport, key []byte) {
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
