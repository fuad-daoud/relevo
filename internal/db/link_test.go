package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigration015AddsLinkColumnsAndKeepsRows pins migration 015 against a
// database at 014: the two link columns arrive nullable, an old row keeps its
// data and carries no link, and a second apply is a no-op.
func TestMigration015AddsLinkColumnsAndKeepsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	sqlDB := rawSQLDB(t, path)

	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 14)); err != nil {
		t.Fatalf("applyMigrations through 014: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json, created_at, updated_at)
		VALUES ('br1', '', 'api', 'active', 1, '/work/api', '{}', '2026-09-01T10:00:00.000Z', '2026-09-01T10:00:00.000Z')`); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 15)); err != nil {
		t.Fatalf("applyMigrations 015: %v", err)
	}

	var name string
	var linkOrigin, linkID sql.Null[string]
	if err := sqlDB.QueryRow(`SELECT name, link_origin, link_id FROM binding_record WHERE id = 'br1'`).
		Scan(&name, &linkOrigin, &linkID); err != nil {
		t.Fatalf("select link columns: %v", err)
	}
	if name != "api" {
		t.Errorf("name = %q, want api (row kept)", name)
	}
	if linkOrigin.Valid || linkID.Valid {
		t.Errorf("link columns = %q/%q, want NULL on a row written before 015", linkOrigin.V, linkID.V)
	}

	// A second apply of 015 is a no-op: the version guard means the ALTERs
	// never run twice.
	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 15)); err != nil {
		t.Fatalf("second applyMigrations 015: %v", err)
	}
	var rows int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_version WHERE version = 15`).Scan(&rows); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if rows != 1 {
		t.Errorf("schema_version rows for 15 = %d, want 1", rows)
	}
}

// TestRecordPutKeepsLink pins the link columns' write path: RecordPut stores
// the link on insert and on update, and an empty link is stored as NULL so a
// row with no link reads back with none.
func TestRecordPutKeepsLink(t *testing.T) {
	d := openTestDB(t)

	if _, err := d.RecordPut(Record{
		Owner: "", Name: "api", State: "active", Round: 1, CWD: "/work/api", JSON: "{}",
		LinkOrigin: "01SERVER", LinkID: "01SRVRECORD",
	}); err != nil {
		t.Fatalf("RecordPut insert: %v", err)
	}

	rec, ok, err := d.RecordGet("", "api")
	if err != nil || !ok {
		t.Fatalf("RecordGet: ok=%v err=%v", ok, err)
	}
	if rec.LinkOrigin != "01SERVER" || rec.LinkID != "01SRVRECORD" {
		t.Fatalf("link after insert = %q/%q, want the written link", rec.LinkOrigin, rec.LinkID)
	}

	// A second put for the same live row proves the UPDATE writes the columns
	// too, and an empty link clears them back to NULL.
	if _, err := d.RecordPut(Record{
		Owner: "", Name: "api", State: "active", Round: 2, CWD: "/work/api", JSON: "{}",
	}); err != nil {
		t.Fatalf("RecordPut update: %v", err)
	}
	rec, ok, err = d.RecordGet("", "api")
	if err != nil || !ok {
		t.Fatalf("RecordGet after update: ok=%v err=%v", ok, err)
	}
	if rec.LinkOrigin != "" || rec.LinkID != "" {
		t.Fatalf("link after update = %q/%q, want the cleared link", rec.LinkOrigin, rec.LinkID)
	}

	// The empty link is NULL on disk, not the empty string.
	var nullOrigin, nullID sql.Null[string]
	if err := d.Tx(func(tx *Tx) error {
		return tx.queryRow(`SELECT link_origin, link_id FROM binding_record WHERE name = ?`, "api").
			Scan(&nullOrigin, &nullID)
	}); err != nil {
		t.Fatalf("read raw columns: %v", err)
	}
	if nullOrigin.Valid || nullID.Valid {
		t.Fatalf("link columns = %q/%q, want NULL", nullOrigin.V, nullID.V)
	}
}
