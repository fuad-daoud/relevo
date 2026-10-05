package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
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

// TestBackfillOriginOnceStampsEveryGateTable pins the pass against the gate it
// exists for: one empty-origin row in each table the gate counts becomes this
// installation's, so every count the gate reports is zero afterwards and the
// gate itself passes. A table the pass left out would leave its count standing
// and the enable refusal would name a fix that could not clear it.
func TestBackfillOriginOnceStampsEveryGateTable(t *testing.T) {
	d := directOpenTestDB(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	seedOneEmptyOriginRowPerTable(t, d)

	before, err := CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins before the pass: %v", err)
	}
	for _, table := range before.Tables {
		if table.Rows != 1 {
			t.Fatalf("%s holds %d unstamped rows before the pass, want 1", table.Table, table.Rows)
		}
	}
	if EnablePreflight(d).OK() {
		t.Fatal("the preflight passes on a database holding an unstamped row in every table")
	}

	stats, ran, err := BackfillOriginOnce(d, "01ORIGIN", now)
	if err != nil {
		t.Fatalf("BackfillOriginOnce: %v", err)
	}
	if !ran {
		t.Fatal("first run reported ran = false")
	}

	reported := map[string]int64{
		"binding_record": stats.BindingRecords,
		"binding":        stats.Bindings,
		"repo":           stats.Repos,
		"mastermind":     stats.Masterminds,
		"chains":         stats.Chains,
	}
	after, err := CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins after the pass: %v", err)
	}
	for _, table := range after.Tables {
		if table.Rows != 0 {
			t.Errorf("%s holds %d unstamped rows after the pass, want 0", table.Table, table.Rows)
		}
		if got, ok := reported[table.Table]; !ok || got != 1 {
			t.Errorf("the pass reported %d row(s) stamped in %s, want 1", got, table.Table)
		}
	}
	if !after.Empty() {
		t.Errorf("counts after the pass = %s, want none", after)
	}

	// One pass, one marker: a second run stamps nothing and reports no rows,
	// which is what makes the retry after a failure safe.
	second, ran, err := BackfillOriginOnce(d, "01ORIGIN", now)
	if err != nil {
		t.Fatalf("second BackfillOriginOnce: %v", err)
	}
	if ran {
		t.Error("second run reported ran = true, want a no-op")
	}
	if second.BindingRecords != 0 || second.Repos != 0 || second.Masterminds != 0 || second.Chains != 0 {
		t.Errorf("the no-op run reported rows: %+v", second)
	}
}

// TestBackfillTablesMatchTheOriginGate pins the two lists against each other.
// The gate decides whether an enable is refused, so a table it counts that the
// backfill does not stamp is a refusal no fix can clear; and a table the
// backfill stamps that the gate does not count is a row moved for nothing. Every
// table must also report into the stats, or the daemon's log line would
// understate what the pass did.
//
// It pins the twin rule's two tables the same way. A table the pass stamps with
// no rule is a table whose collision still aborts the whole pass, which is the
// failure this pass exists to stop; and a rule for a table the pass does not
// stamp is a case no row can reach, so it is a rule that will never be exercised
// and never tested.
func TestBackfillTablesMatchTheOriginGate(t *testing.T) {
	tables := backfillTables()
	if len(tables) != len(originGateTables) {
		t.Fatalf("the backfill stamps %v, the gate counts %v", tables, originGateTables)
	}
	stats := OriginBackfillStats{}
	for i, table := range originGateTables {
		if tables[i] != table {
			t.Errorf("the backfill stamps %d %q, the gate counts %q", i, tables[i], table)
		}
		if stats.countFor(table) == nil {
			t.Errorf("%s has no stats field, so its rows would not be reported", table)
		}
		if _, ok := backfillTwinRule[table]; !ok {
			t.Errorf("%s has no twin rule, so a stale twin there still aborts the pass", table)
		}
		if len(twinKeysFor(table)) == 0 {
			t.Errorf("%s has no unique index in backfillTwinKeys, so a twin there could not be found before the stamp raised it", table)
		}
	}
	for table := range backfillTwinRule {
		if !slices.Contains(tables, table) {
			t.Errorf("backfillTwinRule carries %q, which the pass does not stamp", table)
		}
	}
	for _, key := range backfillTwinKeys {
		if !slices.Contains(tables, key.table) {
			t.Errorf("backfillTwinKeys carries %s on %q, which the pass does not stamp", key.name, key.table)
		}
	}
}

