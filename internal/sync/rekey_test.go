package sync

// The rekey's own cases, on the in-memory log: the act writes one epoch's whole
// worth or nothing, a wrong current passphrase changes nothing anywhere, a
// locked machine is refused before anything is read, the rotation may not
// rotate to the passphrase it is rotating away from, and the machine that lost
// the compare-and-set says so and keeps what it had.

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// rekeyPass is the fleet's current passphrase, derived for real so the remote's
// check is a real check.
var rekeyPass = []byte("the fleet's current passphrase")

// rekeyNew is what the fleet rotates to.
var rekeyNew = []byte("the fleet's new passphrase")

// rekeyFleetPair is one unlocked machine on its own file, synced to a log that
// also holds a peer's history: the machine created the fleet's material with
// its own unlock --new, so the remote's check is the real one for rekeyPass.
func rekeyFleetPair(t *testing.T) (*db.DB, Local, *synclog.MemTransport) {
	t.Helper()
	shared, local := joinPair(t, "m1")
	if err := MarkEnabled(local, true, time.Unix(0, 0)); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	log := synclog.NewMemTransport("m1")
	if _, err := Unlock(context.Background(), local, log, rekeyPass, true, rand.Reader); err != nil {
		t.Fatalf("unlock --new: %v", err)
	}
	seedFar(t, log, "m2", "b2", schemaOf(t, shared))
	return shared, local, log
}

// TestRekeyRotatesEverythingAsOneAct pins the happy rekey: the remote is at
// epoch 2 with the wrapped chain, every logged origin is cut at the head the
// log holds under a tag that verifies against the new key, the machine's
// keyring unwraps to both keys, the facts name the new epoch, and the secrets
// document is signalled so the next export re-seals it.
func TestRekeyRotatesEverythingAsOneAct(t *testing.T) {
	t.Parallel()
	shared, local, log := rekeyFleetPair(t)
	if err := Rekey(context.Background(), local, log, rekeyPass, rekeyNew, rand.Reader); err != nil {
		t.Fatalf("Rekey: %v", err)
	}

	meta, err := log.KeyMeta(context.Background())
	if err != nil || meta.Epoch != 2 {
		t.Fatalf("remote epoch = %d, %v; want 2", meta.Epoch, err)
	}
	if len(meta.Wrapped) != 1 {
		t.Fatalf("the remote holds %d wrapped keys, want the epoch-1 one", len(meta.Wrapped))
	}
	heads := raceHeads(t, log)
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	newKey := ringOf(t, local).Keys[2]
	for origin, head := range heads {
		cut := state.EpochCuts[origin]
		if cut.Epoch != 2 || cut.Seq != head {
			t.Fatalf("cut for %s = %+v, want epoch 2 at head %d", origin, cut, head)
		}
		if !synclog.VerifyEpochCut(newKey, origin, 2, head, cut.Tag) {
			t.Fatalf("the cut tag for %s does not verify under the new key", origin)
		}
	}
	ring := ringOf(t, local)
	if ring.Epoch != 2 || len(ring.Keys) != 2 {
		t.Fatalf("the stored ring is %+v, want epoch 2 with both keys", ring)
	}
	if state, err := KeyState(local); err != nil || state != "unlocked" {
		t.Fatalf("KeyState after the rekey = %q, %v; want unlocked", state, err)
	}
	signals, err := shared.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	if !hasSecretsSignal(signals) {
		t.Fatalf("signals = %v; the rekey left the secrets document unsignalled", signals)
	}
}

func hasSecretsSignal(signals []db.DocSignal) bool {
	for _, s := range signals {
		if s.Doc == SecretsDocName {
			return true
		}
	}
	return false
}

// ringOf reads the machine's keyring, failing the test when it holds none.
func ringOf(t *testing.T, local Local) Keyring {
	t.Helper()
	ring, ok, err := ReadKeyring(local)
	if err != nil || !ok {
		t.Fatalf("ReadKeyring = (ok %v, err %v)", ok, err)
	}
	return ring
}

