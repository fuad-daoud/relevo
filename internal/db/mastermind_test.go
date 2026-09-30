package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestMigration008RenamesMasterMindAndKeepsRows pins D3: a database at 007
// holding one planner row, one binding with planner_id and the registry's
// planner/<id> kv row comes out of 008 with the same data under mastermind,
// mastermind_id and mastermind/<id>, the two indexes renamed, and a second
// run a no-op.
func TestMigration008RenamesMasterMindAndKeepsRows(t *testing.T) {
	sqlDB := rawSQLDB(t, filepath.Join(t.TempDir(), "relevo.db"))
	seedSchemaSeven(t, sqlDB)

	eight := migrationFilesUpTo(t, 8)
	if err := applyMigrations(sqlDB, eight); err != nil {
		t.Fatalf("applyMigrations 008: %v", err)
	}
	assertSchemaEightRenames(t, sqlDB)

	// A second apply of 008 is a no-op: the RENAMEs have no IF NOT EXISTS form,
	// so the version guard must keep them from running twice.
	if err := applyMigrations(sqlDB, eight); err != nil {
		t.Fatalf("second applyMigrations 008: %v", err)
	}
	var rows int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_version WHERE version = 8`).Scan(&rows); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if rows != 1 {
		t.Errorf("schema_version rows for 8 = %d, want 1", rows)
	}
	if got := kvKeyAfter(t, sqlDB); got != "mastermind/pl_aaaaaaaaaaaa" {
		t.Errorf("kv key after the second run = %q, want mastermind/pl_aaaaaaaaaaaa", got)
	}
}

// seedSchemaSeven migrates a fresh database through 007 and fills it with the
// rows 008 must move: one planner row, one binding carrying planner_id, and
// the registry's planner/<id> kv row.
func seedSchemaSeven(t *testing.T, sqlDB *sql.DB) {
	t.Helper()
	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 7)); err != nil {
		t.Fatalf("applyMigrations through 007: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO planner (id, harness_kind, session_id, transcript_locator, first_seen, last_seen)
		VALUES ('pl_aaaaaaaaaaaa', 'claude', 'sess-1', '/tmp/t.jsonl', '2026-09-01T10:00:00.000Z', '2026-09-01T10:00:00.000Z')`); err != nil {
		t.Fatalf("insert planner: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO binding (id, name, planner_id, cwd, builder_mode, created_at, ingest_source)
		VALUES ('b1', 'fixture', 'pl_aaaaaaaaaaaa', '/work/fixture', 'headless', '2026-09-01T10:00:00.000Z', 'live')`); err != nil {
		t.Fatalf("insert binding: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO kv (key, value_json, updated_at)
		VALUES ('planner/pl_aaaaaaaaaaaa', '{}', '2026-09-01T10:00:00.000Z')`); err != nil {
		t.Fatalf("insert kv: %v", err)
	}
}

// assertSchemaEightRenames pins the table, the column and the two indexes under
// their mastermind names, with every value kept.
func assertSchemaEightRenames(t *testing.T, sqlDB *sql.DB) {
	t.Helper()
	var id, kind, session string
	if err := sqlDB.QueryRow(`SELECT id, harness_kind, session_id FROM mastermind WHERE id = 'pl_aaaaaaaaaaaa'`).
		Scan(&id, &kind, &session); err != nil {
		t.Fatalf("select renamed mastermind row: %v", err)
	}
	if kind != "claude" || session != "sess-1" {
		t.Errorf("mastermind row = %s/%s, want claude/sess-1", kind, session)
	}

	var bindingMasterMind string
	if err := sqlDB.QueryRow(`SELECT mastermind_id FROM binding WHERE id = 'b1'`).Scan(&bindingMasterMind); err != nil {
		t.Fatalf("select renamed mastermind_id: %v", err)
	}
	if bindingMasterMind != "pl_aaaaaaaaaaaa" {
		t.Errorf("binding.mastermind_id = %q, want pl_aaaaaaaaaaaa", bindingMasterMind)
	}

	if got := kvKeyAfter(t, sqlDB); got != "mastermind/pl_aaaaaaaaaaaa" {
		t.Errorf("kv key = %q, want mastermind/pl_aaaaaaaaaaaa", got)
	}

	for _, idx := range []string{"mastermind_harness_session_uidx", "binding_mastermind_id_idx"} {
		var name string
		if err := sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&name); err != nil {
			t.Errorf("%s is missing: %v", idx, err)
		}
	}
	for _, gone := range []string{"planner_harness_session_uidx", "binding_planner_id_idx"} {
		var name string
		if err := sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, gone).Scan(&name); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("old index %s still present (err = %v)", gone, err)
		}
	}
}

