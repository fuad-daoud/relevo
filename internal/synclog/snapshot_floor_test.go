package synclog

import (
	"strings"
	"testing"
)

// forgeFloor writes a floor straight into the log's meta, as a machine that
// lies about its compaction would.
func forgeFloor(log *MemTransport, origin string, floor int) {
	log.log.mu.Lock()
	defer log.log.mu.Unlock()
	if log.log.floors == nil {
		log.log.floors = make(map[string]int)
	}
	log.log.floors[origin] = floor
}

// importStalledOn runs one import on reader and requires it to stall origin
// with a reason naming the floor, the mark left where it was.
func importStalledOn(t *testing.T, log *MemTransport, origin, reason string) {
	t.Helper()
	reader, _ := peerFile(t, "m2")
	res, err := NewImporter(reader, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Stalled) != 1 || res.Stalled[0].Origin != origin || !strings.Contains(res.Stalled[0].Reason, reason) {
		t.Fatalf("import = %+v, want %s stalled because its floor %s", res, origin, reason)
	}
	if got := markOf(t, reader, origin); got != 0 {
		t.Fatalf("mark = %d, want it left at 0", got)
	}
	if said := res.Stalled[0].String(); strings.Contains(said, "fetched") {
		t.Errorf("stall reads %q, want no word of a fetch for a floor", said)
	}
}

// A floor past the origin's head names entries the log never held. Taking it
// would set the mark past every entry the origin writes next, and they would
// never be pulled.
func TestForgedFloorPastTheHeadStalls(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	appendOne(t, log.OnLog("m1"), "binding", `["a"]`)
	forgeFloor(log, "m1", 1_000_000_000)
	importStalledOn(t, log, "m1", "past the log's head")
}

// Compaction never drops the entry at the floor, so a floor whose entry is not
// there is not one any compaction wrote.
func TestFloorNamingNoEntryStalls(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	m1 := log.OnLog("m1")
	appendOne(t, m1, "binding", `["a"]`)
	appendOne(t, m1, "binding", `["a"]`)
	appendOne(t, m1, "binding", `["b"]`)
	compactTo(t, log, "m1", 2, 1)
	forgeFloor(log, "m1", 1)
	importStalledOn(t, log, "m1", "names no entry")
}

// A reader applies whole batches and its mark lands on a batch's last entry. A
// floor inside a batch would leave the rest of that batch behind the mark,
// where the normal path skips it as already applied.
func TestFloorInsideABatchStalls(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	if _, err := log.OnLog("m1").Append([]Entry{
		{Table: "binding", PK: `["a"]`, Op: OpUpsert, Body: []byte(`{"id":"a"}`), SchemaVersion: 1},
		{Table: "binding", PK: `["b"]`, Op: OpUpsert, Body: []byte(`{"id":"b"}`), SchemaVersion: 1},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	forgeFloor(log, "m1", 1)
	importStalledOn(t, log, "m1", "inside a batch")
}

// A region past its row bound is a stall rather than a map that grows until the
// daemon runs out of memory.
func TestSnapshotRegionPastItsBoundStalls(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	m1 := log.OnLog("m1")
	appendOne(t, m1, "binding", `["a"]`)
	appendOne(t, m1, "binding", `["b"]`)
	compactTo(t, log, "m1", 2, 0)
	reader, _ := peerFile(t, "m2")
	imp := NewImporter(reader, log.OnLog("m2"))
	imp.regionRows = 1
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Stalled) != 1 || !strings.Contains(res.Stalled[0].Reason, "more than") {
		t.Fatalf("import = %+v, want the region refused for its size", res)
	}
}

// A fork whose parent was rewritten after it needs a second pass. The passes
// are bounded, so a region built to need one per row drops what is left instead
// of costing a pass over the whole region for every row.
func TestSnapshotPassesAreBounded(t *testing.T) {
	t.Parallel()
	m1, m1Path := exporterFile(t, "m1")
	log := NewMemTransport("m1")
	seed(t, m1Path, insertBinding("01P", "m1"))
	exportAs(t, m1, log)
	seed(t, m1Path, insertForkedBinding("01C", "01P", "m1"))
	exportAs(t, m1, log)
	seed(t, m1Path, `UPDATE binding SET name = 'renamed' WHERE id = '01P'`)
	exportAs(t, m1, log)
	head := headOf(t, log, "m1")
	compactTo(t, log, "m1", head, 1)

	for _, tc := range []struct {
		passes  int
		dropped bool
	}{{1, true}, {2, false}} {
		reader, _ := peerFile(t, "m2")
		imp := NewImporter(reader, log.OnLog("m2"))
		imp.passes = tc.passes
		res, err := imp.Import()
		if err != nil {
			t.Fatalf("import with %d passes: %v", tc.passes, err)
		}
		if dropped := len(res.Dropped) == 1; dropped != tc.dropped || len(res.Stalled) != 0 {
			t.Errorf("import with %d passes = %+v, want dropped %t and nothing stalled", tc.passes, res, tc.dropped)
		}
	}
}
