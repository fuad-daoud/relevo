package sync

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The stale stop's own cases: a machine whose held newest epoch sits below the
// recorded remote epoch stops both halves of the exchange until its owner
// unlocks it with the new passphrase, and status says so.

// rekeyTheLog rotates the runner's log to epoch 2 with a cut per logged origin
// at the heads the log itself holds, the way a rekey on another machine would
// have written it. The runner's machine -- still on keyOld, the epoch-1 key
// it was set up with -- is stale the moment it reads the remote.
func rekeyTheLog(t *testing.T, log *synclog.MemTransport, keyOld, keyNew []byte) {
	t.Helper()
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	cuts := make(map[string]synclog.EpochCut, len(state.Heads))
	for origin, head := range state.Heads {
		cuts[origin] = synclog.EpochCut{Epoch: 2, Seq: head, Tag: synclog.EpochCutTag(keyNew, origin, 2, head)}
	}
	if _, err := log.KeyRekey(context.Background(), synclog.KeyRekeySpec{
		From: 1,
		Key: synclog.KeyMeta{
			Salt: []byte("the rekeyed salt"), KDF: "argon2id", Check: []byte("the rekeyed check"),
			Present: true, Epoch: 2, Wrapped: map[int][]byte{1: keyOld},
		},
		Cuts: cuts,
	}); err != nil {
		t.Fatalf("KeyRekey: %v", err)
	}
}

// TestStaleMachineStopsBothHalvesAndNamesThePassphraseChange pins the stop: a
// machine on the old key exports nothing and applies nothing after the
// remote's epoch moved, and both its trouble line and its status name the one
// act that unblocks it, relevo db sync unlock.
func TestStaleMachineStopsBothHalvesAndNamesThePassphraseChange(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	seedJoin(t, shared, joinBinding("b1", "m1"))
	key1 := []byte("0123456789abcdef0123456789abcdef")
	key2 := []byte("fedcba9876543210fedcba9876543210")

	log := synclog.NewMemTransport("m1")
	// A peer has already appended history under the old key, so there is
	// something both halves would move if the gate were absent.
	seedFar(t, log, "m2", "b2", schemaOf(t, shared))
	if err := SetKey(local, key1, time.Now()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	runner := &Runner{Client: log, Local: local, Timeout: time.Second}
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("the first attempt: %v", res.Err)
	}

	// The remote rekeys. The next attempt's hello records epoch 2; this
	// machine holds epoch 1 and is stale. A row is seeded here first so an
	// attempt without the stop would have something to export -- the point
	// the mutation check below lands on.
	rekeyTheLog(t, log, key1, key2)
	seedJoin(t, shared, joinBinding("b3", "m1"))
	res := runner.SyncOnce(context.Background(), shared)
	if res.Err != nil {
		t.Fatalf("the stale attempt: %v", res.Err)
	}
	if res.Exported != 0 {
		t.Errorf("Exported = %d, want nothing appended", res.Exported)
	}
	if res.Applied != 0 {
		t.Errorf("Applied = %d, want nothing written", res.Applied)
	}
	// One trouble line, and it names the unlock.
	if len(res.Trouble.Held) != 1 || !strings.Contains(res.Trouble.Held[0], "run relevo db sync unlock") {
		t.Errorf("trouble = %v, want the one line that names the unlock", res.Trouble.Held)
	}
	// The next attempt repeats the line rather than latching it away.
	again := runner.SyncOnce(context.Background(), shared)
	if len(again.Trouble.Held) != 1 {
		t.Errorf("second attempt trouble = %v, want the same one line", again.Trouble.Held)
	}

	// Status agrees, before any network: the key line names the passphrase
	// change.
	state, err := KeyState(local)
	if err != nil || state != "stale" {
		t.Fatalf("KeyState = %q, %v; want stale", state, err)
	}
}

// TestStaleGateLeavesALockedAndAFreshMachineAlone pins the gate's negative
// cases: a machine with no key and a machine whose held epoch names the
// remote's newest both pass through untouched.
func TestStaleGateLeavesALockedAndAFreshMachineAlone(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	seedJoin(t, shared, joinBinding("b1", "m1"))
	key2 := []byte("fedcba9876543210fedcba9876543210")
	log := synclog.NewMemTransport("m1")
	seedFar(t, log, "m2", "b2", schemaOf(t, shared))

	// Locked: no key, so no held epoch. The remote's epoch is recorded by the
	// attempt's hello, at 1 with no rekey.
	runner := &Runner{Client: log, Local: local, Timeout: time.Second}
	res := runner.SyncOnce(context.Background(), shared)
	if res.Err != nil || len(res.Trouble.Held) != 0 {
		t.Fatalf("locked machine = %+v, %v; want a normal attempt", res, res.Err)
	}

	// Fresh: the machine is set up with the epoch-1 key and the remote
	// rekeys, and the machine unlock stores the newest key -- its held epoch
	// names the remote's and the gate opens.
	rekeyTheLog(t, log, key2, key2)
	if err := SetKeys(local, Keyring{Epoch: 2, Keys: map[int][]byte{1: key2, 2: key2}}, time.Now()); err != nil {
		t.Fatalf("SetKeys: %v", err)
	}
	// The fact was recorded on the previous attempt; the machine's held epoch
	// is 2 and the gate says nothing.
	if err := runner.epochGate(); err != nil {
		t.Fatalf("epochGate on a fresh machine = %v", err)
	}
}

// schemaOf is the schema version the tests seed foreign entries with.
func schemaOf(t *testing.T, d interface{ SchemaVersions() (int, int) }) int {
	t.Helper()
	_, known := d.SchemaVersions()
	return known
}

