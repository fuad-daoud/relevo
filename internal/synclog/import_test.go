package synclog

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The importer's contract is what one machine ends up holding after another
// machine's batch is applied: the rows the batch carried, in the order that
// order was written, with the mark that covers them moved and the rows the batch
// did not carry left alone.
//
// Every case runs against two real files. The foreign keys are on on both, so a
// batch that applied out of order would be refused by the engine rather than
// quietly landing a row whose parent is missing -- the failure this importer has
// to be ordered to avoid.

// peerFile is the machine an import writes into: a second installation's own
// file, opened the way that machine opens it, with foreign keys asserted on.
func peerFile(t *testing.T, origin string) (*db.DB, string) {
	t.Helper()
	d, path := exporterFile(t, origin)
	assertForeignKeys(t, path)
	return d, path
}

// assertForeignKeys proves the import runs on a connection that enforces the
// references a shared row carries. Without the guard an import out of order
// would pass by writing an orphan, and the ordering below would be untested.
func assertForeignKeys(t *testing.T, path string) {
	t.Helper()
	raw, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("open the file for reading its settings: %v", err)
	}
	defer func() { _ = raw.Close() }()

	var on int
	if err := raw.QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if on != 1 {
		t.Fatalf("foreign_keys is %d; the import order needs it on", on)
	}
}

// sharedRows is the file's rows of every shared table as "table key", joined, so
// a case can say what the peer holds rather than counting one table at a time.
// The key is what the outbox records for the row, which is the shape an entry
// names it by.
func sharedRows(t *testing.T, d *db.DB) string {
	t.Helper()
	raw, err := db.OpenRawReadOnly(d.Path())
	if err != nil {
		t.Fatalf("open the file for reading: %v", err)
	}
	defer func() { _ = raw.Close() }()

	var out []string
	for _, tbl := range SharedTablesInOrder() {
		shared, ok := SharedTable(tbl)
		if !ok {
			t.Fatalf("shared table %s is not in the shared list", tbl)
		}
		cols := make([]string, len(shared.PrimaryKey))
		for i, col := range shared.PrimaryKey {
			cols[i] = `"` + col + `"`
		}
		query := fmt.Sprintf(`SELECT json_array(%s) FROM %q ORDER BY %s`,
			strings.Join(cols, ", "), tbl, strings.Join(cols, ", "))
		rows, err := raw.Query(query)
		if err != nil {
			t.Fatalf("read %s: %v", tbl, err)
		}
		for rows.Next() {
			var pk string
			if err := rows.Scan(&pk); err != nil {
				_ = rows.Close()
				t.Fatalf("read %s: %v", tbl, err)
			}
			out = append(out, tbl+" "+pk)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatalf("read %s: %v", tbl, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("read %s: %v", tbl, err)
		}
	}
	return strings.Join(out, "|")
}

// columnOf reads one column of one row, so a case can assert what a body wrote
// rather than only that the row exists.
func columnOf(t *testing.T, d *db.DB, query string, args ...any) string {
	t.Helper()
	raw, err := db.OpenRawReadOnly(d.Path())
	if err != nil {
		t.Fatalf("open the file for reading: %v", err)
	}
	defer func() { _ = raw.Close() }()

	var value string
	if err := raw.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("read %q: %v", query, err)
	}
	return value
}

// importFrom exports the named installation's own outbox into the log the peer
// reads, and applies whatever the peer has not yet applied. The log is the
// remote both machines reach, so the entries the importer sees are entries the
// transport really numbered.
func importFrom(t *testing.T, peer *db.DB, log *MemTransport, from *db.DB, writer *recording) ImportResult {
	t.Helper()
	if _, err := exporter(from, writer).Export(); err != nil {
		t.Fatalf("export %s's own rows: %v", from.Origin(), err)
	}
	got, err := NewImporter(peer, log.OnLog(peer.Origin())).Import()
	if err != nil {
		t.Fatalf("import into %s: %v", peer.Origin(), err)
	}
	return got
}