// kvKeyAfter reads the one kv row's key.
func kvKeyAfter(t *testing.T, sqlDB *sql.DB) string {
	t.Helper()
	var key string
	if err := sqlDB.QueryRow(`SELECT key FROM kv`).Scan(&key); err != nil {
		t.Fatalf("select kv key: %v", err)
	}
	return key
}

// TestUpsertMasterMindByID pins the id-first write: relevo's mastermind records carry
// their own id, so ingest upserts by that id -- inserting it first, then
// updating the columns a move can change.
func TestUpsertMasterMindByID(t *testing.T) {
	d := openTestDB(t)
	t1 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)

	const id = "pl_aaaaaaaaaaaa"
	got, err := d.UpsertMasterMind(MasterMind{
		ID:                id,
		HarnessKind:       "claude",
		SessionID:         "sess-1",
		TranscriptLocator: ptr("/tmp/t1.jsonl"),
		FirstSeen:         t1,
		LastSeen:          t1,
	})
	if err != nil {
		t.Fatalf("UpsertMasterMind (insert): %v", err)
	}
	if got != id {
		t.Fatalf("UpsertMasterMind returned %q, want the record's own id %q", got, id)
	}

	// The same id, a moved session, no new transcript locator: the row keeps
	// its id and its locator and takes the new session and last_seen.
	got, err = d.UpsertMasterMind(MasterMind{
		ID:          id,
		HarnessKind: "claude",
		SessionID:   "sess-2",
		LastSeen:    t2,
	})
	if err != nil {
		t.Fatalf("UpsertMasterMind (update): %v", err)
	}
	if got != id {
		t.Fatalf("UpsertMasterMind (update) returned %q, want %q", got, id)
	}

	p, ok, err := d.MasterMindBySession("claude", "sess-2")
	if err != nil {
		t.Fatalf("MasterMindBySession: %v", err)
	}
	if !ok {
		t.Fatal("MasterMindBySession found no row for the moved session")
	}
	if p.ID != id || p.HarnessKind != "claude" {
		t.Errorf("row = %+v, want id %s kind claude", p, id)
	}
	if p.TranscriptLocator == nil || *p.TranscriptLocator != "/tmp/t1.jsonl" {
		t.Errorf("transcript_locator = %v, want the existing /tmp/t1.jsonl kept", p.TranscriptLocator)
	}
	if !p.LastSeen.Equal(t2) {
		t.Errorf("last_seen = %v, want %v", p.LastSeen, t2)
	}
	if !p.FirstSeen.Equal(t1) {
		t.Errorf("first_seen = %v, want %v (unchanged)", p.FirstSeen, t1)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM mastermind`).Scan(&count); err != nil {
		t.Fatalf("count mastermind: %v", err)
	}
	if count != 1 {
		t.Errorf("mastermind has %d rows, want 1", count)
	}
}

// TestUpsertMasterMindByIDConflictingNaturalKey pins the guard the natural-key
// unique index gives: one (harness_kind, session_id) can never belong to two
// ids.
func TestUpsertMasterMindByIDConflictingNaturalKey(t *testing.T) {
	d := openTestDB(t)
	t1 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)

	// A row that predates the record ids: UpsertMasterMind mints it.
	existing, err := d.UpsertMasterMind(MasterMind{HarnessKind: "claude", SessionID: "sess-1", FirstSeen: t1, LastSeen: t1})
	if err != nil {
		t.Fatalf("UpsertMasterMind: %v", err)
	}

	_, err = d.UpsertMasterMind(MasterMind{
		ID:          "pl_aaaaaaaaaaaa",
		HarnessKind: "claude",
		SessionID:   "sess-1",
		LastSeen:    t1,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("UpsertMasterMind by id onto a taken natural key = %v, want ErrInvalid", err)
	}

	p, ok, err := d.MasterMindBySession("claude", "sess-1")
	if err != nil {
		t.Fatalf("MasterMindBySession: %v", err)
	}
	if !ok || p.ID != existing {
		t.Errorf("row = %+v (ok %v), want the original id %s", p, ok, existing)
	}

	var count int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM mastermind`).Scan(&count); err != nil {
		t.Fatalf("count mastermind: %v", err)
	}
	if count != 1 {
		t.Errorf("mastermind has %d rows, want 1", count)
	}
}