// TestRekeyRefusesAWrongCurrentPassphraseWritingNothing pins the check-first
// rule: a wrong current passphrase is refused, and the remote's material, its
// cuts, this machine's keyring and its signals are all exactly as they were.
func TestRekeyRefusesAWrongCurrentPassphraseWritingNothing(t *testing.T) {
	t.Parallel()
	shared, local, log := rekeyFleetPair(t)
	before, err := log.KeyMeta(context.Background())
	if err != nil {
		t.Fatalf("KeyMeta: %v", err)
	}
	if err := Rekey(context.Background(), local, log, []byte("not the passphrase"), rekeyNew, rand.Reader); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("Rekey = %v, want the wrong-passphrase refusal", err)
	}
	after, err := log.KeyMeta(context.Background())
	if err != nil || after.Epoch != before.Epoch || !bytes.Equal(after.Check, before.Check) {
		t.Fatalf("the remote's material moved on a refused rekey")
	}
	state, err := log.ReadMarks()
	if err != nil || len(state.EpochCuts) != 0 {
		t.Fatalf("the remote holds cuts = %v, %v; want none", state.EpochCuts, err)
	}
	if ring := ringOf(t, local); ring.Epoch != 1 {
		t.Fatalf("the keyring moved to epoch %d; a refused rekey stores nothing", ring.Epoch)
	}
	signals, err := shared.DocSignals()
	if err != nil || hasSecretsSignal(signals) {
		t.Fatalf("signals = %v, %v; a refused rekey signals nothing", signals, err)
	}
}

// TestRekeyRefusesALockedMachine pins the first check: a machine with no key
// is refused with the locked refusal -- which names unlock -- before anything
// of the remote's changes.
func TestRekeyRefusesALockedMachine(t *testing.T) {
	t.Parallel()
	_, local, log := rekeyFleetPair(t)
	if err := DeleteKey(local); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if err := Rekey(context.Background(), local, log, rekeyPass, rekeyNew, rand.Reader); !errors.Is(err, ErrMachineLocked) {
		t.Fatalf("Rekey = %v, want the locked refusal", err)
	}
	if !strings.Contains(ErrMachineLocked.Error(), "relevo db sync unlock") {
		t.Errorf("the locked refusal does not name unlock: %v", ErrMachineLocked)
	}
	if meta, err := log.KeyMeta(context.Background()); err != nil || meta.Epoch != 1 {
		t.Fatalf("the remote moved to epoch %d, %v; a locked machine's rekey writes nothing", meta.Epoch, err)
	}
}

// TestRekeyRefusesTheCurrentPassphraseAsTheNewOne pins the rotation rule: the
// new passphrase may not be the current one, because the same passphrase
// against the new salt re-derives the same key and retires nothing.
func TestRekeyRefusesTheCurrentPassphraseAsTheNewOne(t *testing.T) {
	t.Parallel()
	_, local, log := rekeyFleetPair(t)
	if err := Rekey(context.Background(), local, log, rekeyPass, rekeyPass, rand.Reader); !errors.Is(err, ErrSamePassphrase) {
		t.Fatalf("Rekey = %v, want the same-passphrase refusal", err)
	}
	if meta, err := log.KeyMeta(context.Background()); err != nil || meta.Epoch != 1 {
		t.Fatalf("the remote moved to epoch %d, %v; a refused rotation writes nothing", meta.Epoch, err)
	}
}

// TestRekeyRefusesAnEmptyNewPassphrase pins the empty rule: the new
// passphrase may not be empty, and a refused rotation writes nothing to the
// remote.
func TestRekeyRefusesAnEmptyNewPassphrase(t *testing.T) {
	t.Parallel()
	_, local, log := rekeyFleetPair(t)
	if err := Rekey(context.Background(), local, log, rekeyPass, nil, rand.Reader); !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("Rekey with nil = %v, want ErrEmptyPassphrase", err)
	}
	if err := Rekey(context.Background(), local, log, rekeyPass, []byte(""), rand.Reader); !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("Rekey with empty slice = %v, want ErrEmptyPassphrase", err)
	}
	if meta, err := log.KeyMeta(context.Background()); err != nil || meta.Epoch != 1 {
		t.Fatalf("the remote moved to epoch %d, %v; a refused rotation writes nothing", meta.Epoch, err)
	}
	state, err := log.ReadMarks()
	if err != nil || len(state.EpochCuts) != 0 {
		t.Fatalf("the remote holds cuts = %v, %v; want none", state.EpochCuts, err)
	}
}