// overwriteRekeyWithWinner simulates the second winner in a double-win race,
// writing winner B's material at epoch 2 over winner A's.
func overwriteRekeyWithWinner(t *testing.T, log *synclog.MemTransport, localA Local, passB2 []byte) {
	t.Helper()
	saltB := []byte("salt-for-winner-b")
	keyB2 := DeriveKey(passB2, saltB, DefaultKDF)
	ringA, ok, err := ReadKeyring(localA)
	if err != nil || !ok {
		t.Fatalf("ReadKeyring: %v", err)
	}
	wrappedB, err := WrapKey(keyB2, 1, ringA.Keys[1])
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	cuts := make(map[string]synclog.EpochCut, len(state.Heads))
	for orig, head := range state.Heads {
		cuts[orig] = synclog.EpochCut{Epoch: 2, Seq: head, Tag: synclog.EpochCutTag(keyB2, orig, 2, head)}
	}
	if _, err := log.KeyRekey(context.Background(), synclog.KeyRekeySpec{
		From: 2,
		Key: synclog.KeyMeta{
			Salt: saltB, KDF: DefaultKDF.String(), Check: KeyCheck(keyB2), Present: true, Epoch: 2,
			Wrapped: map[int][]byte{1: wrappedB},
		},
		Cuts: cuts,
	}); err != nil {
		t.Fatalf("B KeyRekey: %v", err)
	}
}

// checkMatchingMachineUnaffected confirms a machine unlocking with winner B's
// passphrase syncs without trouble and reports unlocked.
func checkMatchingMachineUnaffected(t *testing.T, log *synclog.MemTransport, passB2 []byte) {
	t.Helper()
	sharedB, localB := joinPair(t, "m2")
	if err := MarkEnabled(localB, true, time.Unix(0, 0)); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	if _, err := Unlock(context.Background(), localB, log.OnLog("m2"), passB2, false, rand.Reader); err != nil {
		t.Fatalf("B unlock: %v", err)
	}
	runnerB := &Runner{Client: log.OnLog("m2"), Local: localB, Timeout: time.Second}
	resB := runnerB.SyncOnce(context.Background(), sharedB)
	if resB.Err != nil || len(resB.Trouble.Held) != 0 {
		t.Fatalf("B attempt trouble = %v, err = %v; want unaffected attempt", resB.Trouble.Held, resB.Err)
	}
	stateB, err := KeyState(localB)
	if err != nil || stateB != "unlocked" {
		t.Fatalf("KeyState B = %q, %v; want unlocked", stateB, err)
	}
}

// TestDoubleWinRekeyRaceCheckMismatchStopsExchangeUntilUnlock pins that when
// two machines win the rekey race and B overwrites A's material at the same
// epoch, A's next attempt stops exchange with ErrEpochReplaced and KeyState
// stale, unlock with B's passphrase recovers it, and a machine matching B is
// unaffected.
func TestDoubleWinRekeyRaceCheckMismatchStopsExchangeUntilUnlock(t *testing.T) {
	t.Parallel()
	sharedA, localA := joinPair(t, "m1")
	if err := MarkEnabled(localA, true, time.Unix(0, 0)); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	log := synclog.NewMemTransport("m1")
	seedFar(t, log, "m2", "b2", schemaOf(t, sharedA))

	passA := []byte("passphrase A")
	if _, err := Unlock(context.Background(), localA, log, passA, true, rand.Reader); err != nil {
		t.Fatalf("A unlock --new: %v", err)
	}

	seedJoin(t, sharedA, joinBinding("b1", "m1"))
	runnerA := &Runner{Client: log, Local: localA, Timeout: time.Second}
	if res := runnerA.SyncOnce(context.Background(), sharedA); res.Err != nil {
		t.Fatalf("A initial attempt: %v", res.Err)
	}

	passA2 := []byte("passphrase A2")
	if err := Rekey(context.Background(), localA, log, passA, passA2, rand.Reader); err != nil {
		t.Fatalf("A rekey to 2: %v", err)
	}

	passB2 := []byte("passphrase B2")
	overwriteRekeyWithWinner(t, log, localA, passB2)

	seedJoin(t, sharedA, joinBinding("b3", "m1"))
	resA := runnerA.SyncOnce(context.Background(), sharedA)
	if resA.Err != nil {
		t.Fatalf("A attempt error: %v", resA.Err)
	}
	if resA.Exported != 0 || resA.Applied != 0 {
		t.Fatalf("A exported %d, applied %d; want nothing moved", resA.Exported, resA.Applied)
	}
	if len(resA.Trouble.Held) != 1 || resA.Trouble.Held[0] != ErrEpochReplaced.Error() {
		t.Fatalf("trouble = %v, want %q", resA.Trouble.Held, ErrEpochReplaced.Error())
	}
	stateA, err := KeyState(localA)
	if err != nil || stateA != "stale" {
		t.Fatalf("KeyState A = %q, %v; want stale", stateA, err)
	}

	checkMatchingMachineUnaffected(t, log, passB2)

	if _, err := Unlock(context.Background(), localA, log, passB2, false, rand.Reader); err != nil {
		t.Fatalf("A unlock with winner passphrase: %v", err)
	}
	stateAAfter, err := KeyState(localA)
	if err != nil || stateAAfter != "unlocked" {
		t.Fatalf("KeyState A after unlock = %q, %v; want unlocked", stateAAfter, err)
	}
	resA2 := runnerA.SyncOnce(context.Background(), sharedA)
	if resA2.Err != nil || len(resA2.Trouble.Held) != 0 {
		t.Fatalf("A attempt after recovery: %v, %v", resA2.Err, resA2.Trouble.Held)
	}
	if resA2.Exported == 0 {
		t.Errorf("A exported %d, want b3 to be exported", resA2.Exported)
	}
}
