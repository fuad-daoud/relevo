package synclog

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// The MAC's path through the transport a slice is written against: a stored
// entry keeps it, the reads that hand an entry back hand the MAC with it, and a
// compaction that keeps a survivor keeps its signature. Every one of those is a
// place the field could be dropped and nothing would notice until a reader
// tried to verify.

func TestMemTransportCarriesMACThroughPullRangeAndCompaction(t *testing.T) {
	t.Parallel()
	writer := NewMemTransport("origin-a")
	reader := writer.OnLog("origin-b")
	key := []byte("the passphrase this machine stretched")

	at := time.Date(2026, 10, 8, 9, 30, 0, 123456789, time.UTC)
	signed := func(pk, title string) Entry {
		t.Helper()
		e, err := NewUpsert("origin-a", "board", pk, 3, json.RawMessage(`{"title":"`+title+`"}`), at)
		if err != nil {
			t.Fatalf("NewUpsert: %v", err)
		}
		e.MAC = EntryMAC(key, e)
		return e
	}

	// Two writes of one row, so compaction has a superseded entry to drop and a
	// survivor to keep.
	if _, err := writer.Append([]Entry{signed(`["b"]`, "first")}); err != nil {
		t.Fatalf("append the first write: %v", err)
	}
	if _, err := writer.Append([]Entry{signed(`["b"]`, "second")}); err != nil {
		t.Fatalf("append the second write: %v", err)
	}

	pulled, err := reader.Pull(nil)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !VerifyMAC(key, pulled[len(pulled)-1]) {
		t.Error("a pull handed back an entry whose MAC no longer matches its fields")
	}

	page, err := reader.PullRange("origin-a", 0, 2)
	if err != nil {
		t.Fatalf("PullRange: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("PullRange = %d entries, want 2", len(page))
	}
	for _, e := range page {
		if !VerifyMAC(key, e) {
			t.Errorf("PullRange handed back seq %d with a MAC that does not verify", e.Seq)
		}
	}

	// Compacting to the first write drops it as superseded and keeps the
	// second, which is the entry whose signature has to come back intact. Only
	// the origin that wrote the entries may compact them.
	if _, err := writer.CompactOwn(1); err != nil {
		t.Fatalf("CompactOwn: %v", err)
	}
	kept, err := reader.PullRange("origin-a", 1, 2)
	if err != nil {
		t.Fatalf("PullRange after compaction: %v", err)
	}
	if len(kept) != 1 || kept[0].Seq != 2 {
		t.Fatalf("after compaction the range holds %+v, want only seq 2", kept)
	}
	if !VerifyMAC(key, kept[0]) {
		t.Error("the entry compaction kept came back with a MAC that does not verify")
	}
}

// TestMemTransportRecordsWhereEachOriginStartedSigning pins the marker the
// worker writes on the remote: the first sequence of the batch that carried a
// MAC, and no later one.
// TestMemTransportAnswersAbsentBeforeAKeyIsSet pins what the fake log says about
// its own passphrase material, so the exporter is written against the same
// answer the remote gives rather than against a guess.
func TestMemTransportAnswersAbsentBeforeAKeyIsSet(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("origin-a")
	other := log.OnLog("origin-b")

	key, err := other.KeyMeta(context.Background())
	if err != nil {
		t.Fatalf("KeyMeta on a log with no key: %v", err)
	}
	if key.Present || key.Salt != nil || key.Check != nil {
		t.Errorf("key = %+v, want absent: nothing has written one", key)
	}

	mine := KeyMeta{Salt: []byte("this machine's salt"), KDF: "argon2id", Check: []byte("its check")}
	won, err := other.KeyInit(context.Background(), mine)
	if err != nil {
		t.Fatalf("the first KeyInit: %v", err)
	}
	if !won.Present || string(won.Salt) != string(mine.Salt) || won.KDF != "argon2id" {
		t.Fatalf("the first KeyInit answered %+v, want the material it was given", won)
	}

	// The losing half of a race is answered with what the log holds, and both
	// handles agree on it -- which is what lets each machine find out that its
	// own check does not match.
	lost, err := log.KeyInit(context.Background(), KeyMeta{
		Salt: []byte("a different salt"), KDF: "argon2id", Check: []byte("a different check"),
	})
	if err != nil {
		t.Fatalf("the second KeyInit: %v", err)
	}
	if string(lost.Salt) != string(mine.Salt) {
		t.Errorf("the second KeyInit answered the salt %q, want %q", lost.Salt, mine.Salt)
	}
	reread, err := other.KeyMeta(context.Background())
	if err != nil {
		t.Fatalf("KeyMeta after the race: %v", err)
	}
	if string(reread.Salt) != string(mine.Salt) || string(reread.Check) != string(mine.Check) {
		t.Errorf("the log now holds %+v, want the first machine's material", reread)
	}
}

// TestMemTransportRecordsWhereEachOriginStartedSigning pins that the memfake
// writes the same marker the worker does, so a reader's rules are written against
// the fake and hold on the remote.
func TestMemTransportRecordsWhereEachOriginStartedSigning(t *testing.T) {
	t.Parallel()
	writer := NewMemTransport("origin-a")
	reader := writer.OnLog("origin-b")
	now := time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)

	if _, err := writer.Append([]Entry{
		{Origin: "origin-a", Table: "board", PK: `["u"]`, Op: OpUpsert,
			SchemaVersion: 3, Body: json.RawMessage(`{"title":"unsigned"}`), At: now},
	}); err != nil {
		t.Fatalf("append the unsigned batch: %v", err)
	}
	state, err := reader.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if state.SignedFrom != nil {
		t.Errorf("signed_from = %v, want none: nothing has been signed yet", state.SignedFrom)
	}

	for _, pk := range []string{`["s"]`, `["t"]`} {
		if _, err := writer.Append([]Entry{{
			Origin: "origin-a", Table: "board", PK: pk, Op: OpUpsert, SchemaVersion: 3,
			Body: json.RawMessage(`{"title":"signed"}`), At: now, MAC: []byte("a signature"),
		}}); err != nil {
			t.Fatalf("append a signed batch: %v", err)
		}
	}
	state, err = reader.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if got := state.SignedFrom["origin-a"]; got != 2 {
		t.Errorf("signed_from[origin-a] = %d, want 2: the first signed sequence", got)
	}
}