// TestBackfillTwinKeysMatchTheSchema pins the index list against the schema it
// is read from. A key the schema does not have is a case that can never be
// reached, and a key the schema has but this list omits is exactly the collision
// the field report found: one stale row silently aborting the whole pass.
func TestBackfillTwinKeysMatchTheSchema(t *testing.T) {
	d := directOpenTestDB(t)
	for _, key := range backfillTwinKeys {
		var sqlText string
		err := d.sqlDB.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, key.name).Scan(&sqlText)
		if err != nil {
			t.Errorf("index %s is not in the schema: %v", key.name, err)
			continue
		}
		for _, col := range key.cols {
			if !strings.Contains(sqlText, col) {
				t.Errorf("index %s does not key on %q, which the twin rule looks for", key.name, col)
			}
		}
		if !strings.Contains(sqlText, "origin") {
			t.Errorf("index %s does not key on origin, so no stamp can collide on it", key.name)
		}
	}

	// Every unique index the schema holds over an origin-carrying table that the
	// pass stamps is in the list: one left out is a collision nothing previews.
	for _, table := range backfillTables() {
		rows, err := d.sqlDB.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND sql LIKE '%UNIQUE%'`, table)
		if err != nil {
			t.Fatalf("list indexes on %s: %v", table, err)
		}
		for rows.Next() {
			var name, sqlText string
			if err := rows.Scan(&name, &sqlText); err != nil {
				t.Fatalf("scan index on %s: %v", table, err)
			}
			if !strings.Contains(sqlText, "origin") {
				continue // keyed without origin: a stamp cannot change its key.
			}
			if !slices.ContainsFunc(backfillTwinKeys, func(k backfillTwinKey) bool { return k.name == name }) {
				t.Errorf("%s holds unique index %s over origin, which backfillTwinKeys does not carry", table, name)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate indexes on %s: %v", table, err)
		}
		_ = rows.Close()
	}
}

// TestBackfillResolvesStaleRepoTwins is the field report's shape: one checkout
// recorded twice, a stale ” repo row and this installation's stamped live row
// holding the same common_dir across a rename. The pass completes rather than
// aborting on the pair, the live row is left byte for byte as it was, the stale
// row loses to it, and the other rows in the same table are stamped rather than
// rolled back with the pair.
func TestBackfillResolvesStaleRepoTwins(t *testing.T) {
	d := directOpenTestDB(t)
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	const origin = "01M3ORIGINORIGINORIGINORIGIN"

	// The two URLs are the field report's own: one checkout recorded under its old
	// name and one under its new, so the pair differs in origin_url and collides
	// on common_dir.
	dir := "/home/fuad/projects/relevo-site/.git"
	staleURL := "https://github.com/fuad-daoud/relay-site" // name-guard: legacy
	liveURL := "https://github.com/fuad-daoud/relevo-site"

	if _, err := d.sqlDB.Exec(`INSERT INTO repo (id, origin, origin_url, common_dir, first_seen)
		VALUES ('r-stale', '', ?, ?, '2026-09-01T10:00:00.000Z')`, staleURL, dir); err != nil {
		t.Fatalf("insert the stale repo row: %v", err)
	}
	if _, err := d.sqlDB.Exec(`INSERT INTO repo (id, origin, origin_url, common_dir, first_seen)
		VALUES ('r-live', ?, ?, ?, '2026-09-20T10:00:00.000Z')`, origin, liveURL, dir); err != nil {
		t.Fatalf("insert the live repo row: %v", err)
	}
	// A clean row beside the pair, so the pass has something in this table that
	// is only reachable if the pair did not roll the table back.
	if _, err := d.sqlDB.Exec(`INSERT INTO repo (id, origin, origin_url, common_dir, first_seen)
		VALUES ('r-clean', '', 'https://github.com/acme/other.git', '/home/fuad/projects/other/.git', '2026-09-02T10:00:00.000Z')`); err != nil {
		t.Fatalf("insert the clean repo row: %v", err)
	}

	before, err := CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins before the pass: %v", err)
	}
	if before.Tables[2].Rows != 2 {
		t.Fatalf("repo holds %d unstamped rows before the pass, want 2", before.Tables[2].Rows)
	}

	stats, ran, err := BackfillOriginOnce(d, origin, now)
	if err != nil {
		t.Fatalf("BackfillOriginOnce: %v", err)
	}
	if !ran {
		t.Fatal("first run reported ran = false")
	}
	if stats.Repos != 1 {
		t.Errorf("the pass reported %d repo rows stamped, want 1 (the clean one)", stats.Repos)
	}
	if stats.Dropped() != 1 {
		t.Errorf("the pass dropped %d stale rows, want 1", stats.Dropped())
	}
	if stats.Halted() != 0 {
		t.Errorf("the pass halted on %d rows, want 0", stats.Halted())
	}

	// The stale row is gone and the live row is untouched: the rule resolved in
	// favour of the stamped row, not the other way round.
	var staleLeft int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM repo WHERE id = 'r-stale'`).Scan(&staleLeft); err != nil {
		t.Fatalf("count the stale repo row: %v", err)
	}
	if staleLeft != 0 {
		t.Error("the stale '' repo row survived the pass, want it dropped for the stamped live row")
	}
	var liveOrigin, liveURLGot, liveDir, liveSeen string
	if err := d.sqlDB.QueryRow(`SELECT origin, origin_url, common_dir, first_seen FROM repo WHERE id = 'r-live'`).
		Scan(&liveOrigin, &liveURLGot, &liveDir, &liveSeen); err != nil {
		t.Fatalf("select the live repo row: %v", err)
	}
	if liveOrigin != origin || liveURLGot != liveURL || liveDir != dir || liveSeen != "2026-09-20T10:00:00.000Z" {
		t.Errorf("the live repo row = origin %q url %q dir %q first_seen %q, want it byte-identical to what it was",
			liveOrigin, liveURLGot, liveDir, liveSeen)
	}

	after, err := CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins after the pass: %v", err)
	}
	if !after.Empty() {
		t.Errorf("counts after the pass = %s, want none: the gate would refuse an enable on a resolved database", after)
	}
}

