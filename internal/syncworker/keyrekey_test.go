package syncworker

import (
	"database/sql"
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// rekeySpec is the spec a rekeying machine would send: a fresh salt and check
// for epoch 2, the epoch 1 key sealed under it, and one cut per origin in the
// log at the head the caller observed.
func rekeySpec(t *testing.T, from int, heads map[string]int) KeyRekeySpec {
	t.Helper()
	key := []byte("the rekeyed key")
	cuts := make(map[string]EpochCut, len(heads))
	for origin, seq := range heads {
		cuts[origin] = EpochCut{Epoch: from + 1, Seq: seq, Tag: synclog.EpochCutTag(key, origin, from+1, seq)}
	}
	return KeyRekeySpec{
		From: from,
		Key: KeyMeta{
			Salt: []byte("the rekeyed salt"), KDF: "argon2id",
			Check: []byte("the rekeyed check"), Present: true,
			Epoch: from + 1, Wrapped: map[int][]byte{from: []byte("the sealed epoch key")},
		},
		Cuts: cuts,
	}
}

// headsOf is what a rekeying machine observed before it asked: every origin's
// highest sequence, read off the backend it is about to hand the spec to.
func headsOf(t *testing.T, b *TursoBackend) map[string]int {
	t.Helper()
	state, err := b.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks before the rekey: %v", err)
	}
	return state.Heads
}

// metaRow is one row of the replica's meta table, and whether it is there.
func metaRow(t *testing.T, path, key string) (string, bool) {
	t.Helper()
	var value string
	err := openAt(t, path).QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return value, true
}

// TestKeyRekeyWritesTheEpochTheCallerNames pins the whole write: the epoch
// advances to the one the spec names, the new material replaces the old, the
// key it replaced is stored sealed, and every origin's cut lands at the head
// the caller observed -- with a tag that verifies under the new key.
func TestKeyRekeyWritesTheEpochTheCallerNames(t *testing.T) {
	t.Parallel()
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["u"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}

	spec := rekeySpec(t, 1, headsOf(t, b))
	held, err := b.KeyRekey(spec)
	if err != nil {
		t.Fatalf("KeyRekey: %v", err)
	}
	if held.Epoch != 2 || !held.Present {
		t.Errorf("read-back = %+v, want epoch 2, present", held)
	}
	if string(held.Wrapped[1]) != "the sealed epoch key" {
		t.Errorf("read-back wrapped = %v, want the sealed epoch 1 key", held.Wrapped)
	}
	if !synclog.VerifyEpochCut([]byte("the rekeyed key"), "origin-a", 2, 1, spec.Cuts["origin-a"].Tag) {
		t.Fatal("the test's own cut tag does not verify; the pins below mean nothing")
	}
	if epoch, ok := metaRow(t, path, metaKeyEpochKey); !ok || epoch != "2" {
		t.Errorf("key_epoch = %q, %v; want 2", epoch, ok)
	}
	if wrapped, ok := metaRow(t, path, keyWrappedPrefix+"1"); !ok || wrapped == "" {
		t.Errorf("key_wrapped.1 = %q, %v; want the sealed key", wrapped, ok)
	}
	want := "2 1 " + encodeKeyValue(spec.Cuts["origin-a"].Tag)
	if cut, ok := metaRow(t, path, epochCutPrefix+"origin-a"); !ok || cut != want {
		t.Errorf("epoch_cut.origin-a = %q, %v; want %q", cut, ok, want)
	}
}

// TestKeyRekeyRefusesAMovedHeadAndWritesNothing pins the predict-and-verify:
// the cuts are checked against the log's own heads inside the transaction, so
// an origin whose head moved between the caller's read and the write refuses
// the whole rekey and leaves the remote's material as it was.
func TestKeyRekeyRefusesAMovedHeadAndWritesNothing(t *testing.T) {
	t.Parallel()
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["u"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	stale := rekeySpec(t, 1, map[string]int{"origin-a": 1})
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["v"]`, `{"title":"two"}`)}); err != nil {
		t.Fatalf("the append that moved the head: %v", err)
	}

	if _, err := b.KeyRekey(stale); err == nil {
		t.Fatal("a rekey whose cuts named a head the log had moved past came back as a success")
	}
	if _, ok := metaRow(t, path, metaKeyEpochKey); ok {
		t.Error("a refused rekey wrote key_epoch")
	}
	if _, ok := metaRow(t, path, metaKDFSaltKey); ok {
		t.Error("a refused rekey wrote a salt")
	}
}

// TestKeyRekeyIsACompareAndSetOnTheEpoch pins that two machines rekeying at
// once ends with the epoch advanced exactly once: the second caller, still
// holding the epoch it read, changes nothing -- not the epoch, not the salt it
// wanted, not the cuts it predicted.
func TestKeyRekeyIsACompareAndSetOnTheEpoch(t *testing.T) {
	t.Parallel()
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["u"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	winner := rekeySpec(t, 1, headsOf(t, b))
	if _, err := b.KeyRekey(winner); err != nil {
		t.Fatalf("the winning rekey: %v", err)
	}
	loser := rekeySpec(t, 1, headsOf(t, b))
	if _, err := b.KeyRekey(loser); err == nil {
		t.Fatal("a second rekey from an epoch the remote had left came back as a success")
	}
	if epoch, ok := metaRow(t, path, metaKeyEpochKey); !ok || epoch != "2" {
		t.Errorf("key_epoch = %q, %v; want 2, advanced exactly once", epoch, ok)
	}
	if salt, ok := metaRow(t, path, metaKDFSaltKey); !ok || salt != encodeKeyValue(winner.Key.Salt) {
		t.Errorf("kdf_salt = %q, want the winner's", salt)
	}
	if cut, ok := metaRow(t, path, epochCutPrefix+"origin-a"); ok {
		want := "2 1 " + encodeKeyValue(winner.Cuts["origin-a"].Tag)
		if cut != want {
			t.Errorf("epoch_cut.origin-a = %q, want the winner's %q", cut, want)
		}
	}
}

// TestKeyRekeyCarriesTheRaceOverThePipe pins that the race crosses the pipe as
// its own class: a daemon reading the refusal knows the remote rekeyed under
// it, not that the remote failed to answer.
func TestKeyRekeyCarriesTheRaceOverThePipe(t *testing.T) {
	t.Parallel()
	b := backendFor(t, &localDriver{path: tempReplica(t)}, "origin-a")
	w := startWorker(t, b)
	w.askHello()

	init := w.ask(Request{ID: "1", Verb: VerbKeyInit, KeyMeta: &KeyMeta{
		Salt: []byte("the first salt"), KDF: "argon2id", Check: []byte("the first check"), Present: true,
	}})
	if !init.OK {
		t.Fatalf("key_init = %+v, want ok", init)
	}
	spec := rekeySpec(t, 1, nil)
	first := w.ask(Request{ID: "2", Verb: VerbKeyRekey, Epoch: 1, KeyMeta: &spec.Key, Cuts: spec.Cuts})
	if !first.OK {
		t.Fatalf("the first key_rekey = %+v, want ok", first)
	}
	loser := rekeySpec(t, 1, nil)
	second := w.ask(Request{ID: "3", Verb: VerbKeyRekey, Epoch: 1, KeyMeta: &loser.Key})
	if second.OK {
		t.Fatal("the second key_rekey from epoch 1 was answered, want a refusal")
	}
	if second.Code != CodeEpochRace {
		t.Errorf("the second key_rekey carried code %q, want %q", second.Code, CodeEpochRace)
	}
}
