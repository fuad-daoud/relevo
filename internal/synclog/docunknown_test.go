package synclog

import (
	"strings"
	"testing"
	"time"
)

// rowAndFutureDocLog is a log holding one signed batch from m1: a row and an
// entry for the document "future".
func rowAndFutureDocLog(t *testing.T) (*MemTransport, *txDoc) {
	t.Helper()
	theirs, theirPath := exporterFile(t, "m1")
	seed(t, theirPath, insertBinding("01A", "m1"))
	scratch := NewMemTransport("scratch")
	if _, err := NewExporter(theirs, scratch.OnLog("m1")).Export(); err != nil {
		t.Fatalf("export the row: %v", err)
	}
	row, err := scratch.Pull(nil)
	if err != nil || len(row) != 1 {
		t.Fatalf("pull the row = %d entries, %v", len(row), err)
	}
	future := newTxDoc("future")
	docEntry, err := NewDocUpsert("m1", future, "k", row[0].SchemaVersion, 5,
		[]byte(`{"a":1}`), time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewDocUpsert: %v", err)
	}
	log := NewMemTransport("m1")
	writer := keyed{key: keyA}
	batch := []Entry{row[0], docEntry}
	batch[0].Seq, batch[0].Batch = 0, 0
	writer.sign(batch)
	if _, err := writer.appendTo(log.OnLog("m1"), "m1", batch); err != nil {
		t.Fatalf("append: %v", err)
	}
	return log, future
}

// TestUnknownDocNameHoldsTheOriginAndKeepsItsRows pins that a batch carrying a
// row and an entry for a document this build has no reader for holds its
// origin, locked or unlocked: neither the row nor the mark moves. Once the
// document is bound, the next import applies both.
func TestUnknownDocNameHoldsTheOriginAndKeepsItsRows(t *testing.T) {
	t.Parallel()
	log, future := rowAndFutureDocLog(t)
	known := newTxDoc("config")

	for name, key := range map[string][]byte{"unlocked": keyA, "locked": nil} {
		peer, _ := peerFile(t, "m2")
		res, err := docImporter(peer, log, key, known).Import()
		if err != nil {
			t.Fatalf("%s import: %v", name, err)
		}
		if len(res.Held) != 1 || !strings.Contains(res.Held[0].String(), `"future"`) ||
			!strings.Contains(res.Held[0].String(), "upgrade") {
			t.Errorf("%s: held = %v, want the origin held with the document named", name, res.Held)
		}
		if len(res.Dropped) != 0 {
			t.Errorf("%s: dropped = %v, want nothing dropped", name, res.Dropped)
		}
		if got := markOf(t, peer, "m1"); got != 0 {
			t.Errorf("%s: mark = %d, want it unmoved", name, got)
		}
		if got := sharedRows(t, peer); got != "" {
			t.Errorf("%s: peer holds %q, want no row from a held batch", name, got)
		}
	}

	peer, _ := peerFile(t, "m2")
	if _, err := docImporter(peer, log, keyA, known).Import(); err != nil {
		t.Fatal(err)
	}
	res, err := docImporter(peer, log, keyA, known, future).Import()
	if err != nil || len(res.Held) != 0 {
		t.Fatalf("import with the doc bound = %+v, %v", res, err)
	}
	if got := markOf(t, peer, "m1"); got != 2 {
		t.Errorf("mark = %d, want both entries applied", got)
	}
	if got := sharedRows(t, peer); !strings.Contains(got, "01A") {
		t.Errorf("peer holds %q, want the row", got)
	}
	if len(future.revisions) != 1 {
		t.Errorf("revisions %v, want the future entry applied", future.revisions)
	}
}

// TestUnknownDocNameHoldsTheOriginAtItsSnapshot pins the same hold on the path
// that brings an origin up to its compaction floor.
func TestUnknownDocNameHoldsTheOriginAtItsSnapshot(t *testing.T) {
	t.Parallel()
	log, _ := rowAndFutureDocLog(t)
	peer, _ := peerFile(t, "m2")
	imp := docImporter(peer, log, keyA, newTxDoc("config"))

	var total ImportResult
	hold, err := imp.snapshotOrigin("m1", 0, 2, 2, map[string]int{}, &total)
	if err != nil {
		t.Fatalf("snapshotOrigin: %v", err)
	}
	if hold == nil || !strings.Contains(hold.String(), `"future"`) {
		t.Fatalf("hold = %v, want the origin held with the document named", hold)
	}
	if got := markOf(t, peer, "m1"); got != 0 {
		t.Errorf("mark = %d, want it unmoved", got)
	}
	if len(total.Dropped) != 0 || sharedRows(t, peer) != "" {
		t.Errorf("dropped %v, rows %q: a held region applies nothing", total.Dropped, sharedRows(t, peer))
	}
}
