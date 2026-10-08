package synclog

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// What a writer's schema version does to a machine that is behind it, and what
// column drift in either direction costs. A body carries the columns its writer
// had, and this machine's schema decides which of them it writes, so the two
// schemas meeting anywhere is the normal case and not the exceptional one.

// knownVersion is the schema version this build understands, read from the file
// an import writes into rather than hardcoded, so a case that stands in for an
// upgrade moves with the migrations this tree carries.
func knownVersion(t *testing.T, d *db.DB) int {
	t.Helper()
	_, know := d.SchemaVersions()
	return know
}

// appendBatch puts entries on the log as one append, which is what gives them
// one batch number between them and makes them the unit an importer either
// applies whole or not at all.
func appendBatch(t *testing.T, log *MemTransport, entries ...Entry) {
	t.Helper()
	if _, err := log.Append(entries); err != nil {
		t.Fatalf("append a batch of %d entries: %v", len(entries), err)
	}
}

// upsert is one binding entry at a named schema version, the shape a remote
// machine's log carries: an upsert whose body is the row. The version is a
// parameter because it is the one thing that decides whether this machine can
// read the entry at all.
func upsert(t *testing.T, id string, schema int) Entry {
	t.Helper()
	body := bodyFor(t, map[string]any{
		"id": id, "name": id, "cwd": "/x",
		"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m1",
	})
	return Entry{Table: "binding", PK: `["` + id + `"]`, Op: OpUpsert, SchemaVersion: schema, Body: body}
}

// tableColumns is one table's columns as the file declares them, so a case can
// assert that a body's invented names did not become columns of the schema.
func tableColumns(t *testing.T, d *db.DB, tbl string) string {
	t.Helper()
	raw, err := db.OpenRawReadOnly(d.Path())
	if err != nil {
		t.Fatalf("open the file for reading: %v", err)
	}
	defer func() { _ = raw.Close() }()

	rows, err := raw.Query(`SELECT name || ' ' || type FROM pragma_table_info(?) ORDER BY cid`, tbl)
	if err != nil {
		t.Fatalf("read the columns of %s: %v", tbl, err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("read the columns of %s: %v", tbl, err)
		}
		out = append(out, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the columns of %s: %v", tbl, err)
	}
	return strings.Join(out, "|")
}

// A writer's entry from a schema this machine does not know stops its origin
// where it stands. The batch before it applied and its mark is where the exporter
// left it; the held batch and every batch behind it are left alone, so an
// upgraded machine picks them up from the same mark rather than from a mark that
// had moved over rows nobody wrote.
//
// The newer entry sits in the middle of its batch on purpose. An importer that
// only compared the batch's first entry would pass this, and would then wedge:
// its mark would rest inside the batch, and the transport skips whole batches at
// or before the mark, so the entries it could not read would never be handed
// over again.
func TestNewerSchemaHoldsTheMark(t *testing.T) {
	t.Parallel()
	peer, peerPath := peerFile(t, "m2")
	know := knownVersion(t, peer)
	seed(t, peerPath,
		`INSERT INTO installation (id, label, first_seen, last_seen) VALUES ('m1', 'the laptop', '2026-01-02T03:04:05.000Z', '2026-01-02T03:04:05.000Z')`,
	)

	log := NewMemTransport("m1")
	appendBatch(t, log, upsert(t, "01A", know))
	appendBatch(t, log,
		upsert(t, "02A", know),
		upsert(t, "02B", know+1),
		upsert(t, "02C", know),
	)
	appendBatch(t, log, upsert(t, "03A", know))

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import from a writer ahead of this machine: %v", err)
	}

	// Only the first batch is whole and readable, so only it counts.
	if got.Batches != 1 || got.Applied != 1 {
		t.Fatalf("import = %+v, want the one batch ahead of the newer entry applied", got)
	}
	if len(got.Held) != 1 {
		t.Fatalf("Held = %+v, want one origin reported as held", got.Held)
	}
	hold := got.Held[0]
	if hold.Origin != "m1" || hold.Schema != know+1 {
		t.Fatalf("the hold is %+v, want m1 held at schema %d", hold, know+1)
	}
	if hold.Seq != 1 {
		t.Fatalf("the hold rests at %d, want the batch before the newer entry", hold.Seq)
	}

	// The report names the installation, because the action it asks for is an
	// upgrade and the reader has to know whose entries are waiting.
	if !strings.Contains(hold.String(), "the laptop") {
		t.Fatalf("the hold reads %q, want it to name the writer's label", hold)
	}

	if have := sharedRows(t, peer); have != `installation ["m1"]|binding ["01A"]` {
		t.Fatalf("the peer holds %q, want the directory row and only the batch before the newer entry", have)
	}
	if seq, found, err := peer.ImportMark("m1"); err != nil || !found || seq != 1 {
		t.Fatalf("ImportMark = (%d, %t, %v), want the mark resting on the last applied entry", seq, found, err)
	}

	// Nothing of the held batch landed, including the entries in it this
	// machine could read: a batch is applied whole or not at all.
	if have := sharedRows(t, peer); strings.Contains(have, "02A") || strings.Contains(have, "02C") {
		t.Fatalf("the peer holds %q, want no entry of the held batch", have)
	}
}

