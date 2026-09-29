package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigration009RewritesThePromptArtifactKind pins the one stored rewrite: a
// database at 008 holding a plan artifact comes out of 009 with it named
// prompt, another kind is untouched, and a second apply is a no-op.
func TestMigration009RewritesThePromptArtifactKind(t *testing.T) {
	sqlDB := rawSQLDB(t, filepath.Join(t.TempDir(), "relevo.db"))

	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 8)); err != nil {
		t.Fatalf("applyMigrations through 008: %v", err)
	}
	seedArtifactAtSchemaEight(t, sqlDB)

	nine := migrationFilesUpTo(t, 9)
	if err := applyMigrations(sqlDB, nine); err != nil {
		t.Fatalf("applyMigrations 009: %v", err)
	}

	if got := artifactKindOf(t, sqlDB, "a1"); got != "prompt" {
		t.Errorf("artifact a1 kind = %q, want prompt", got)
	}
	if got := artifactKindOf(t, sqlDB, "a2"); got != "report" {
		t.Errorf("artifact a2 kind = %q, want report left alone", got)
	}

	// A second apply of 009 is a no-op: the version guard means the UPDATE runs
	// once.
	if err := applyMigrations(sqlDB, nine); err != nil {
		t.Fatalf("second applyMigrations 009: %v", err)
	}
	var rows int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_version WHERE version = 9`).Scan(&rows); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if rows != 1 {
		t.Errorf("schema_version rows for 9 = %d, want 1", rows)
	}
	if got := artifactKindOf(t, sqlDB, "a1"); got != "prompt" {
		t.Errorf("artifact a1 kind after the second run = %q, want prompt", got)
	}
}

// seedArtifactAtSchemaEight fills a database already at 008 with the rows 009
// must move: one plan artifact and one report artifact on the same round.
func seedArtifactAtSchemaEight(t *testing.T, sqlDB *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source)
			VALUES ('b1', 'fixture', '/work/fixture', 'headless', '2026-09-01T10:00:00.000Z', 'live')`,
		`INSERT INTO round (id, binding_id, number, started_at, outcome, switches)
			VALUES ('r1', 'b1', 1, '2026-09-01T10:00:00.000Z', 'reported', 0)`,
		`INSERT INTO artifact (id, round_id, kind, consult_id, text, bytes, sha256, captured_at)
			VALUES ('a1', 'r1', 'plan', '', 'the prompt', 10, 'sha-a1', '2026-09-01T10:00:00.000Z')`,
		`INSERT INTO artifact (id, round_id, kind, consult_id, text, bytes, sha256, captured_at)
			VALUES ('a2', 'r1', 'report', '', 'the report', 10, 'sha-a2', '2026-09-01T10:00:00.000Z')`,
	} {
		if _, err := sqlDB.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
}

// artifactKindOf reads one artifact row's kind.
func artifactKindOf(t *testing.T, sqlDB *sql.DB, id string) string {
	t.Helper()
	var kind string
	if err := sqlDB.QueryRow(`SELECT kind FROM artifact WHERE id = ?`, id).Scan(&kind); err != nil {
		t.Fatalf("select artifact %s: %v", id, err)
	}
	return kind
}
