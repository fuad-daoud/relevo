package synclog

import (
	"context"
	"errors"
	"testing"
)

func headOf(t *testing.T, log *MemTransport, origin string) int {
	t.Helper()
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("read marks: %v", err)
	}
	return state.Heads[origin]
}

func TestMemCompactOwnReportsTheRecordedFloor(t *testing.T) {
	t.Parallel()
	m := NewMemTransport("o")
	appendOne(t, m, "binding", `["a"]`)
	appendOne(t, m, "binding", `["a"]`)
	res, err := m.CompactOwn(2)
	if err != nil || res.Dropped != 1 || res.Floor != 2 {
		t.Fatalf("CompactOwn = %+v, %v; want one dropped at floor 2", res, err)
	}
	if _, err := m.CompactOwn(9); err == nil {
		t.Fatal("CompactOwn past the head succeeded")
	}
}

// An entry a newer binary wrote holds the origin where it stands, the way a
// batch does, and the mark does not move.
func TestSnapshotHoldsAtAnEntryFromANewerWriter(t *testing.T) {
	t.Parallel()
	m2, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")
	h := log.OnLog("m1")
	if _, err := h.Append([]Entry{{Table: "binding", PK: `["x"]`, Op: OpUpsert, Body: []byte(`{"id":"x"}`), SchemaVersion: 9999}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	compactTo(t, log, "m1", 1, 0)
	res, err := NewImporter(m2, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Held) != 1 || res.Held[0].Origin != "m1" || res.Snapshots != 0 {
		t.Fatalf("import = %+v, want the origin held", res)
	}
	if got := markOf(t, m2, "m1"); got != 0 {
		t.Fatalf("mark = %d, want it left at 0", got)
	}
}

// A table this build does not share is reported and dropped; the rest of the
// region still applies and the mark reaches the floor.
func TestSnapshotDropsAnEntryForAnUnsharedTable(t *testing.T) {
	t.Parallel()
	m1, m1Path := exporterFile(t, "m1")
	m2, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")
	seed(t, m1Path, insertBinding("01A", "m1"))
	exportAs(t, m1, log)
	if _, err := log.OnLog("m1").Append([]Entry{{Table: "nope", PK: `["x"]`, Op: OpUpsert, Body: []byte(`{"id":"x"}`), SchemaVersion: 1}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	compactTo(t, log, "m1", headOf(t, log, "m1"), 0)
	res, err := NewImporter(m2, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Dropped) != 1 || res.Snapshots != 1 {
		t.Fatalf("import = %+v, want the one entry dropped and the region applied", res)
	}
	if got := sharedRows(t, m2); got != `binding ["01A"]` {
		t.Fatalf("m2 holds %q", got)
	}
}

// An entry for a row another installation owns is refused and dropped, and the
// rest of its chunk is written without it.
func TestSnapshotDropsAnEntryForARowAnotherInstallationOwns(t *testing.T) {
	t.Parallel()
	m1, m1Path := exporterFile(t, "m1")
	m2, m2Path := peerFile(t, "m2")
	log := NewMemTransport("m1")
	seed(t, m1Path, insertBinding("01Q", "m1"), insertBinding("01Z", "m1"))
	exportAs(t, m1, log)
	seed(t, m2Path, insertBinding("01Z", "m2"))
	compactTo(t, log, "m1", headOf(t, log, "m1"), 0)

	res, err := NewImporter(m2, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Dropped) != 1 {
		t.Fatalf("import = %+v, want the foreign-owned row dropped", res)
	}
	if got := sharedRows(t, m2); got != `binding ["01Q"]|binding ["01Z"]` {
		t.Fatalf("m2 holds %q, want the applied row and its own", got)
	}
	if got := columnOf(t, m2, `SELECT origin FROM binding WHERE id = '01Z'`); got != "m2" {
		t.Fatalf("01Z is owned by %q, want m2 to keep it", got)
	}
}

// A region whose references can never be satisfied stalls the origin and leaves
// the mark where it was, rather than looping or applying a row without its parent.
func TestSnapshotStallsWhenNoPassCanApplyTheRest(t *testing.T) {
	t.Parallel()
	m1, m1Path := exporterFile(t, "m1")
	m2, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")
	seed(t, m1Path, insertBinding("01P", "m1"))
	exportAs(t, m1, log)
	seed(t, m1Path, insertForkedBinding("01C", "01P", "m1"))
	exportAs(t, m1, log)
	log.log.mu.Lock()
	kept := log.log.entries[:0]
	for _, e := range log.log.entries {
		if e.PK != `["01P"]` {
			kept = append(kept, e)
		}
	}
	log.log.entries = kept
	log.log.mu.Unlock()
	compactTo(t, log, "m1", 2, 0)

	res, err := NewImporter(m2, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Stalled) != 1 || res.Snapshots != 0 {
		t.Fatalf("import = %+v, want the origin stalled", res)
	}
	if got := markOf(t, m2, "m1"); got != 0 {
		t.Fatalf("mark = %d, want it left at 0", got)
	}
}

type failingRange struct{ *MemTransport }

func (failingRange) PullRange(string, int, int) ([]Entry, error) {
	return nil, errors.New("the range read failed")
}

func TestSnapshotRangeReadFailureEndsTheImport(t *testing.T) {
	t.Parallel()
	m1, m1Path := exporterFile(t, "m1")
	m2, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")
	seed(t, m1Path, insertBinding("01A", "m1"))
	exportAs(t, m1, log)
	compactTo(t, log, "m1", 1, 0)
	if _, err := NewImporter(m2, failingRange{log.OnLog("m2")}).Import(); err == nil {
		t.Fatal("import succeeded with a range read that failed")
	}
}

// A body missing from the store is the same hard stop on the snapshot path as on
// the batch path, naming the file.
func TestSnapshotMissingObjectEndsTheImportNamingTheFile(t *testing.T) {
	t.Parallel()
	p := newBlobPair()
	a, aPath := exporterFile(t, "A")
	seedBodies(t, aPath)
	exportFrom(t, a, p.link(t, "A"))
	compactTo(t, p.log, "A", headOf(t, p.log, "A"), 0)
	objects, _ := p.mover.Store().List(context.Background(), "")
	for _, o := range objects {
		_ = p.mover.Store().Delete(context.Background(), o.Key)
	}
	c, _ := exporterFile(t, "C")
	if _, err := NewImporter(c, p.link(t, "C")).Import(); !errors.Is(err, ErrBlobMissing) {
		t.Fatalf("import = %v, want ErrBlobMissing", err)
	}
}
