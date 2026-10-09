package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestOutboxTruncateEmptiesTheLog pins the truncate on a real handle: every
// entry goes, a second one has nothing to do, and a write after the truncate
// records again. The last half is what a truncate that took the table's
// triggers with it would break, and it is the half that matters, because a
// machine still writing while its outbox is emptied has to keep recording.
func TestOutboxTruncateEmptiesTheLog(t *testing.T) {
	d, sqlDB := truncateDB(t)
	seedOutboxRows(t, d, "/checkout/one", "/checkout/two")
	if got := len(outboxRows(t, sqlDB, 0)); got != 2 {
		t.Fatalf("the fixture recorded %d entries, want 2", got)
	}

	if err := d.TruncateOutbox(); err != nil {
		t.Fatalf("TruncateOutbox: %v", err)
	}
	if got := outboxRows(t, sqlDB, 0); len(got) != 0 {
		t.Errorf("the outbox holds %d entries after a truncate, want none", len(got))
	}

	// A truncate on an empty outbox is the shape the schedule meets most
	// often, and it is not a failure.
	if err := d.TruncateOutbox(); err != nil {
		t.Errorf("TruncateOutbox on an empty outbox: %v", err)
	}

	seedOutboxRows(t, d, "/checkout/three")
	if got := len(outboxRows(t, sqlDB, 0)); got != 1 {
		t.Errorf("a write after a truncate recorded %d entries, want 1", got)
	}
}

// TestOutboxTruncateOnAFileWithoutTheTable pins that a handle whose file no
// longer carries the outbox has nothing to truncate, and is not a failure.
func TestOutboxTruncateOnAFileWithoutTheTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	sqlDB := rawSQLDB(t, path)
	if err := applyMigrations(sqlDB, migrationFiles); err != nil {
		t.Fatalf("applyMigrations: %v", err)
	}
	if _, err := sqlDB.Exec(`DROP TABLE sync_outbox`); err != nil {
		t.Fatalf("drop sync_outbox: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.TruncateOutbox(); err != nil {
		t.Errorf("TruncateOutbox without the table: %v", err)
	}
}

// truncateDB opens a migrated machine database and a driver connection to the
// same file, so a test seeds through the handle and reads the entries the
// triggers wrote.
func truncateDB(t *testing.T) (*DB, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, rawSQLDB(t, path)
}

// seedOutboxRows writes one repo row per directory. repo is a shared table, so
// each write records exactly one entry; a row's directory is its natural key,
// so one directory per row is what makes them separate rows rather than one row
// written twice.
func seedOutboxRows(t *testing.T, d *DB, dirs ...string) {
	t.Helper()
	first := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	for _, dir := range dirs {
		if err := d.Tx(func(tx *Tx) error {
			_, err := tx.UpsertRepo(Repo{CommonDir: &dir, FirstSeen: first})
			return err
		}); err != nil {
			t.Fatalf("seed repo %s: %v", dir, err)
		}
	}
}

// TestHasOutboxEntriesTracksTheLog pins the probe against a real handle: empty
// before a write, set after one, empty again after a truncate.
func TestHasOutboxEntriesTracksTheLog(t *testing.T) {
	d, _ := truncateDB(t)
	has := func() bool {
		t.Helper()
		got, err := d.HasOutboxEntries()
		if err != nil {
			t.Fatalf("HasOutboxEntries: %v", err)
		}
		return got
	}
	if has() {
		t.Fatal("a fresh handle reports outbox entries")
	}
	seedOutboxRows(t, d, "/checkout/probe")
	if !has() {
		t.Error("a handle with a recorded write reports an empty outbox")
	}
	if err := d.TruncateOutbox(); err != nil {
		t.Fatalf("TruncateOutbox: %v", err)
	}
	if has() {
		t.Error("a truncated outbox reports entries")
	}
}

// TestHasOutboxEntriesOnAFileWithoutTheTable pins that a file with no outbox
// has nothing waiting.
func TestHasOutboxEntriesOnAFileWithoutTheTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	sqlDB := rawSQLDB(t, path)
	if err := applyMigrations(sqlDB, migrationFiles); err != nil {
		t.Fatalf("applyMigrations: %v", err)
	}
	if _, err := sqlDB.Exec(`DROP TABLE sync_outbox`); err != nil {
		t.Fatalf("drop sync_outbox: %v", err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if got, err := d.HasOutboxEntries(); err != nil || got {
		t.Errorf("HasOutboxEntries without the table = (%v, %v), want (false, nil)", got, err)
	}
}
