package db

import (
	"database/sql"
	"testing"
)

func signalRows(t *testing.T, sqlDB *sql.DB) []string {
	t.Helper()
	rows, err := sqlDB.Query(`SELECT doc FROM shared_outbox ORDER BY doc`)
	if err != nil {
		t.Fatalf("read the doc signals: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var docs []string
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			t.Fatalf("scan: %v", err)
		}
		docs = append(docs, doc)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return docs
}

// TestConfigWriteDirtiesOnlyTheDocSignal pins that a write to a config section
// leaves one signal naming the doc and no copy of the value, however many
// writes and whichever write kind, and that the sync section, which never
// syncs, leaves none.
func TestConfigWriteDirtiesOnlyTheDocSignal(t *testing.T) {
	sqlDB := migratedSQLDB(t)
	if _, err := sqlDB.Exec(`DELETE FROM shared_outbox`); err != nil {
		t.Fatalf("clear the seed signal: %v", err)
	}

	if _, err := sqlDB.Exec(`INSERT INTO config_doc (name, body, updated_at) VALUES ('sync', '{}', 't')`); err != nil {
		t.Fatalf("write the sync section: %v", err)
	}
	if got := signalRows(t, sqlDB); len(got) != 0 {
		t.Fatalf("a write to the sync section left signals %v, want none", got)
	}

	for _, stmt := range []string{
		`INSERT INTO config_doc (name, body, updated_at) VALUES ('policy', '{"a":1}', 't')`,
		`UPDATE config_doc SET body = '{"a":2}' WHERE name = 'policy'`,
		`DELETE FROM config_doc WHERE name = 'policy'`,
	} {
		if _, err := sqlDB.Exec(`DELETE FROM shared_outbox`); err != nil {
			t.Fatalf("clear: %v", err)
		}
		if _, err := sqlDB.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
		got := signalRows(t, sqlDB)
		if len(got) != 1 || got[0] != "config" {
			t.Fatalf("after %q the signals are %v, want exactly [config]", stmt, got)
		}
	}
}