// One machine's rows land on the other machine's file, parent and child alike,
// and the peer holds no row the export did not carry. The batch is mixed -- a
// binding, its round, and a second binding the round was re-pointed at -- so the
// order it applies in is the order the foreign keys demand, not a convenient
// one.
func TestTwoMachinesShareOneRecord(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, theirPath,
		insertBinding("01A", "m1"),
		insertRound("02A", "01A"),
		insertBinding("01B", "m1"),
		`UPDATE round SET binding_id = '01B' WHERE id = '02A'`,
	)

	log := NewMemTransport("m1")
	writer := &recording{MemTransport: log}
	got := importFrom(t, peer, log, theirs, writer)
	if got.Batches != 1 || got.Applied != 3 {
		t.Fatalf("import = %+v, want one batch of three entries applied", got)
	}

	want := sharedRows(t, theirs)
	if have := sharedRows(t, peer); have != want {
		t.Fatalf("peer holds %q, want the exporter's %q", have, want)
	}
	if label := columnOf(t, peer, `SELECT binding_id FROM round WHERE id = '02A'`); label != "01B" {
		t.Fatalf("the peer's round points at binding %q, want the re-pointed parent", label)
	}
	if seq, found, err := peer.ImportMark("m1"); err != nil || !found || seq != 3 {
		t.Fatalf("ImportMark = (%d, %t, %v), want the batch's last sequence applied", seq, found, err)
	}
}

// A root removed on one machine is gone on the other, children included. The
// children carry ON DELETE CASCADE, so the peer loses them with the root rather
// than being left holding rows whose parent is not there; the mark moves so the
// removal is not re-read.
func TestImporterRootDeleteCascades(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, theirPath,
		insertRecord("02A", "m1"),
		insertRecordEvent("02A", 1),
		insertRoundFile("02A", "f", 1),
	)

	log := NewMemTransport("m1")
	writer := &recording{MemTransport: log}
	importFrom(t, peer, log, theirs, writer)
	if have := sharedRows(t, peer); have != `binding_record ["02A"]|binding_event ["02A",1]|round_file ["02A","f"]` {
		t.Fatalf("the peer holds %q, want the record and both children", have)
	}

	seed(t, theirPath, `DELETE FROM binding_record WHERE id = '02A'`)
	removals := &recording{MemTransport: log}
	got := importFrom(t, peer, log, theirs, removals)
	if got.Batches != 1 || got.Applied != 1 {
		t.Fatalf("import = %+v, want one batch of the root's removal", got)
	}
	if shape := removals.shape(); shape != `binding_record ["02A"] delete` {
		t.Fatalf("the exporter appended %q, want one delete of the root", shape)
	}
	if have := sharedRows(t, peer); have != "" {
		t.Fatalf("the peer still holds %q, want the record and its cascaded children gone", have)
	}
	if seq, _, err := peer.ImportMark("m1"); err != nil || seq != 4 {
		t.Fatalf("ImportMark = %d (%v), want the removal's sequence applied", seq, err)
	}
}

// A single child removed is that child and nothing else. The parent's other
// rows and the record itself stay, because a delete names one row by its key
// and the cascade runs from the peer down, not from the delete outward.
func TestImporterChildOnlyDelete(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, theirPath,
		insertRecord("02A", "m1"),
		insertRoundFile("02A", "keep", 1),
		insertRoundFile("02A", "drop", 1),
	)

	log := NewMemTransport("m1")
	importFrom(t, peer, log, theirs, &recording{MemTransport: log})

	seed(t, theirPath, `DELETE FROM round_file WHERE record_id = '02A' AND name = 'drop'`)
	removals := &recording{MemTransport: log}
	if got := importFrom(t, peer, log, theirs, removals); got.Batches != 1 || got.Applied != 1 {
		t.Fatalf("import = %+v, want one batch of the child's removal", got)
	}
	want := `binding_record ["02A"]|round_file ["02A","keep"]`
	if have := sharedRows(t, peer); have != want {
		t.Fatalf("the peer holds %q, want the record and the sibling kept", have)
	}
}