func TestMasterMindBySession(t *testing.T) {
	d := openTestDB(t)
	t1 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)

	id, err := d.UpsertMasterMind(MasterMind{
		HarnessKind:       "claude",
		SessionID:         "sess-1",
		TranscriptLocator: ptr("/tmp/t.jsonl"),
		FirstSeen:         t1,
		LastSeen:          t2,
	})
	if err != nil {
		t.Fatalf("UpsertMasterMind: %v", err)
	}

	p, ok, err := d.MasterMindBySession("claude", "sess-1")
	if err != nil {
		t.Fatalf("MasterMindBySession: %v", err)
	}
	if !ok {
		t.Fatal("MasterMindBySession reported no row")
	}
	if p.ID != id || p.HarnessKind != "claude" || p.SessionID != "sess-1" {
		t.Errorf("row = %+v", p)
	}
	if p.TranscriptLocator == nil || *p.TranscriptLocator != "/tmp/t.jsonl" {
		t.Errorf("transcript_locator = %v", p.TranscriptLocator)
	}
	if !p.FirstSeen.Equal(t1) || !p.LastSeen.Equal(t2) {
		t.Errorf("times = %v..%v, want %v..%v", p.FirstSeen, p.LastSeen, t1, t2)
	}

	// The kind is part of the key, and a miss is not an error.
	if _, ok, err := d.MasterMindBySession("opencode", "sess-1"); err != nil || ok {
		t.Errorf("MasterMindBySession(other kind) = ok %v, err %v; want no row", ok, err)
	}
	if _, ok, err := d.MasterMindBySession("claude", "nope"); err != nil || ok {
		t.Errorf("MasterMindBySession(unknown) = ok %v, err %v; want no row and no error", ok, err)
	}
}

// TestMasterMindBySessionIsOriginScoped pins the natural-key lookup to the
// handle's origin, like the (origin, harness_kind, session_id) index the upsert
// guards: on a machine database carrying more than one installation, a session
// another installation registered must not answer for this one.
func TestMasterMindBySessionIsOriginScoped(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.sqlDB.Exec(`INSERT INTO mastermind (id, origin, harness_kind, session_id, first_seen, last_seen)
		VALUES ('pl_other', 'other-installation', 'claude', 'sess-1', '2026-09-01T10:00:00.000Z', '2026-09-01T10:00:00.000Z')`); err != nil {
		t.Fatalf("seed other-origin row: %v", err)
	}

	if _, ok, err := d.MasterMindBySession("claude", "sess-1"); err != nil || ok {
		t.Errorf("MasterMindBySession over another origin = ok %v, err %v; want no row", ok, err)
	}

	if _, err := d.UpsertMasterMind(MasterMind{HarnessKind: "claude", SessionID: "sess-1"}); err != nil {
		t.Fatalf("UpsertMasterMind: %v", err)
	}
	if _, ok, err := d.MasterMindBySession("claude", "sess-1"); err != nil || !ok {
		t.Errorf("MasterMindBySession over this origin = ok %v, err %v; want the row", ok, err)
	}
}
