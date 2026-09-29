package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// rawSQLDB opens a driver connection straight to path, without Open's
// migration, so a test can drive applyMigrations itself.
func rawSQLDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB
}

// TestMigrationsApplyInOrder injects a second migration through the unexported
// applyMigrations(db, fs) entry point, so it never touches the embedded set.
func TestMigrationsApplyInOrder(t *testing.T) {
	sqlDB := rawSQLDB(t, filepath.Join(t.TempDir(), "relevo.db"))

	fsys := fstest.MapFS{
		"migrations/001_initial.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE IF NOT EXISTS a (id TEXT PRIMARY KEY);`)},
		"migrations/002_second.sql":  &fstest.MapFile{Data: []byte(`CREATE TABLE IF NOT EXISTS b (id TEXT PRIMARY KEY);`)},
	}

	if err := applyMigrations(sqlDB, fsys); err != nil {
		t.Fatalf("applyMigrations: %v", err)
	}

	rows, err := sqlDB.Query(`SELECT version FROM schema_version ORDER BY version ASC`)
	if err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var versions []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan version: %v", err)
		}
		versions = append(versions, v)
	}

	if len(versions) != 2 || versions[0] != 1 || versions[1] != 2 {
		t.Fatalf("schema_version versions = %v, want [1 2]", versions)
	}

	for _, tbl := range []string{"a", "b"} {
		var name string
		if err := sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&name); err != nil {
			t.Errorf("table %s missing: %v", tbl, err)
		}
	}
}

// migrationFilesUpTo returns the embedded migrations numbered up to and
// including n, so a test can hold a database at an older schema version.
func migrationFilesUpTo(t *testing.T, n int) fstest.MapFS {
	t.Helper()
	names, err := migrationNames(migrationFiles)
	if err != nil {
		t.Fatalf("migrationNames: %v", err)
	}
	fsys := fstest.MapFS{}
	for _, name := range names {
		num, err := migrationNumber(name)
		if err != nil {
			t.Fatalf("migrationNumber(%s): %v", name, err)
		}
		if num > n {
			continue
		}
		data, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		fsys["migrations/"+name] = &fstest.MapFile{Data: data}
	}
	return fsys
}

// TestMigration007RenamesRoundColumnsAndKeepsRows pins the clean break at the
// table level: a database at 006 with a full builder_*-shaped row keeps every
// value in the renamed columns, actor defaults to builder, the index follows,
// and a second migrate run is a no-op.
func TestMigration007RenamesRoundColumnsAndKeepsRows(t *testing.T) {
	sqlDB := rawSQLDB(t, filepath.Join(t.TempDir(), "relevo.db"))

	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 6)); err != nil {
		t.Fatalf("applyMigrations through 006: %v", err)
	}

	if _, err := sqlDB.Exec(`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source)
		VALUES ('b1', 'fixture', '/work/fixture', 'pane', '2026-09-01T10:00:00.000Z', 'live')`); err != nil {
		t.Fatalf("insert binding: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO round (id, binding_id, number, started_at, outcome,
			builder_candidate, builder_harness, builder_provider, builder_model, builder_mode, switches)
		VALUES ('r1', 'b1', 1, '2026-09-01T10:00:00.000Z', 'reported',
			'claude/anthropic/sonnet', 'agy', 'anthropic', 'sonnet', 'pane', 0)`); err != nil {
		t.Fatalf("insert round: %v", err)
	}

	seven := migrationFilesUpTo(t, 7)
	if err := applyMigrations(sqlDB, seven); err != nil {
		t.Fatalf("applyMigrations 007: %v", err)
	}

	var candidate, harness, provider, model, mode, actor string
	if err := sqlDB.QueryRow(`SELECT candidate, harness, provider, model, mode, actor FROM round WHERE id = 'r1'`).
		Scan(&candidate, &harness, &provider, &model, &mode, &actor); err != nil {
		t.Fatalf("select renamed columns: %v", err)
	}
	if candidate != "claude/anthropic/sonnet" || harness != "agy" || provider != "anthropic" ||
		model != "sonnet" || mode != "pane" {
		t.Errorf("renamed values = %q/%q/%q/%q/%q, want the builder_* values",
			candidate, harness, provider, model, mode)
	}
	if actor != "builder" {
		t.Errorf("actor = %q, want builder (the column's default)", actor)
	}

	var idx string
	if err := sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'round_candidate_idx'`).Scan(&idx); err != nil {
		t.Errorf("round_candidate_idx is missing: %v", err)
	}

	// A second apply of 007 is a no-op: the version guard means the ALTERs,
	// which have no IF NOT EXISTS form, never run twice.
	if err := applyMigrations(sqlDB, seven); err != nil {
		t.Fatalf("second applyMigrations 007: %v", err)
	}
	var rows int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_version WHERE version = 7`).Scan(&rows); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if rows != 1 {
		t.Errorf("schema_version rows for 7 = %d, want 1", rows)
	}
	if err := sqlDB.QueryRow(`SELECT actor FROM round WHERE id = 'r1'`).Scan(&actor); err != nil {
		t.Fatalf("select actor after the second run: %v", err)
	}
	if actor != "builder" {
		t.Errorf("actor after the second run = %q, want builder", actor)
	}
}

func TestMigrationsApplyInOrderIsIdempotent(t *testing.T) {
	sqlDB := rawSQLDB(t, filepath.Join(t.TempDir(), "relevo.db"))

	fsys := fstest.MapFS{
		"migrations/001_initial.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE IF NOT EXISTS a (id TEXT PRIMARY KEY);`)},
	}

	if err := applyMigrations(sqlDB, fsys); err != nil {
		t.Fatalf("applyMigrations (1st): %v", err)
	}
	if err := applyMigrations(sqlDB, fsys); err != nil {
		t.Fatalf("applyMigrations (2nd): %v", err)
	}

	var count int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&count); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_version has %d rows, want 1", count)
	}
}