// The hold is not a failure and does not stop the origins beside it. A machine
// whose peers disagree about their schema versions still shares everything the
// versions agree on, and a hold that stopped the whole exchange would lose those
// rows over one writer being ahead.
//
// The reader is m2, so the two writers are m1 and m3: a machine's own entries
// are already in its file and the transport never hands them back to it.
func TestNewerSchemaHoldsOneOriginOnly(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")
	entryFor(t, log, upsert(t, "01A", know))

	// A third machine writing to the same log, one schema ahead of this one.
	// OnLog is the handle that puts its entries where this reader's pull finds
	// them: two machines reach one remote, not two remotes.
	entryFor(t, log.OnLog("m3"), upsert(t, "03A", know+1))

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import with one origin ahead: %v", err)
	}
	if len(got.Held) != 1 || got.Held[0].Origin != "m3" {
		t.Fatalf("Held = %+v, want only m3 held", got.Held)
	}
	want := `binding ["01A"]`
	if have := sharedRows(t, peer); have != want {
		t.Fatalf("the peer holds %q, want the origin that did not hold", have)
	}
	if seq, found, err := peer.ImportMark("m1"); err != nil || !found || seq != 1 {
		t.Fatalf("ImportMark for the readable origin = (%d, %t, %v), want it advanced", seq, found, err)
	}
}

// Once this machine knows the writer's schema the held entries apply and the
// mark advances past them. This is the half of the hold that makes it safe: the
// entries were left readable rather than refused, so upgrading is all it takes
// to converge.
func TestHeldEntriesApplyAfterTheUpgrade(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")
	appendBatch(t, log, upsert(t, "01A", know))
	// The newer entry sits in the middle of its batch, so the mark the hold
	// leaves behind rests on the batch before it rather than inside this one.
	// That is what lets these entries come back at all: the transport skips
	// whole batches at or before the mark, so a mark that had moved onto the
	// entries ahead of the held one would hide the held one forever.
	appendBatch(t, log,
		upsert(t, "02A", know),
		upsert(t, "02B", know+1),
		upsert(t, "02C", know),
	)

	if got, err := NewImporter(peer, log.OnLog("m2")).Import(); err != nil {
		t.Fatalf("import before the upgrade: %v", err)
	} else if len(got.Held) != 1 {
		t.Fatalf("Held = %+v, want the newer origin held before the upgrade", got.Held)
	}

	// The same entries against an importer that knows the version. The entries
	// are unchanged: nothing was rewritten to get them applied, only read.
	i := NewImporter(peer, log.OnLog("m2"))
	i.known = know + 1
	got, err := i.Import()
	if err != nil {
		t.Fatalf("import after the upgrade: %v", err)
	}
	if len(got.Held) != 0 {
		t.Fatalf("Held = %+v, want nothing held once this machine knows the schema", got.Held)
	}
	want := `binding ["01A"]|binding ["02A"]|binding ["02B"]|binding ["02C"]`
	if have := sharedRows(t, peer); have != want {
		t.Fatalf("the peer holds %q, want every entry of the held batch applied", have)
	}
	if seq, _, err := peer.ImportMark("m1"); err != nil || seq != 4 {
		t.Fatalf("ImportMark = %d (%v), want the mark past the held entry", seq, err)
	}
}

// A body naming a column this machine's schema does not carry applies with that
// column dropped. The names in a body come from the writer, so a column a newer
// migration added is what a body carries and this machine has not got; the row
// it describes is still a row this machine can write, so refusing the entry
// would lose a change this build could have kept most of.
//
// The row is checked column by column, and the columns the body never named are
// checked as NULL with it: an import that wrote the body's values and also
// invented a column for every key it did not recognize would pass a case that
// looked only at the values the body carried.
func TestImporterIgnoresUnknownColumns(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")
	entryFor(t, log, Entry{
		Table: "binding", PK: `["01A"]`, Op: OpUpsert, SchemaVersion: knownVersion(t, peer),
		Body: bodyFor(t, map[string]any{
			"id": "01A", "name": "01A", "cwd": "/x",
			"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m1",
			// Columns a newer migration of the writer's schema declares, and one
			// name no schema declares at all.
			"transcript_codec": "zstd", "last_seen_by": "m2",
		}),
	})

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import a body carrying columns this schema does not declare: %v", err)
	}
	if got.Applied != 1 {
		t.Fatalf("import = %+v, want the one entry applied", got)
	}
	assertColumns(t, peer, "binding", `["01A"]`, map[string]string{
		"id": "01A", "name": "01A", "cwd": "/x", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual", "origin": "m1",
	})
	// The unknown names are absent from the file's declaration rather than
	// named in the statement: an importer that invented them would be writing
	// to a schema it does not have.
	columns := tableColumns(t, peer, "binding")
	if !strings.Contains(columns, "id TEXT") {
		t.Fatalf("the binding's columns are %q, want the table's own declaration", columns)
	}
	if strings.Contains(columns, "transcript_codec") || strings.Contains(columns, "last_seen_by") {
		t.Fatalf("the binding's columns are %q, want the body's invented names absent", columns)
	}
}

