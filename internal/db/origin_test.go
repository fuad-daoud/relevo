package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// seedV13Rows writes one row into each table migration 014 adds an origin to,
// at the schema version before 014 exists.
func seedV13Rows(t *testing.T, sqlDB *sql.DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json, created_at, updated_at)
			VALUES ('br1', '', 'api', 'active', 1, '/work/api', '{}', '2026-09-01T10:00:00.000Z', '2026-09-01T10:00:00.000Z')`,
		`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source)
			VALUES ('b1', 'api', '/work/api', 'pane', '2026-09-01T10:00:00.000Z', 'live')`,
		`INSERT INTO repo (id, origin_url, first_seen)
			VALUES ('r1', 'git@example.com:acme/api.git', '2026-09-01T10:00:00.000Z')`,
		`INSERT INTO mastermind (id, harness_kind, session_id, first_seen, last_seen)
			VALUES ('m1', 'claude', 's1', '2026-09-01T10:00:00.000Z', '2026-09-01T10:00:00.000Z')`,
	}
	for _, stmt := range stmts {
		if _, err := sqlDB.Exec(stmt); err != nil {
			t.Fatalf("seed row: %v\n%s", err, stmt)
		}
	}
}

// assertIndexes checks that every index in present exists and every index in
// gone does not.
func assertIndexes(t *testing.T, sqlDB *sql.DB, present, gone []string) {
	t.Helper()
	for _, idx := range present {
		var name string
		if err := sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&name); err != nil {
			t.Errorf("index %s is missing: %v", idx, err)
		}
	}
	for _, idx := range gone {
		var name string
		if err := sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&name); err == nil {
			t.Errorf("index %s survived the re-key", idx)
		}
	}
}

// openTwoOrigins opens one database file through two handles with distinct
// origins, so a test can show what each installation sees and writes.
func openTwoOrigins(t *testing.T, path string) (*DB, *DB) {
	t.Helper()
	a, err := OpenWith(path, Options{Origin: "01AAAAAAAAAAAAAAAAAAAAAAAA"})
	if err != nil {
		t.Fatalf("OpenWith a: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := OpenWith(path, Options{Origin: "01BBBBBBBBBBBBBBBBBBBBBBBB"})
	if err != nil {
		t.Fatalf("OpenWith b: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return a, b
}

// TestMigration014AddsOriginAndKeepsRows pins migration 014 against a database
// at 013: every old row keeps its data with an empty origin, the installation
// table arrives, the live indexes are re-keyed, and a second apply is a no-op.
func TestMigration014AddsOriginAndKeepsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	sqlDB := rawSQLDB(t, path)

	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 13)); err != nil {
		t.Fatalf("applyMigrations through 013: %v", err)
	}
	seedV13Rows(t, sqlDB)

	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 14)); err != nil {
		t.Fatalf("applyMigrations 014: %v", err)
	}

	for _, tc := range []struct{ table, id, col, want string }{
		{"binding_record", "br1", "name", "api"},
		{"binding", "b1", "name", "api"},
		{"repo", "r1", "origin_url", "git@example.com:acme/api.git"},
		{"mastermind", "m1", "session_id", "s1"},
	} {
		var origin, kept string
		if err := sqlDB.QueryRow(`SELECT origin, `+tc.col+` FROM `+tc.table+` WHERE id = ?`, tc.id).Scan(&origin, &kept); err != nil {
			t.Fatalf("%s row: %v", tc.table, err)
		}
		if origin != "" {
			t.Errorf("%s origin = %q, want the empty default", tc.table, origin)
		}
		if kept != tc.want {
			t.Errorf("%s %s = %q, want %q (row kept)", tc.table, tc.col, kept, tc.want)
		}
	}

	var table string
	if err := sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'installation'`).Scan(&table); err != nil {
		t.Errorf("installation table is missing: %v", err)
	}

	assertIndexes(t, sqlDB,
		[]string{
			"binding_record_origin_owner_name_uidx",
			"binding_record_origin_idx",
			"binding_name_created_at_uidx",
			"repo_origin_origin_url_uidx",
			"repo_origin_common_dir_uidx",
			"mastermind_origin_harness_session_uidx",
		},
		[]string{
			"binding_record_owner_name_uidx",
			"repo_origin_url_uidx",
			"repo_common_dir_uidx",
			"mastermind_harness_session_uidx",
		})

	// A second apply of 014 is a no-op: the version guard means the ALTERs and
	// the index swaps never run twice.
	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 14)); err != nil {
		t.Fatalf("second applyMigrations 014: %v", err)
	}
	var rows int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_version WHERE version = 14`).Scan(&rows); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if rows != 1 {
		t.Errorf("schema_version rows for 14 = %d, want 1", rows)
	}
}

// TestInstallationTouchUpserts pins the directory row the daemon writes: the
// first touch starts it, a later touch keeps first_seen and moves the label and
// last_seen, and an empty id is refused rather than written.
func TestInstallationTouchUpserts(t *testing.T) {
	d := openTestDB(t)
	first := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)

	touch := func(id, label string, at time.Time) error {
		return d.Tx(func(tx *Tx) error { return tx.InstallationTouch(id, label, at) })
	}

	if err := touch("01INSTALLATION", "laptop", first); err != nil {
		t.Fatalf("InstallationTouch: %v", err)
	}
	if err := touch("01INSTALLATION", "zen", second); err != nil {
		t.Fatalf("second InstallationTouch: %v", err)
	}

	var label, firstSeen, lastSeen string
	if err := d.sqlDB.QueryRow(`SELECT label, first_seen, last_seen FROM installation WHERE id = ?`, "01INSTALLATION").
		Scan(&label, &firstSeen, &lastSeen); err != nil {
		t.Fatalf("select installation: %v", err)
	}
	if label != "zen" {
		t.Errorf("label = %q, want zen", label)
	}
	if firstSeen != formatTime(first) {
		t.Errorf("first_seen = %q, want %q (kept)", firstSeen, formatTime(first))
	}
	if lastSeen != formatTime(second) {
		t.Errorf("last_seen = %q, want %q", lastSeen, formatTime(second))
	}

	if err := touch("", "laptop", first); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty id error = %v, want ErrInvalid", err)
	}
}

// TestRecordOriginScoping pins the rule every scoped record query follows: a
// handle sees its own rows and the empty-origin rows that predate the column,
// and two installations may each hold a live binding of the same name.
func TestRecordOriginScoping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	a, b := openTwoOrigins(t, path)

	if _, err := a.RecordPut(Record{Owner: "", Name: "api", State: "active", Round: 1, CWD: "/work/a"}); err != nil {
		t.Fatalf("a RecordPut: %v", err)
	}
	if _, ok, err := b.RecordGet("", "api"); err != nil || ok {
		t.Fatalf("b RecordGet before its own put = (_, %v, %v), want no row", ok, err)
	}

	// The live uniqueness key is (origin, owner, name): b can hold a live
	// binding of the same name without touching a's row.
	if _, err := b.RecordPut(Record{Owner: "", Name: "api", State: "needs_you", Round: 3, CWD: "/work/b"}); err != nil {
		t.Fatalf("b RecordPut: %v", err)
	}
	got, ok, err := a.RecordGet("", "api")
	if err != nil || !ok {
		t.Fatalf("a RecordGet = (_, %v, %v), want its own row", ok, err)
	}
	if got.CWD != "/work/a" || got.Round != 1 {
		t.Errorf("a's row = cwd %q round %d, want /work/a round 1", got.CWD, got.Round)
	}
	gotB, ok, err := b.RecordGet("", "api")
	if err != nil || !ok {
		t.Fatalf("b RecordGet = (_, %v, %v), want its own row", ok, err)
	}
	if gotB.CWD != "/work/b" {
		t.Errorf("b's row = cwd %q, want /work/b", gotB.CWD)
	}

	// A row written before the origin column existed is in scope for both.
	plain, err := OpenWith(path, Options{})
	if err != nil {
		t.Fatalf("OpenWith plain: %v", err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	if _, err := plain.RecordPut(Record{Owner: "", Name: "legacy", State: "active", Round: 1, CWD: "/work/old"}); err != nil {
		t.Fatalf("plain RecordPut: %v", err)
	}
	for name, d := range map[string]*DB{"a": a, "b": b, "plain": plain} {
		if _, ok, err := d.RecordGet("", "legacy"); err != nil || !ok {
			t.Errorf("%s RecordGet(legacy) = (_, %v, %v), want the empty-origin row", name, ok, err)
		}
	}

	// An unscoped handle sees only the empty-origin rows, while each scoped
	// handle sees its own rows as well.
	list, err := plain.RecordList("")
	if err != nil {
		t.Fatalf("plain RecordList: %v", err)
	}
	if len(list) != 1 || list[0].Name != "legacy" {
		t.Errorf("plain RecordList = %+v, want only legacy", list)
	}
	for name, d := range map[string]*DB{"a": a, "b": b} {
		list, err := d.RecordList("")
		if err != nil {
			t.Fatalf("%s RecordList: %v", name, err)
		}
		if len(list) != 2 {
			t.Errorf("%s RecordList = %+v, want its own row and legacy", name, list)
		}
	}
}

// TestBackfillOriginOnceStampsOldRows pins the one-time pass: empty-origin
// rows become this installation's, the pass records itself in kv, and a
// second run does nothing.
func TestBackfillOriginOnceStampsOldRows(t *testing.T) {
	d := openTestDB(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	if _, err := d.RecordPut(Record{Owner: "", Name: "api", State: "active", Round: 1, CWD: "/work/api"}); err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	if _, err := d.UpsertBinding(Binding{
		Name: "api", CWD: "/work/api", BuilderMode: "pane", CreatedAt: now.Add(-time.Hour), IngestSource: IngestLive,
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	stats, ran, err := BackfillOriginOnce(d, "01ORIGIN", now)
	if err != nil {
		t.Fatalf("BackfillOriginOnce: %v", err)
	}
	if !ran {
		t.Fatal("first run reported ran = false")
	}
	if stats.BindingRecords != 1 || stats.Bindings != 1 {
		t.Errorf("stats = %+v, want one binding_record and one binding", stats)
	}

	for _, table := range []string{"binding_record", "binding"} {
		var stamped, empties int
		if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE origin = ?`, "01ORIGIN").Scan(&stamped); err != nil {
			t.Fatalf("count stamped in %s: %v", table, err)
		}
		if stamped != 1 {
			t.Errorf("%s rows with the new origin = %d, want 1", table, stamped)
		}
		if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE origin = ''`).Scan(&empties); err != nil {
			t.Fatalf("count empty origins in %s: %v", table, err)
		}
		if empties != 0 {
			t.Errorf("%s rows with an empty origin = %d, want 0", table, empties)
		}
	}

	_, ran, err = BackfillOriginOnce(d, "01ORIGIN", now)
	if err != nil {
		t.Fatalf("second BackfillOriginOnce: %v", err)
	}
	if ran {
		t.Error("second run reported ran = true, want a no-op")
	}
	if _, ok, err := d.KVGet(originBackfillKVKey); err != nil || !ok {
		t.Errorf("kv row %s = (_, %v, %v), want it recorded", originBackfillKVKey, ok, err)
	}
}

// TestUpsertBindingIsScopedByOrigin pins the mirror binding's natural key,
// (origin, name, created_at): two installations' ingests of one binding are two
// rows, while one origin's second upsert still updates its own row.
func TestUpsertBindingIsScopedByOrigin(t *testing.T) {
	a, b := openTwoOrigins(t, filepath.Join(t.TempDir(), "relevo.db"))
	created := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	binding := Binding{Name: "api", CWD: "/work/api", BuilderMode: "pane", CreatedAt: created, IngestSource: IngestLive}

	idA, err := a.UpsertBinding(binding)
	if err != nil {
		t.Fatalf("a UpsertBinding: %v", err)
	}
	idB, err := b.UpsertBinding(binding)
	if err != nil {
		t.Fatalf("b UpsertBinding: %v", err)
	}
	if idA == idB {
		t.Errorf("both origins got mirror binding id %q, want two rows", idA)
	}

	binding.CWD = "/work/api2"
	againA, err := a.UpsertBinding(binding)
	if err != nil {
		t.Fatalf("a second UpsertBinding: %v", err)
	}
	if againA != idA {
		t.Errorf("second upsert of one origin's binding = %q, want %q", againA, idA)
	}
	var mirrorBindings int
	if err := a.sqlDB.QueryRow(`SELECT COUNT(*) FROM binding WHERE name = 'api'`).Scan(&mirrorBindings); err != nil {
		t.Fatalf("count mirror bindings: %v", err)
	}
	if mirrorBindings != 2 {
		t.Errorf("mirror binding rows = %d, want 2 (one per origin)", mirrorBindings)
	}
}

// TestUpsertRepoAndMasterMindAreScopedByOrigin pins the same per-origin rule
// for the mirror's repo and session tables, whose unique indexes migration 014
// re-keys by origin.
func TestUpsertRepoAndMasterMindAreScopedByOrigin(t *testing.T) {
	a, b := openTwoOrigins(t, filepath.Join(t.TempDir(), "relevo.db"))
	created := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	url := "git@example.com:acme/api.git"
	repoA, err := a.UpsertRepo(Repo{OriginURL: &url, FirstSeen: created})
	if err != nil {
		t.Fatalf("a UpsertRepo: %v", err)
	}
	repoB, err := b.UpsertRepo(Repo{OriginURL: &url, FirstSeen: created})
	if err != nil {
		t.Fatalf("b UpsertRepo: %v", err)
	}
	if repoA == repoB {
		t.Errorf("both origins got mirror repo id %q, want two rows", repoA)
	}
	againRepoA, err := a.UpsertRepo(Repo{OriginURL: &url, FirstSeen: created})
	if err != nil {
		t.Fatalf("a second UpsertRepo: %v", err)
	}
	if againRepoA != repoA {
		t.Errorf("second upsert of one origin's repo = %q, want %q", againRepoA, repoA)
	}

	session := MasterMind{HarnessKind: "claude", SessionID: "s1", FirstSeen: created, LastSeen: created}
	mmA, err := a.UpsertMasterMind(session)
	if err != nil {
		t.Fatalf("a UpsertMasterMind: %v", err)
	}
	mmB, err := b.UpsertMasterMind(session)
	if err != nil {
		t.Fatalf("b UpsertMasterMind: %v", err)
	}
	if mmA == mmB {
		t.Errorf("both origins got mastermind id %q, want two rows", mmA)
	}
	againMMA, err := a.UpsertMasterMind(session)
	if err != nil {
		t.Fatalf("a second UpsertMasterMind: %v", err)
	}
	if againMMA != mmA {
		t.Errorf("second upsert of one origin's session = %q, want %q", againMMA, mmA)
	}
}
