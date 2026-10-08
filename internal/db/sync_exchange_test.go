package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestImportMarkTableIsLocalOnly pins that the import mark is about this
// machine's progress rather than about shared history: no trigger names it, and
// a write to it records nothing in the outbox. A mark that travelled would let
// another machine believe the entries between the two marks were applied.
func TestImportMarkTableIsLocalOnly(t *testing.T) {
	sqlDB := migratedSQLDB(t)
	mark := seedRoots(t, sqlDB)

	bodies := triggerBodies(t, sqlDB)
	for _, op := range outboxOps {
		if name := "sync_outbox_sync_import_mark" + op.Suffix; bodies[name] != "" {
			t.Errorf("%s exists; a mark must never travel", name)
		}
	}

	if _, err := sqlDB.Exec(`INSERT INTO sync_import_mark (origin, seq) VALUES ('instX', 7)`); err != nil {
		t.Fatalf("write sync_import_mark: %v", err)
	}
	wantEntries(t, sqlDB, mark)

	var recorded int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sync_outbox`).Scan(&recorded); err != nil {
		t.Fatalf("count sync_outbox: %v", err)
	}
	if recorded != mark {
		t.Errorf("sync_outbox holds %d rows, want the %d the fixture wrote", recorded, mark)
	}
}

// TestImportMarkRoundTrip pins the mark's whole life: an origin with no mark
// reads as absent rather than as zero, a mark written reads back, and a second
// write for the same origin replaces the first in place rather than adding a
// row, because one origin has one mark.
func TestImportMarkRoundTrip(t *testing.T) {
	sqlDB := migratedSQLDB(t)

	if seq, found, err := sqlImportMark(t, sqlDB, "instX"); err != nil {
		t.Fatalf("read a mark that was never written: %v", err)
	} else if found {
		t.Errorf("an origin with no mark reads seq %d, want absent", seq)
	}

	for _, seq := range []int{3, 11} {
		if _, err := sqlDB.Exec(`INSERT OR REPLACE INTO sync_import_mark (origin, seq) VALUES (?, ?)`,
			"instX", seq); err != nil {
			t.Fatalf("write mark %d: %v", seq, err)
		}
		got, found, err := sqlImportMark(t, sqlDB, "instX")
		if err != nil {
			t.Fatalf("read mark %d: %v", seq, err)
		}
		if !found || got != seq {
			t.Errorf("mark reads (%d, %t), want (%d, true)", got, found, seq)
		}
	}

	var rows int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sync_import_mark`).Scan(&rows); err != nil {
		t.Fatalf("count sync_import_mark: %v", err)
	}
	if rows != 1 {
		t.Errorf("sync_import_mark holds %d rows for one origin, want 1", rows)
	}

	if _, err := sqlDB.Exec(`INSERT INTO sync_import_mark (origin, seq) VALUES (?, ?)`, "instY", 4); err != nil {
		t.Fatalf("write a second origin's mark: %v", err)
	}
	if got, found, err := sqlImportMark(t, sqlDB, "instX"); err != nil || !found || got != 11 {
		t.Errorf("the first origin's mark reads (%d, %t, %v) after another origin wrote, want (11, true, nil)", got, found, err)
	}
}

// sqlImportMark reads one origin's mark straight from the file, so a test can
// check a mark against the schema rather than against the seam that writes it.
func sqlImportMark(t *testing.T, sqlDB *sql.DB, origin string) (int, bool, error) {
	t.Helper()
	var seq int
	err := sqlDB.QueryRow(`SELECT seq FROM sync_import_mark WHERE origin = ?`, origin).Scan(&seq)
	switch {
	case err == sql.ErrNoRows:
		return 0, false, nil
	case err != nil:
		return 0, false, err
	}
	return seq, true, nil
}

// TestImportMarkThroughTheSeam pins that the mark methods the importer calls
// read and write the table the migration created: a mark set through the Tx is
// visible to the *DB read, and a mark set on one origin leaves another's alone.
func TestImportMarkThroughTheSeam(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if _, found, err := d.ImportMark("instX"); err != nil {
		t.Fatalf("ImportMark on a fresh file: %v", err)
	} else if found {
		t.Error("a fresh file reports a mark for an origin that never wrote one")
	}

	if err := d.Tx(func(tx *Tx) error { return tx.SetImportMark("instX", 5) }); err != nil {
		t.Fatalf("set mark inside a Tx: %v", err)
	}
	seq, found, err := d.ImportMark("instX")
	if err != nil {
		t.Fatalf("ImportMark: %v", err)
	}
	if !found || seq != 5 {
		t.Errorf("ImportMark reads (%d, %t), want (5, true)", seq, found)
	}

	if err := d.Tx(func(tx *Tx) error { return tx.SetImportMark("instX", 9) }); err != nil {
		t.Fatalf("move the mark inside a Tx: %v", err)
	}
	var rows int
	if err := d.Tx(func(tx *Tx) error {
		seq, found, err := tx.ImportMark("instX")
		if err != nil {
			return err
		}
		if !found || seq != 9 {
			t.Errorf("the Tx reads (%d, %t), want (9, true)", seq, found)
		}
		return tx.queryRow(`SELECT COUNT(*) FROM sync_import_mark`).Scan(&rows)
	}); err != nil {
		t.Fatalf("read the mark back inside the Tx that wrote it: %v", err)
	}
	if rows != 1 {
		t.Errorf("sync_import_mark holds %d rows after two writes for one origin, want 1", rows)
	}
}
