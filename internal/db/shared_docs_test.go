package db

import (
	"testing"
)

// sharedDocTables is every table the shared-doc bookkeeping adds, with the
// reason each one may never travel. It is written out rather than derived so a
// table added to the migration and not to this list is a failing test rather
// than a row nothing here would look at.
var sharedDocTables = []string{
	// Which doc changed. Read out of the doc at export time, so it carries no
	// value of its own.
	"shared_outbox",
	// What this machine last put on the log for each key.
	"shared_export_state",
	// The newest doc state this machine has applied for each key.
	"shared_applied",
	// Doc entries parked until a key arrives to check them.
	"shared_pending",
	// The machine-local layer of the config doc.
	"config_local",
}

// TestSharedDocTablesStayOutOfSharedTables pins the classification a shared doc
// depends on: every table behind it is machine-local, so none of it is in the
// shared table list the triggers, the exporter and the reconcile are built
// from. One of them in that list would have its rows read by a reconciler and
// written to a remote, which is a row about what this machine sent, saw or
// parked, not about shared history.
func TestSharedDocTablesStayOutOfSharedTables(t *testing.T) {
	sqlDB := migratedSQLDB(t)

	for _, name := range sharedDocTables {
		for _, shared := range SharedTables {
			if shared.Name == name {
				t.Errorf("%s is in SharedTables; it is machine-local bookkeeping and must never travel", name)
			}
		}
		if _, err := sqlDB.Exec(`SELECT 1 FROM ` + name + ` LIMIT 1`); err != nil {
			t.Errorf("%s is missing from the schema: %v", name, err)
		}
	}
}

// TestSharedDocTablesCarryNoTrigger pins that no trigger sits on any of them. A
// trigger on one would put rows into an outbox before any exporter exists to
// drain one, so the first write after this migration would leave something
// nothing reads. The doc trigger on config_doc lands in its own migration and
// writes into shared_outbox, so it does not make shared_outbox a triggered
// table either.
func TestSharedDocTablesCarryNoTrigger(t *testing.T) {
	sqlDB := migratedSQLDB(t)

	rows, err := sqlDB.Query(`SELECT name, tbl_name FROM sqlite_schema WHERE type = 'trigger'`)
	if err != nil {
		t.Fatalf("read sqlite_schema: %v", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name, tblName string
		if err := rows.Scan(&name, &tblName); err != nil {
			t.Fatalf("scan trigger: %v", err)
		}
		for _, tbl := range sharedDocTables {
			if tblName == tbl {
				t.Errorf("trigger %s sits on %s; the doc bookkeeping carries none", name, tbl)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate triggers: %v", err)
	}
}

// TestSharedPendingIsKeyedByOriginAndSeq pins the key a parked doc entry hangs
// under. The same item may be delivered twice before it can be verified, so
// keying by document and item would collapse the two deliveries into one row
// and the second delivery would be lost rather than re-offered.
func TestSharedPendingIsKeyedByOriginAndSeq(t *testing.T) {
	sqlDB := migratedSQLDB(t)

	if _, err := sqlDB.Exec(`INSERT INTO shared_pending (origin, seq, entry, parked_at)
		VALUES ('o1', 1, '{}', '2026-10-12T00:00:00.000Z')`); err != nil {
		t.Fatalf("first parked entry: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO shared_pending (origin, seq, entry, parked_at)
		VALUES ('o1', 2, '{}', '2026-10-12T00:00:01.000Z')`); err != nil {
		t.Fatalf("second parked entry for the same origin: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO shared_pending (origin, seq, entry, parked_at)
		VALUES ('o2', 1, '{}', '2026-10-12T00:00:02.000Z')`); err != nil {
		t.Fatalf("parked entry for the same sequence at another origin: %v", err)
	}

	var count int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM shared_pending`).Scan(&count); err != nil {
		t.Fatalf("count shared_pending: %v", err)
	}
	if count != 3 {
		t.Errorf("shared_pending holds %d rows, want 3", count)
	}
}

// TestSharedStateRowsCarryAnOrigin pins that origin is NOT NULL on both tables
// whose whole decision is made by comparing a stamp and an origin together. A
// row with no origin cannot say whether an arriving pair is this machine's own
// echo, and an export that recorded one would re-import it forever.
func TestSharedStateRowsCarryAnOrigin(t *testing.T) {
	sqlDB := migratedSQLDB(t)

	for _, tbl := range []string{"shared_export_state", "shared_applied"} {
		if _, err := sqlDB.Exec(`INSERT INTO ` + tbl + ` (doc, key, stamp) VALUES ('config', 'policy', '1')`); err == nil {
			t.Errorf("%s accepted a row with no origin", tbl)
		}
	}
}

// TestOutboxSignalCarriesNoPayload pins that the shared outbox records which doc
// changed and nothing more. A row here is a signal to diff the doc at export
// time; a value beside it would be a copy taken at write time, which is stale
// by the time anything reads it.
func TestOutboxSignalCarriesNoPayload(t *testing.T) {
	sqlDB := migratedSQLDB(t)

	if _, err := sqlDB.Exec(`INSERT OR REPLACE INTO shared_outbox (doc, dirtied_at)
		VALUES ('config', '2026-10-12T00:00:00.000Z')`); err != nil {
		t.Fatalf("write the doc signal: %v", err)
	}

	var columns []string
	rows, err := sqlDB.Query(`SELECT name FROM pragma_table_info('shared_outbox')`)
	if err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}

	for _, name := range columns {
		if name != "doc" && name != "dirtied_at" {
			t.Errorf("shared_outbox carries column %q; the signal is a doc name and a time", name)
		}
	}
}