// A body missing a column this machine's schema declares applies with that
// column's default on an insert and leaves the column alone on an update. A
// writer that dropped a column sends bodies without it, and the row it describes
// is one this machine's schema still holds a value for: naming the column as NULL
// on either path would erase a value the writer has and the body merely left out.
//
// The rows are checked column by column, so the insert path is pinned on the
// column it defaulted and the update path on the column it kept. The one column
// the body does not decide is the owner. It is resolved from this file rather
// than read out of the body, so the row lands under the origin whose entries were
// applied -- which is what makes the entry it came from still be the owner of it
// on the next import.
func TestImporterDefaultsMissingColumns(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")
	known := knownVersion(t, peer)

	// Two entries for one row: the first names values the second leaves out, so
	// the update path is what a body's silence has to survive. A second row is
	// inserted with the same silence, so the insert path is checked too.
	entryFor(t, log, Entry{
		Table: "binding", PK: `["01A"]`, Op: OpUpsert, SchemaVersion: known,
		Body: bodyFor(t, map[string]any{
			"id": "01A", "name": "01A", "cwd": "/x", "builder_mode": "local",
			"created_at": "t", "ingest_source": "manual", "origin": "m1",
			"ticket": "#473", "final_state": "done",
		}),
	})
	entryFor(t, log, Entry{
		Table: "binding", PK: `["01A"]`, Op: OpUpsert, SchemaVersion: known,
		Body: bodyFor(t, map[string]any{
			"id": "01A", "name": "01A", "cwd": "/x",
			"builder_mode": "local", "created_at": "t", "ingest_source": "manual",
		}),
	})
	entryFor(t, log, Entry{
		Table: "binding", PK: `["01B"]`, Op: OpUpsert, SchemaVersion: known,
		Body: bodyFor(t, map[string]any{
			"id": "01B", "name": "01B", "cwd": "/x",
			"builder_mode": "local", "created_at": "t", "ingest_source": "manual",
		}),
	})

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import a body omitting a defaulted column: %v", err)
	}
	if got.Applied != 3 {
		t.Fatalf("import = %+v, want all three entries applied", got)
	}
	assertColumns(t, peer, "binding", `["01A"]`, map[string]string{
		"id": "01A", "name": "01A", "cwd": "/x", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual", "origin": "m1",
		"ticket": "#473", "final_state": "done",
	})
	assertColumns(t, peer, "binding", `["01B"]`, map[string]string{
		"id": "01B", "name": "01B", "cwd": "/x", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual", "origin": "m1",
	})
}

// sharedColumn reads one column of one row through the exchange seam, so a case
// can assert on a column whose value is not text.
func sharedColumn(t *testing.T, d *db.DB, tbl, pk string) db.ExchangeRow {
	t.Helper()
	row, found, err := d.ReadExchangeRow(tbl, pk)
	if err != nil || !found {
		t.Fatalf("read %s %s: found=%t, %v", tbl, pk, found, err)
	}
	return row
}

// assertColumns checks every column of one row against want, and requires every
// column the expectations leave out to be NULL. A case that checked only the
// columns a body named would pass an import that also wrote the columns it did
// not know; the whole row is what the exchange either reproduces or does not.
func assertColumns(t *testing.T, d *db.DB, tbl, pk string, want map[string]string) {
	t.Helper()
	row := sharedColumn(t, d, tbl, pk)
	seen := make(map[string]bool, len(want))
	for _, col := range row.Columns {
		expected, named := want[col.Name]
		if !named {
			expected = "<null>"
		} else {
			seen[col.Name] = true
		}
		if have := renderValue(col.Value); have != expected {
			t.Fatalf("%s.%s = %s, want %s", tbl, col.Name, have, expected)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("the %s row carries no %s column", tbl, name)
		}
	}
}

// renderValue spells one stored column value for a comparison, with NULL kept
// distinct from an empty text value and a BLOB shown as the bytes it holds.
func renderValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "<null>"
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}
