package synclog

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// memRekeySpec is a rekey of the log one handle holds: the material the remote
// is to hold at epoch 2, the epoch 1 key sealed for the machines still holding
// it, and one cut per origin at the head that handle observed.
func memRekeySpec(heads map[string]int) KeyRekeySpec {
	cuts := make(map[string]EpochCut, len(heads))
	for origin, seq := range heads {
		cuts[origin] = EpochCut{Epoch: 2, Seq: seq, Tag: []byte("the cut's tag")}
	}
	return KeyRekeySpec{
		From: 1,
		Key: KeyMeta{
			Salt: []byte("the rekeyed salt"), KDF: "argon2id",
			Check: []byte("the rekeyed check"), Present: true,
			Epoch: 2, Wrapped: map[int][]byte{1: []byte("the sealed epoch key")},
		},
		Cuts: cuts,
	}
}

// memHeads is what the handle observed before it asked.
func memHeads(t *testing.T, log *MemTransport) map[string]int {
	t.Helper()
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	return state.Heads
}

// memAppend gives one origin a head, so a cut has a position to sit at.
func memAppend(t *testing.T, log *MemTransport, pk string) Entry {
	t.Helper()
	e, err := NewUpsert(log.origin, "board", pk, 1, json.RawMessage(`{"x":1}`), time.Now())
	if err != nil {
		t.Fatalf("build the entry: %v", err)
	}
	written, err := log.Append([]Entry{e})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	return written[0]
}

// TestMemRekeyWritesTheEpochTheCallerNames pins the write on the fake the
// transport rules are read from: the epoch advances to the one the spec names,
// the key it replaced is stored sealed, and every origin's cut lands at the
// head the caller observed.
func TestMemRekeyWritesTheEpochTheCallerNames(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("a")
	written := memAppend(t, log, `["u"]`)
	spec := memRekeySpec(memHeads(t, log))

	held, err := log.KeyRekey(context.Background(), spec)
	if err != nil {
		t.Fatalf("KeyRekey: %v", err)
	}
	if held.Epoch != 2 || !held.Present {
		t.Errorf("read-back = %+v, want epoch 2, present", held)
	}
	if string(held.Wrapped[1]) != "the sealed epoch key" {
		t.Errorf("read-back wrapped = %v, want the sealed epoch 1 key", held.Wrapped)
	}
	cut, ok := log.log.epochCut["a"]
	if !ok {
		t.Fatal("the rekey wrote no cut for the origin that appended")
	}
	if cut.Epoch != 2 || cut.Seq != written.Seq {
		t.Errorf("cut = %+v, want epoch 2 at the head the caller observed (%d)", cut, written.Seq)
	}
	// The article the reader reads the cuts through answers with the same rows,
	// tags and all, which is what a verifying reader is handed.
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	read, ok := state.EpochCuts["a"]
	if !ok || read.Epoch != cut.Epoch || read.Seq != cut.Seq || string(read.Tag) != string(cut.Tag) {
		t.Errorf("ReadMarks epoch_cuts = %+v, want %+v", state.EpochCuts["a"], cut)
	}
}

// TestMemRekeyRefusesAStaleObservationAndWritesNothing pins that a rekey whose
// observation is no longer true writes nothing at all: a head that moved, and
// an origin the caller named that the log does not hold, both refuse.
func TestMemRekeyRefusesAStaleObservationAndWritesNothing(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("a")
	memAppend(t, log, `["u"]`)
	stale := memRekeySpec(map[string]int{"a": 1})
	memAppend(t, log, `["v"]`)
	if _, err := log.KeyRekey(context.Background(), stale); err == nil {
		t.Fatal("a rekey whose cut named a head the log had moved past came back as a success")
	}
	if log.log.key.Salt != nil || log.log.key.Epoch != 0 {
		t.Errorf("the log holds %+v, want the material it had before", log.log.key)
	}

	log = NewMemTransport("b")
	unknown := memRekeySpec(map[string]int{"a": 1})
	if _, err := log.KeyRekey(context.Background(), unknown); err == nil {
		t.Fatal("a rekey naming an origin the log does not hold came back as a success")
	}
	if log.log.key.Salt != nil {
		t.Error("a refused rekey wrote a salt")
	}
}

// TestMemRekeyIsACompareAndSetOnTheEpoch pins that two machines rekeying at
// once ends with the epoch advanced exactly once: the second caller, still
// holding the epoch it read, changes nothing.
func TestMemRekeyIsACompareAndSetOnTheEpoch(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("a")
	memAppend(t, log, `["u"]`)
	winner := memRekeySpec(memHeads(t, log))
	if _, err := log.KeyRekey(context.Background(), winner); err != nil {
		t.Fatalf("the winning rekey: %v", err)
	}
	if _, err := log.KeyRekey(context.Background(), memRekeySpec(memHeads(t, log))); err == nil {
		t.Fatal("a second rekey from an epoch the log had left came back as a success")
	}
	if log.log.key.Epoch != 2 {
		t.Fatalf("the log is at epoch %d, want 2: advanced exactly once", log.log.key.Epoch)
	}
	if string(log.log.key.Salt) != "the rekeyed salt" {
		t.Errorf("salt = %q, want the winner's", log.log.key.Salt)
	}
}