// TestBackfillHaltsOnABindingRecordTwin pins the case the rule declines. The
// binding_record table is the system of record, so a twin there is not resolved
// in code: the stale row stays unstamped, the live row is left alone, and the
// pass returns an error naming the table and the surviving count. What the pass
// did settle before the halt stays settled, which is the whole point of the
// per-table transaction.
func TestBackfillHaltsOnABindingRecordTwin(t *testing.T) {
	d := directOpenTestDB(t)
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	const origin = "01M3ORIGINORIGINORIGINORIGIN"

	// The same live name twice: the '' row and this installation's stamped row.
	for _, row := range []struct{ id, origin, state, cwd string }{
		{"br-stale", "", "active", "/work/old"},
		{"br-live", origin, "needs_you", "/work/live"},
	} {
		if _, err := d.sqlDB.Exec(`INSERT INTO binding_record (id, origin, owner, name, state, round, cwd, record_json, created_at, updated_at)
			VALUES (?, ?, '', 'api', ?, 1, ?, '{}', '2026-09-01T10:00:00.000Z', '2026-09-01T10:00:00.000Z')`,
			row.id, row.origin, row.state, row.cwd); err != nil {
			t.Fatalf("insert %s: %v", row.id, err)
		}
	}
	// A clean row in a later table, so the halt is shown not to roll it back.
	upsertRepo(t, d, Repo{OriginURL: ptr("https://example.test/a.git"), FirstSeen: now})
	if _, err := d.sqlDB.Exec(`UPDATE repo SET origin = '' WHERE origin_url = ?`, "https://example.test/a.git"); err != nil {
		t.Fatalf("make the repo row unstamped: %v", err)
	}

	stats, ran, err := BackfillOriginOnce(d, origin, now)
	if err == nil {
		t.Fatal("the pass returned no error on a binding_record twin, want a halt for a person")
	}
	if ran {
		t.Error("a halted pass reported ran = true, want false: it recorded nothing")
	}
	if !errors.Is(err, ErrInvalid) && !strings.Contains(err.Error(), "binding_record=1") {
		t.Errorf("the halt names neither the table nor the surviving count: %v", err)
	}
	if stats.Halted() != 1 {
		t.Errorf("the pass halted on %d rows, want 1", stats.Halted())
	}
	if stats.Dropped() != 0 {
		t.Errorf("the pass dropped %d rows, want 0: binding_record is never deleted", stats.Dropped())
	}
	if len(stats.LeftUnstamped) != 1 || stats.LeftUnstamped["binding_record"] != 1 {
		t.Errorf("LeftUnstamped = %v, want binding_record=1", stats.LeftUnstamped)
	}

	// Nothing was deleted: both records are still there, the stale one unstamped
	// for a person to decide on and the live one exactly as it was.
	var staleOrigin, liveOrigin, liveCWD string
	if err := d.sqlDB.QueryRow(`SELECT origin FROM binding_record WHERE id = 'br-stale'`).Scan(&staleOrigin); err != nil {
		t.Fatalf("select the stale record: %v", err)
	}
	if staleOrigin != "" {
		t.Errorf("the stale record was stamped as %q, want it left unstamped for a person", staleOrigin)
	}
	if err := d.sqlDB.QueryRow(`SELECT origin, cwd FROM binding_record WHERE id = 'br-live'`).Scan(&liveOrigin, &liveCWD); err != nil {
		t.Fatalf("select the live record: %v", err)
	}
	if liveOrigin != origin || liveCWD != "/work/live" {
		t.Errorf("the live record = origin %q cwd %q, want it untouched", liveOrigin, liveCWD)
	}
	var records int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM binding_record`).Scan(&records); err != nil {
		t.Fatalf("count binding_record rows: %v", err)
	}
	if records != 2 {
		t.Errorf("binding_record holds %d rows, want both: the record table is never deleted from", records)
	}

	assertHaltKeptTheSettledTables(t, d, stats, origin)
}

// assertHaltKeptTheSettledTables pins what a halt must not take with it: the
// tables the pass settled before it stopped keep their stamps and are committed,
// and no marker is written, so the next start resumes rather than declaring the
// pass done over a database that still holds unstamped rows.
func assertHaltKeptTheSettledTables(t *testing.T, d *DB, stats OriginBackfillStats, origin string) {
	t.Helper()
	if stats.Repos != 1 {
		t.Errorf("the pass reported %d repo rows stamped, want 1: a halt must not roll back a settled table", stats.Repos)
	}
	var repoOrigin string
	if err := d.sqlDB.QueryRow(`SELECT origin FROM repo`).Scan(&repoOrigin); err != nil {
		t.Fatalf("select the repo row: %v", err)
	}
	if repoOrigin != origin {
		t.Errorf("the repo row's origin = %q, want %q kept across the halt", repoOrigin, origin)
	}
	if _, ok, err := d.KVGet(originBackfillKVKey); err != nil || ok {
		t.Errorf("kv row %s = (_, %v, %v), want it absent so the next start resumes", originBackfillKVKey, ok, err)
	}
}

// TestBackfillResumesAfterAHalt pins that a second start over a half-finished
// database picks the work up rather than starting over or skipping: the tables
// the first pass settled report no rows to stamp, the row it left for a person is
// still the only unstamped row, and once that row is stamped the pass records
// itself.
func TestBackfillResumesAfterAHalt(t *testing.T) {
	d := directOpenTestDB(t)
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	const origin = "01M3ORIGINORIGINORIGINORIGIN"

	seedOneEmptyOriginRowPerTable(t, d)
	if _, err := d.RecordPut(testRecord("api")); err != nil {
		t.Fatalf("RecordPut the second record: %v", err)
	}

	first, ran, err := BackfillOriginOnce(d, origin, now)
	if err != nil || !ran {
		t.Fatalf("first BackfillOriginOnce = (_, %v, %v), want it to finish", ran, err)
	}
	if first.Stamped() != 6 {
		t.Errorf("the first pass stamped %d rows, want 6 (five tables, one twice)", first.Stamped())
	}
	if _, ok, err := d.KVGet(originBackfillKVKey); err != nil || !ok {
		t.Fatalf("kv row %s = (_, %v, %v), want it recorded", originBackfillKVKey, ok, err)
	}

	// A settled database: the second start has nothing to stamp and reports
	// neither a re-stamp nor a skip.
	second, ran, err := BackfillOriginOnce(d, origin, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second BackfillOriginOnce: %v", err)
	}
	if ran {
		t.Error("the second start reported ran = true, want a no-op")
	}
	if second.Stamped() != 0 {
		t.Errorf("the second start reported %d rows stamped, want 0", second.Stamped())
	}

	// A half-finished database: clear one table's stamps and drop the marker, the
	// shape a pass interrupted before its last write leaves behind.
	if _, err := d.sqlDB.Exec(`DELETE FROM kv WHERE key = ?`, originBackfillKVKey); err != nil {
		t.Fatalf("clear the marker: %v", err)
	}
	if _, err := d.sqlDB.Exec(`UPDATE repo SET origin = ''`); err != nil {
		t.Fatalf("unstamp the repo rows: %v", err)
	}
	third, ran, err := BackfillOriginOnce(d, origin, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("third BackfillOriginOnce: %v", err)
	}
	if !ran {
		t.Fatal("the resumed pass reported ran = false, want it to finish the work")
	}
	if third.Repos != 1 {
		t.Errorf("the resumed pass reported %d repo rows stamped, want 1", third.Repos)
	}
	// The tables the first pass settled were not stamped again.
	if third.BindingRecords != 0 || third.Bindings != 0 || third.Masterminds != 0 || third.Chains != 0 {
		t.Errorf("the resumed pass re-stamped settled tables: %+v", third)
	}
	if _, ok, err := d.KVGet(originBackfillKVKey); err != nil || !ok {
		t.Errorf("kv row %s = (_, %v, %v), want the resumed pass to record itself", originBackfillKVKey, ok, err)
	}
	after, err := CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins after the resumed pass: %v", err)
	}
	if !after.Empty() {
		t.Errorf("counts after the resumed pass = %s, want none", after)
	}
}

// TestCollidingRowIDsFindsOnlyRealTwins pins the query the rule rests on: it
// names exactly the unstamped rows whose stamp would raise the unique index,
// including a row that collides on both of its table's indexes, and it does not
// name a row whose key is free or whose column the index's predicate leaves out.
func TestCollidingRowIDsFindsOnlyRealTwins(t *testing.T) {
	d := directOpenTestDB(t)
	const origin = "01M3ORIGINORIGINORIGINORIGIN"
	day := "2026-09-01T10:00:00.000Z"

	seed := []struct {
		id, origin, url, dir string
		nullURL              bool
	}{
		{"r-twin-dir", "", "https://example.test/old.git", "/work/.git", false},
		{"r-live-dir", origin, "https://example.test/new.git", "/work/.git", false},
		{"r-twin-url", "", "https://example.test/same.git", "/other/.git", false},
		{"r-live-url", origin, "https://example.test/same.git", "/another/.git", false},
		// Both of its indexes at once, against one live row that keys alike on
		// both: the same shape twice over, which is why the ids are deduped.
		{"r-twin-both", "", "https://example.test/dup.git", "/dup/.git", false},
		{"r-live-both", origin, "https://example.test/dup.git", "/dup/.git", false},
		// A NULL never collides: neither index covers this row.
		{"r-null", "", "", "/unique-null/.git", true},
		// A third origin's live row is a different namespace: no collision.
		{"r-other", "01OTHERORIGIN", "https://example.test/other.git", "/other-origin/.git", false},
	}
	for _, r := range seed {
		var url any
		if !r.nullURL {
			url = r.url
		}
		if _, err := d.sqlDB.Exec(`INSERT INTO repo (id, origin, origin_url, common_dir, first_seen)
			VALUES (?, ?, ?, ?, ?)`, r.id, r.origin, url, r.dir, day); err != nil {
			t.Fatalf("insert %s: %v", r.id, err)
		}
	}

	want := map[string][]string{
		"repo_origin_origin_url_uidx": {"r-twin-url", "r-twin-both"},
		"repo_origin_common_dir_uidx": {"r-twin-dir", "r-twin-both"},
	}
	for _, key := range twinKeysFor("repo") {
		ids, err := collidingRowIDs(d.sqlDB, "repo", origin, key)
		if err != nil {
			t.Fatalf("collidingRowIDs on %s: %v", key.name, err)
		}
		slices.Sort(ids)
		expect := want[key.name]
		slices.Sort(expect)
		if !slices.Equal(ids, expect) {
			t.Errorf("%s: colliding ids = %v, want %v", key.name, ids, expect)
		}
	}
}
