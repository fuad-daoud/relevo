package synclog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// What a body is allowed to do to this machine's file: name the rows it carries,
// and nothing else. The values in a body travel from a machine this one does not
// control, so the table it names and the columns those values are written under
// are decided here, from this machine's schema, and a body that carries more than
// that is answered by dropping the rest.

// entryFor builds one entry as a remote machine would have written it: the
// transport numbers it, so the batch it lands in is the log's own. A body is
// given as raw JSON because that is the shape a remote sends and the shape the
// importer has to survive.
func entryFor(t *testing.T, log *MemTransport, e Entry) {
	t.Helper()
	if _, err := log.Append([]Entry{e}); err != nil {
		t.Fatalf("append the entry: %v", err)
	}
}

// bodyFor renders a row's values as the JSON a log entry carries, with the
// columns named as given so a case can put a name this schema does not declare.
func bodyFor(t *testing.T, values map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("render the body: %v", err)
	}
	return raw
}

// tablesIn is every table the file holds, so a case can assert that a body
// carrying SQL-ish text left the file's tables standing.
func tablesIn(t *testing.T, d *db.DB) string {
	t.Helper()
	raw, err := db.OpenRawReadOnly(d.Path())
	if err != nil {
		t.Fatalf("open the file for reading: %v", err)
	}
	defer func() { _ = raw.Close() }()

	rows, err := raw.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("read the file's tables: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("read the file's tables: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the file's tables: %v", err)
	}
	return strings.Join(out, "|")
}

// An entry naming a table this machine does not share is not applied. The table
// name reaches SQL, and the only names allowed to are the ones the db package
// has already classified as shared -- a table name from a remote is exactly the
// input a query is built from.
//
// The batch is dropped rather than failed on: an import that returned an error
// here would leave the mark where it was, so the same entry would be offered
// again on every run and refuse itself again for as long as the log holds it.
func TestImporterRefusesUnknownTable(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")

	entryFor(t, log, Entry{
		Origin: "m1", Table: "sqlite_master", PK: `["sqlite_master"]`,
		Op: OpDelete, SchemaVersion: 23,
	})

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("importing an entry for a table that is not shared: %v", err)
	}
	if got.Applied != 0 || got.Batches != 0 {
		t.Fatalf("import = %+v, want nothing applied", got)
	}
	if len(got.Dropped) != 1 {
		t.Fatalf("Dropped = %+v, want the unshared-table batch reported as dropped", got.Dropped)
	}
	if !strings.Contains(got.Dropped[0].Reason, "shared table") {
		t.Fatalf("the drop reads %q, want it to name the table as unshared", got.Dropped[0])
	}
	// The mark moved past the drop, so nothing behind it is offered again.
	if seq, found, err := peer.ImportMark("m1"); err != nil || !found || seq != 1 {
		t.Fatalf("ImportMark = (%d, %t, %v), want the mark past the dropped batch", seq, found, err)
	}
	if again, err := NewImporter(peer, log.OnLog("m2")).Import(); err != nil {
		t.Fatalf("import again after the drop: %v", err)
	} else if again.Applied != 0 || len(again.Dropped) != 0 {
		t.Fatalf("the second import = %+v, want the dropped batch not re-read", again)
	}
}

// A column name a body carries is never one this machine writes the values
// under. The body here carries the whole statement as a key and a value; both are
// dropped, and every table in the file survives, which is what says the text was
// data rather than SQL.
func TestImporterNeverSplicesBodyKeys(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	log := NewMemTransport("m1")

	body := bodyFor(t, map[string]any{
		"id": "b1", "name": "b1", "cwd": "/x",
		"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m1",
		`x"; DROP TABLE binding; --`: `y'; DELETE FROM binding; --`,
		"another invented column":    "value",
	})
	entryFor(t, log, Entry{
		Origin: "m1", Table: "binding", PK: `["b1"]`,
		Op: OpUpsert, SchemaVersion: 23, Body: body,
	})

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import a body carrying invented column names: %v", err)
	}
	if got.Applied != 1 {
		t.Fatalf("import = %+v, want the one entry applied", got)
	}
	if have := sharedRows(t, peer); have != `binding ["b1"]` {
		t.Fatalf("the peer holds %q, want the one binding the body named", have)
	}
	if cwd := columnOf(t, peer, `SELECT cwd FROM binding WHERE id = 'b1'`); cwd != "/x" {
		t.Fatalf("cwd = %q, want the value the body carried", cwd)
	}
	if !strings.Contains(tablesIn(t, peer), "binding|") {
		t.Fatalf("the file's tables are %q, want binding among them", tablesIn(t, peer))
	}
}

// Re-applying a batch changes nothing. The mark is what keeps a normal run from
// re-reading a batch, so the property has to hold on its own: an import that was
// interrupted between applying a batch and moving its mark will do exactly this.
func TestImportIsIdempotent(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, theirPath,
		insertBinding("01A", "m1"),
		insertRound("02A", "01A"),
		insertRecord("03A", "m1"),
		insertRoundFile("03A", "f", 1),
	)

	log := NewMemTransport("m1")
	writer := &recording{MemTransport: log}
	importFrom(t, peer, log, theirs, writer)
	first := sharedRows(t, peer)
	marks, err := peer.ImportMarks()
	if err != nil {
		t.Fatalf("read the marks after the first import: %v", err)
	}

	// The same entries again, against the marks the first import left. Nothing
	// should be pulled, and nothing should change.
	batch, err := log.OnLog("m2").Pull(marks)
	if err != nil {
		t.Fatalf("pull again from the marks the first import left: %v", err)
	}
	if len(batch) != 0 {
		t.Fatalf("pull returned %d entries past the marks, want the batch already applied", len(batch))
	}
	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import again: %v", err)
	}
	if got.Batches != 0 || got.Applied != 0 {
		t.Fatalf("the second import = %+v, want nothing left to apply", got)
	}
	if have := sharedRows(t, peer); have != first {
		t.Fatalf("the peer holds %q, want the first import's %q", have, first)
	}

	// A mark that did not survive a restore brings the same batch back, and it
	// is the whole batch rather than part of it. Re-applying it writes the same
	// rows: the upsert is in place, and the deletes name rows that are already
	// gone.
	seed(t, peer.Path(), `DELETE FROM sync_import_mark`)
	if got, err := NewImporter(peer, log.OnLog("m2")).Import(); err != nil {
		t.Fatalf("import the replayed batch: %v", err)
	} else if got.Batches != 1 || got.Applied != 4 {
		t.Fatalf("the replayed import = %+v, want the one batch of four entries re-applied", got)
	}
	if have := sharedRows(t, peer); have != first {
		t.Fatalf("the peer holds %q after the replay, want %q", have, first)
	}
}