// TestTheRekeyLoserChangesNothingAndSaysSo pins the race: a machine whose
// observation of the remote is one rekey behind loses the compare-and-set, the
// refusal names the race, and nothing of its own was stored.
func TestTheRekeyLoserChangesNothingAndSaysSo(t *testing.T) {
	t.Parallel()
	_, local, log := rekeyFleetPair(t)
	oldMeta, err := log.KeyMeta(context.Background())
	if err != nil {
		t.Fatalf("KeyMeta before the rekey: %v", err)
	}
	if err := Rekey(context.Background(), local, log, rekeyPass, rekeyNew, rand.Reader); err != nil {
		t.Fatalf("the winner's rekey: %v", err)
	}
	ring := ringOf(t, local)

	// A loser that still read epoch 1: its start answer is the material the
	// remote held before the winner's write and its later reads see the
	// winner's epoch, which is what a read that happened before another
	// machine's write and a re-read that happened after it look like.
	loser := &rekeyMetaMover{LogTransport: log, start: &oldMeta}
	loser.after = func() *synclog.KeyMeta {
		meta, err := log.KeyMeta(context.Background())
		if err != nil {
			t.Fatalf("KeyMeta: %v", err)
		}
		return &meta
	}
	if err := Rekey(context.Background(), local, loser, rekeyPass, rekeyNew, rand.Reader); !errors.Is(err, ErrEpochRace) {
		t.Fatalf("the loser's rekey = %v, want the race refusal", err)
	}
	if !strings.Contains(ErrEpochRace.Error(), "another machine rekeyed first") ||
		!strings.Contains(ErrEpochRace.Error(), "relevo db sync unlock") {
		t.Errorf("the race refusal does not say what happened and what fixes it: %v", ErrEpochRace)
	}
	if got := ringOf(t, local); got.Epoch != ring.Epoch || len(got.Keys) != len(ring.Keys) {
		t.Fatalf("the loser's ring moved: %+v -> %+v; a lost race stores nothing", ring, got)
	}
}

// TestARekeyAnswerFromAnotherEpochIsTheRace pins the read-back check: a
// transport that accepts the write but answers with an epoch that is not the
// one the caller wrote -- two rekeys landed under it -- is refused as the race,
// before anything is stored.
func TestARekeyAnswerFromAnotherEpochIsTheRace(t *testing.T) {
	t.Parallel()
	_, local, log := rekeyFleetPair(t)
	ring := ringOf(t, local)
	loser := &rekeyMetaMover{LogTransport: log}
	loser.rekey = func() (synclog.KeyMeta, error) {
		return synclog.KeyMeta{Salt: []byte("elsewhere"), KDF: DefaultKDF.String(),
			Check: []byte("elsewhere"), Present: true, Epoch: 3}, nil
	}
	if err := Rekey(context.Background(), local, loser, rekeyPass, rekeyNew, rand.Reader); !errors.Is(err, ErrEpochRace) {
		t.Fatalf("the rekey = %v, want the race refusal", err)
	}
	if got := ringOf(t, local); got.Epoch != ring.Epoch {
		t.Fatalf("the ring moved to epoch %d; an answer from another epoch stores nothing", got.Epoch)
	}
}

// TestARekeyAnswerFromAnotherWinnerAtSameEpochIsTheRace pins the same-epoch
// read-back check: a transport that answers with another machine's check at the
// same epoch -- two machines rekeyed concurrently and this caller's write was
// lost or overwritten -- is refused as the race before anything is stored.
func TestARekeyAnswerFromAnotherWinnerAtSameEpochIsTheRace(t *testing.T) {
	t.Parallel()
	_, local, log := rekeyFleetPair(t)
	ring := ringOf(t, local)
	loser := &rekeyMetaMover{LogTransport: log}
	loser.rekey = func() (synclog.KeyMeta, error) {
		return synclog.KeyMeta{Salt: []byte("elsewhere"), KDF: DefaultKDF.String(),
			Check: []byte("winner check"), Present: true, Epoch: 2}, nil
	}
	if err := Rekey(context.Background(), local, loser, rekeyPass, rekeyNew, rand.Reader); !errors.Is(err, ErrEpochRace) {
		t.Fatalf("the rekey = %v, want the race refusal", err)
	}
	if got := ringOf(t, local); got.Epoch != ring.Epoch {
		t.Fatalf("the ring moved to epoch %d; an answer with another machine's check stores nothing", got.Epoch)
	}
}

// rekeyMetaMover is a log whose key reads are the test's to stage: the first
// KeyMeta answers whatever the log holds now, and later ones answer after. It
// is how one test holds a machine's start observation still while the remote
// moves under it.
type rekeyMetaMover struct {
	synclog.LogTransport
	calls int
	start *synclog.KeyMeta
	after func() *synclog.KeyMeta
	rekey func() (synclog.KeyMeta, error)
}

func (m *rekeyMetaMover) KeyMeta(context.Context) (synclog.KeyMeta, error) {
	m.calls++
	if m.calls > 1 && m.after != nil {
		return *m.after(), nil
	}
	if m.start != nil {
		return *m.start, nil
	}
	return m.LogTransport.KeyMeta(context.Background())
}

func (m *rekeyMetaMover) KeyRekey(ctx context.Context, spec synclog.KeyRekeySpec) (synclog.KeyMeta, error) {
	if m.rekey != nil {
		return m.rekey()
	}
	return m.LogTransport.KeyRekey(ctx, spec)
}
