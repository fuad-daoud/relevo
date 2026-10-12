package syncworker

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The signature's round trip over the pipe and through the log, against a local
// replica. The scratch remote covers the same path against a real engine, but
// it is skipped unless a scratch remote is configured, so this is what actually
// runs on CI: if the encoding of the MAC or the body were dropped anywhere
// between the export and the pull, it is here that it shows up.

func TestSignedEntryVerifiesAfterThePipeAndTheLog(t *testing.T) {
	path := tempReplica(t)
	// Two handles on one replica: an append is only readable by another origin,
	// since a machine's own writes are already in its file.
	writer := backendFor(t, &localDriver{path: path}, "origin-a")
	reader := backendFor(t, &localDriver{path: path}, "origin-b")
	key := []byte("the passphrase this machine stretched")

	// A body written with whitespace and columns in an order of its own, at a
	// time carrying a zone and a nanosecond. None of that survives storage, and
	// the signature has to survive it anyway.
	entry, err := synclog.NewUpsert("origin-a", "board", `["m"]`, 3,
		json.RawMessage("{\n  \"title\": \"signed\",\n  \"n\": 1\n}"),
		time.Date(2026, 10, 8, 9, 30, 0, 123456789, time.FixedZone("an hour out", 3600)))
	if err != nil {
		t.Fatalf("build the entry: %v", err)
	}
	entry.MAC = synclog.EntryMAC(key, entry)

	line, err := json.Marshal([]Entry{entryToWire(entry)})
	if err != nil {
		t.Fatalf("marshal the entry: %v", err)
	}
	var onWire []Entry
	if err := json.Unmarshal(line, &onWire); err != nil {
		t.Fatalf("decode the entry: %v", err)
	}
	written, err := writer.Append(onWire)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("append = %d entries, want 1", len(written))
	}

	// The stored bytes are the log's, not the exporter's: the body has been
	// compacted by the pipe and the time is UTC. If the signature had been made
	// over the exporter's own bytes it could not verify here.
	stored, err := reader.Pull(nil)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("pull = %d entries, want 1", len(stored))
	}
	back := entryFromWire(stored[0])
	if !synclog.VerifyMAC(key, back) {
		t.Fatalf("the entry that came back does not verify: %+v", back)
	}
	if back.At.Location() != time.UTC {
		t.Errorf("the entry came back with the time in %v, want UTC: the log stores one string", back.At.Location())
	}
}

// TestAnUnsignedEntryIsStoredWithoutAMAC pins that an entry written before this
// machine had a key leaves a NULL in the column, which is what a reader takes
// as unsigned. An empty blob would be a different answer: it looks like a
// signature of nothing rather than the absence of one.
func TestAnUnsignedEntryIsStoredWithoutAMAC(t *testing.T) {
	path := tempReplica(t)
	writer := backendFor(t, &localDriver{path: path}, "origin-a")
	reader := backendFor(t, &localDriver{path: path}, "origin-b")

	written, err := writer.Append([]Entry{upsertEntry("origin-a", "board", `["u"]`, `{"title":"unsigned"}`)})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("append = %d entries, want 1", len(written))
	}
	if mac, present := logMAC(t, path, written[0].Seq); present {
		t.Errorf("the row holds a MAC of %x, want NULL", mac)
	}
	pulled, err := reader.Pull(nil)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pulled[0].MAC != nil {
		t.Errorf("the pull handed back a MAC of %x, want none", pulled[0].MAC)
	}
}

// TestPullRangeHandsBackTheMAC pins that the read the snapshot path uses carries
// the signature too. It goes through its own statement from Pull, and a column
// left out of it would silently un-sign every entry the snapshot reads.
func TestPullRangeHandsBackTheMAC(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	key := []byte("the passphrase this machine stretched")

	e := upsertEntry("origin-a", "board", `["r"]`, `{"title":"ranged"}`)
	e.MAC = synclog.EntryMAC(key, entryFromWire(e))
	if _, err := b.Append([]Entry{e}); err != nil {
		t.Fatalf("append: %v", err)
	}
	paged, err := b.PullRange("origin-a", 0, 10)
	if err != nil {
		t.Fatalf("PullRange: %v", err)
	}
	if len(paged) != 1 {
		t.Fatalf("PullRange = %d entries, want 1", len(paged))
	}
	if !synclog.VerifyMAC(key, entryFromWire(paged[0])) {
		t.Error("the entry a pull_range read does not verify: the statement left the MAC behind")
	}
}