// A parent written again is updated in place, and the rows hanging off it are
// untouched. Two kinds of child are here on purpose, because a REPLACE fails
// them differently. The binding's round and event carry no cascade, and this
// engine's REPLACE against such a parent leaves them standing -- so a REPLACE
// would go unnoticed through them alone. The record's binding_event and
// round_file do carry ON DELETE CASCADE, so a REPLACE deletes the record first
// and takes both with it, which is the regression this test exists to catch.
func TestImporterParentUpdateKeepsChildren(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, theirPath,
		insertBinding("01A", "m1"),
		insertRound("02A", "01A"),
		insertEvent("03A", "01A", "02A", 1),
		insertRecord("04A", "m1"),
		insertRecordEvent("04A", 1),
		insertRoundFile("04A", "f", 1),
	)

	log := NewMemTransport("m1")
	importFrom(t, peer, log, theirs, &recording{MemTransport: log})
	before := sharedRows(t, peer)
	want := `binding_record ["04A"]|binding ["01A"]|binding_event ["04A",1]|round_file ["04A","f"]|round ["02A"]|event ["03A"]`
	if before != want {
		t.Fatalf("the peer holds %q, want both parents with all four children", before)
	}

	seed(t, theirPath,
		`UPDATE binding SET cwd = '/moved', tier = 'second' WHERE id = '01A'`,
		`UPDATE binding_record SET state = 'closed', cwd = '/moved' WHERE id = '04A'`,
	)
	updates := &recording{MemTransport: log}
	if got := importFrom(t, peer, log, theirs, updates); got.Batches != 1 || got.Applied != 2 {
		t.Fatalf("import = %+v, want one batch of both parents' new states", got)
	}
	if cwd := columnOf(t, peer, `SELECT cwd FROM binding WHERE id = '01A'`); cwd != "/moved" {
		t.Fatalf("the peer's binding cwd = %q, want the updated value", cwd)
	}
	if tier := columnOf(t, peer, `SELECT tier FROM binding WHERE id = '01A'`); tier != "second" {
		t.Fatalf("the peer's binding tier = %q, want the updated value", tier)
	}
	if state := columnOf(t, peer, `SELECT state FROM binding_record WHERE id = '04A'`); state != "closed" {
		t.Fatalf("the peer's record state = %q, want the updated value", state)
	}
	if have := sharedRows(t, peer); have != want {
		t.Fatalf("the peer holds %q, want both parents updated with every child intact", have)
	}
}

// A child re-pointed at a parent the same batch carries arrives after it. The
// binding is written second in the exporter's outbox and the round's update that
// names it is written third, so a batch ordered by first appearance would hand
// the importer a round whose binding it had not written, and the foreign key
// would refuse it.
func TestImporterAppliesARepointedChildAfterItsNewParent(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, theirPath,
		insertBinding("01A", "m1"),
		insertRound("02A", "01A"),
		insertBinding("01B", "m1"),
		`UPDATE round SET binding_id = '01B' WHERE id = '02A'`,
	)

	log := NewMemTransport("m1")
	writer := &recording{MemTransport: log}
	got := importFrom(t, peer, log, theirs, writer)
	if got.Applied != 3 {
		t.Fatalf("import = %+v, want the three entries applied", got)
	}
	if shape := writer.shape(); shape != `binding ["01A"] upsert|binding ["01B"] upsert|round ["02A"] upsert` {
		t.Fatalf("the batch was %q, want both bindings ahead of the round", shape)
	}
	if parent := columnOf(t, peer, `SELECT binding_id FROM round WHERE id = '02A'`); parent != "01B" {
		t.Fatalf("the peer's round names binding %q, want the parent this batch carried", parent)
	}
	if label := columnOf(t, peer, `SELECT cwd FROM binding WHERE id = '01B'`); label != "/x" {
		t.Fatalf("the peer's new binding cwd = %q, want the row the round now needs", label)
	}
}
