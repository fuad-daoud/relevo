package synclog

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

func exportAs(t *testing.T, d *db.DB, log *MemTransport) {
	t.Helper()
	if _, err := NewExporter(d, log.OnLog(d.Origin())).Export(); err != nil {
		t.Fatalf("export from %s: %v", d.Origin(), err)
	}
}

func compactTo(t *testing.T, log *MemTransport, origin string, floor, wantDropped int) {
	t.Helper()
	dropped, err := log.Compact(origin, floor)
	if err != nil || dropped != wantDropped {
		t.Fatalf("compact %s to %d = %d, %v; want %d dropped", origin, floor, dropped, err, wantDropped)
	}
}

func markOf(t *testing.T, d *db.DB, origin string) int {
	t.Helper()
	marks, err := d.ImportMarks()
	if err != nil {
		t.Fatalf("read marks: %v", err)
	}
	return marks[origin]
}

// A fork's entry can sit before its parent's once the parent was rewritten and
// the parent's old entry compacted away, so the parent has the higher sequence.
func TestSnapshotImportFromZeroAfterCompaction(t *testing.T) {
	t.Parallel()
	m1, m1Path := exporterFile(t, "m1")
	m2, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")

	seed(t, m1Path, insertBinding("01P", "m1"))
	exportAs(t, m1, log)
	seed(t, m1Path, insertForkedBinding("01C", "01P", "m1"))
	exportAs(t, m1, log)
	seed(t, m1Path, `UPDATE binding SET cwd = '/y' WHERE id = '01P'`)
	exportAs(t, m1, log)
	compactTo(t, log, "m1", 3, 1)

	res, err := NewImporter(m2, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import from zero: %v", err)
	}
	if res.Snapshots != 1 || len(res.Stalled) != 0 {
		t.Fatalf("import = %+v, want one snapshot and no stall", res)
	}
	if got := sharedRows(t, m2); got != `binding ["01C"]|binding ["01P"]` {
		t.Fatalf("m2 holds %q, want the fork and its parent", got)
	}
	if got := markOf(t, m2, "m1"); got != 3 {
		t.Fatalf("mark = %d, want the floor 3", got)
	}
	assertReferencesClean(t, m2)
}

// The reader held a row the origin then deleted below the new floor, and the
// entries between its mark and the floor were compacted, so the live log has a
// hole right after the mark.
func TestStaleReaderBelowFloorConverges(t *testing.T) {
	t.Parallel()
	m1, m1Path := exporterFile(t, "m1")
	m2, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")

	seed(t, m1Path, insertBinding("01R", "m1"))
	exportAs(t, m1, log)
	if _, err := NewImporter(m2, log.OnLog("m2")).Import(); err != nil {
		t.Fatalf("first import: %v", err)
	}
	seed(t, m1Path, insertBinding("01Q", "m1"))
	exportAs(t, m1, log)
	seed(t, m1Path, `UPDATE binding SET cwd = '/q2' WHERE id = '01Q'`)
	exportAs(t, m1, log)
	seed(t, m1Path, `DELETE FROM binding WHERE id = '01R'`)
	exportAs(t, m1, log)
	seed(t, m1Path, insertBinding("01S", "m1"))
	exportAs(t, m1, log)
	compactTo(t, log, "m1", 5, 2)

	res, err := NewImporter(m2, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("stale import: %v", err)
	}
	if len(res.Gaps) != 0 || res.Snapshots != 1 {
		t.Fatalf("import = %+v, want a snapshot and no gap", res)
	}
	if got := sharedRows(t, m2); got != `binding ["01Q"]|binding ["01S"]` {
		t.Fatalf("m2 holds %q, want the deleted row gone", got)
	}
	assertConverged(t, m1, m2, `binding ["01Q"]|binding ["01S"]`)
}

// A failure in the middle of the region leaves the mark where it was, and the
// rerun applies the region again and converges.
func TestSnapshotMarkMovesOnlyAtTheEnd(t *testing.T) {
	t.Parallel()
	p := newBlobPair()
	a, aPath := exporterFile(t, "A")
	b, _ := exporterFile(t, "B")
	seedBodies(t, aPath)
	exportFrom(t, a, p.link(t, "A"))
	head, err := p.log.Head("A")
	if err != nil || len(head) == 0 {
		t.Fatalf("head = %v, %v", head, err)
	}
	stats, _ := p.log.Stats()
	compactTo(t, p.log, "A", stats.Seq, 0)

	p.mover.GetErr = errors.New("the bucket is unreachable")
	imp := NewImporter(b, p.link(t, "B"))
	imp.chunk = 1
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import with the bucket down: %v", err)
	}
	if len(res.Stalled) != 1 {
		t.Fatalf("import = %+v, want the origin stalled", res)
	}
	if got := markOf(t, b, "A"); got != 0 {
		t.Fatalf("mark = %d after a failed region, want it still 0", got)
	}
	if sharedRows(t, b) == "" {
		t.Fatal("no chunk was applied before the failure, want the test to fail mid-region")
	}

	p.mover.GetErr = nil
	if _, err := NewImporter(b, p.link(t, "B")).Import(); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if got := markOf(t, b, "A"); got != stats.Seq {
		t.Fatalf("mark = %d after the rerun, want the floor %d", got, stats.Seq)
	}
	want, got := storedColumns(t, a), storedColumns(t, b)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s differs on B after the rerun", k)
		}
	}
	assertConverged(t, a, b, sharedRows(t, a))
}

// The snapshot path sets a refused upsert aside and keeps writing in the same
// transaction instead of taking a savepoint per statement. That holds only while
// the engine leaves a transaction open after a statement a foreign key refused.
func TestRefusedStatementLeavesTheTransactionOpen(t *testing.T) {
	t.Parallel()
	_, path := peerFile(t, "m2")
	raw, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = raw.Close() }()
	ctx := context.Background()
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := conn.ExecContext(ctx, insertForkedBinding("01C", "01P", "m1")); err == nil {
		t.Fatal("a fork of a missing parent was accepted, want the foreign key to refuse it")
	}
	if _, err := conn.ExecContext(ctx, insertBinding("01P", "m1")); err != nil {
		t.Fatalf("a statement after the refused one: %v", err)
	}
	if _, err := conn.ExecContext(ctx, insertForkedBinding("01C", "01P", "m1")); err != nil {
		t.Fatalf("the retried fork: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM binding").Scan(&n); err != nil || n != 2 {
		t.Fatalf("bindings = %d, %v; want both committed", n, err)
	}
}

// A hand-over refuses a target whose newest schema it has not seen, so an
// origin read from a snapshot has its schema noted like one read entry by
// entry: a quiet machine's log is the one compaction shortens first.
func TestASnapshotNotesTheOriginSchema(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, theirPath, insertBinding("01A", "m1"))
	log := NewMemTransport("m1")
	if _, err := exporter(theirs, &recording{MemTransport: log}).Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	seed(t, theirPath, `UPDATE binding SET name = 'renamed' WHERE id = '01A'`)
	if _, err := exporter(theirs, &recording{MemTransport: log}).Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	compactTo(t, log, "m1", 2, 1)
	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil || got.Snapshots != 1 {
		t.Fatalf("import = %+v, %v; want one snapshot", got, err)
	}
	have, _ := theirs.SchemaVersions()
	if schemas, err := peer.OriginSchemas(); err != nil || schemas["m1"] != have {
		t.Fatalf("OriginSchemas = (%v, %v), want m1 at %d", schemas, err, have)
	}
}
