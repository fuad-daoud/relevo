package db

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

// outboxRow is one entry a shared table's triggers wrote, as a test reads it:
// the table, the primary key the trigger recorded, the operation and the owning
// installation the trigger resolved.
type outboxRow struct {
	tbl    string
	pk     string
	op     string
	origin sql.NullString
}

// outboxMark returns the sequence number the outbox has reached, so a shape can
// be pinned against the entries it wrote itself rather than against the rows a
// fixture laid down to give it a parent.
func outboxMark(t *testing.T, sqlDB *sql.DB) int {
	t.Helper()
	var seq int
	if err := sqlDB.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM sync_outbox`).Scan(&seq); err != nil {
		t.Fatalf("read sync_outbox mark: %v", err)
	}
	return seq
}

// outboxRows returns every entry written after mark, in the order the outbox
// hands them to a drain.
func outboxRows(t *testing.T, sqlDB *sql.DB, mark int) []outboxRow {
	t.Helper()
	rows, err := sqlDB.Query(`SELECT tbl, pk, op, origin FROM sync_outbox WHERE seq > ? ORDER BY seq`, mark)
	if err != nil {
		t.Fatalf("read sync_outbox: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var entries []outboxRow
	for rows.Next() {
		var e outboxRow
		if err := rows.Scan(&e.tbl, &e.pk, &e.op, &e.origin); err != nil {
			t.Fatalf("scan sync_outbox: %v", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sync_outbox: %v", err)
	}
	return entries
}

// execAll runs each statement, so a failing one names itself.
func execAll(t *testing.T, sqlDB *sql.DB, stmts ...string) {
	t.Helper()
	for _, stmt := range stmts {
		if _, err := sqlDB.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// wantEntries compares the entries written after mark against what a write shape
// must leave. The comparison is exact in both directions and on every field: an
// entry a shape adds is a defect, and an entry it leaves out is a lost write.
func wantEntries(t *testing.T, sqlDB *sql.DB, mark int, want ...string) {
	t.Helper()
	got := outboxRows(t, sqlDB, mark)

	var rendered []string
	for _, e := range got {
		origin := "NULL"
		if e.origin.Valid {
			origin = e.origin.String
		}
		rendered = append(rendered, e.tbl+" "+e.pk+" "+e.op+" "+origin)
	}
	if strings.Join(rendered, " | ") != strings.Join(want, " | ") {
		t.Errorf("sync_outbox holds\n  %s\nwant\n  %s",
			strings.Join(rendered, " | "), strings.Join(want, " | "))
	}
}

// TestSyncOutboxDriftCoversSharedTables compares the three places the shared
// classification is written -- the list in internal/db/shared_tables.go, the
// table SCOPES.md names as shared, and the triggers a freshly migrated file
// actually carries -- so a migration that adds or re-keys a shared table is
// caught here as a disagreement rather than as a silent divergence.
func TestSyncOutboxDriftCoversSharedTables(t *testing.T) {
	sqlDB := migratedSQLDB(t)
	scoped := scopedSharedTables(t)

	for _, table := range SharedTables {
		if !scoped[table.Name] {
			t.Errorf("%s is in SharedTables but SCOPES.md does not name it shared", table.Name)
		}
	}
	for name := range scoped {
		if !sharedTableNamed(name) {
			t.Errorf("SCOPES.md names %s shared but SharedTables does not list it", name)
		}
	}

	bodies := triggerBodies(t, sqlDB)
	seen := map[string]string{}
	for _, table := range SharedTables {
		for _, op := range outboxOps {
			name := "sync_outbox_" + table.Name + op.Suffix
			seen[name] = table.Name
			assertTriggerOn(t, bodies, name, table, op)
		}
	}
	assertNoUnlistedTriggers(t, bodies, seen)
	assertParentsFirst(t, sqlDB)
}

// scopedSharedTables returns the tables SCOPES.md classifies as shared: every
// row of the shared-history table's first column, which names the table either
// bare or with the mirror prefix.
func scopedSharedTables(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("migrations/SCOPES.md")
	if err != nil {
		t.Fatalf("read SCOPES.md: %v", err)
	}
	_, shared, found := strings.Cut(string(raw), "## Shared history")
	if !found {
		t.Fatal("SCOPES.md has no shared-history section")
	}
	section, _, _ := strings.Cut(shared, "\n## ")

	tables := map[string]bool{}
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cell, _, _ := strings.Cut(strings.TrimPrefix(line, "|"), "|")
		// The mirror tables carry the prefix the section describes them with,
		// and it names the same table the migration created.
		cell = strings.ReplaceAll(strings.TrimSpace(cell), "`", "")
		name := strings.TrimPrefix(cell, "mirror ")
		if name == "" || name == "table" || strings.HasPrefix(name, "-") {
			continue
		}
		tables[name] = true
	}
	if len(tables) == 0 {
		t.Fatal("SCOPES.md's shared-history table names no table")
	}
	return tables
}

// sharedTableNamed reports whether SharedTables lists a table.
func sharedTableNamed(name string) bool {
	for _, table := range SharedTables {
		if table.Name == name {
			return true
		}
	}
	return false
}

// assertTriggerOn checks that one table's trigger for one operation exists and
// keys on the table's current primary-key columns.
func assertTriggerOn(t *testing.T, bodies map[string]string, name string, table SharedTable, op outboxOp) {
	t.Helper()
	body, ok := bodies[name]
	if !ok {
		t.Errorf("%s is missing; the shared table has no %s outbox trigger", name, op.Name)
		return
	}
	tight := strings.ReplaceAll(body, " ", "")
	if !strings.Contains(tight, "AFTER"+strings.ToUpper(op.Name)+"ON"+table.Name) {
		t.Errorf("%s is not an AFTER %s trigger on %s", name, strings.ToUpper(op.Name), table.Name)
	}
	if !strings.Contains(tight, "'"+op.Name+"'") {
		t.Errorf("%s does not record op %q", name, op.Name)
	}

	cols, ok := outboxKeyArgs(body)
	if !ok {
		t.Errorf("%s records no key", name)
		return
	}
	if strings.Join(cols, ",") != strings.Join(table.PrimaryKey, ",") {
		t.Errorf("%s keys on %v, want %v", name, cols, table.PrimaryKey)
	}
}

// assertNoUnlistedTriggers fails on an outbox trigger for a table the list does
// not carry, which is how a de-scoped or dropped table would keep recording.
func assertNoUnlistedTriggers(t *testing.T, bodies map[string]string, seen map[string]string) {
	t.Helper()
	for name := range bodies {
		if !strings.HasPrefix(name, "sync_outbox_") {
			continue
		}
		if _, ok := seen[name]; !ok {
			t.Errorf("%s records into the outbox but no shared table claims it", name)
		}
	}
}

// assertParentsFirst pins the list's order against the foreign keys the schema
// carries: a table that needs a parent must come after it.
func assertParentsFirst(t *testing.T, sqlDB *sql.DB) {
	t.Helper()
	position := map[string]int{}
	for i, table := range SharedTables {
		position[table.Name] = i
	}

	for _, table := range SharedTables {
		for _, parent := range foreignKeyParents(t, sqlDB, table.Name) {
			parentAt, shared := position[parent]
			// A table that references itself, as binding does through the fork
			// it was taken from, constrains no order.
			if !shared || parent == table.Name {
				continue
			}
			if parentAt >= position[table.Name] {
				t.Errorf("%s is at %d but its parent %s is at %d; parents must come first",
					table.Name, position[table.Name], parent, parentAt)
			}
		}
	}
}

// TestSyncOutboxWriteShapes pins every shared-table write shape against a fresh
// migrated file with foreign keys on: each shape records exactly the entries it
// must, keyed on the right columns and attributed to the right installation. A
// child resolves its owner through its parent, and a child removed by a cascade
// resolves to no owner at all, because its parent is already gone.
func TestSyncOutboxWriteShapes(t *testing.T) {
	t.Run("insert records one entry per row", testInsertShape)
	t.Run("update records the row it wrote", testUpdateShape)
	t.Run("upsert records a single insert", testUpsertShape)
	t.Run("delete records the row it removed", testDeleteShape)
	t.Run("cascade delete attributes only the parent", testCascadeShape)
	t.Run("a child resolves its owner through its parent", testInheritedOriginShape)
	t.Run("a local-only write records nothing", testLocalOnlyShape)
}

// migratedWithForeignKeys opens a fresh migrated file and proves the cascade
// assertions run on a connection that enforces them: without the guard, a
// cascade that silently did nothing would pass by recording no children.
func migratedWithForeignKeys(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB := migratedSQLDB(t)
	var on int
	if err := sqlDB.QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if on != 1 {
		t.Fatalf("foreign_keys is %d; the cascade shape needs it on", on)
	}
	return sqlDB
}

// The origin each root is seeded with. They are distinct on purpose: a shared
// value would let a trigger that reads an owner from the wrong table pass, so
// every child a shape below asserts is attributed to the parent that owns it
// and to nothing else.
const (
	repoOrigin          = "instP"
	bindingOrigin       = "instB"
	mastermindOrigin    = "instM"
	bindingRecordOrigin = "instR"
	chainsOrigin        = "instC"
	installationID      = "instI"
)

// seedRoots writes one row into each root table the shapes below descend from,
// so a child has a parent to resolve its owner through, and returns the outbox
// mark that separates the fixture's own entries from the shape's.
func seedRoots(t *testing.T, sqlDB *sql.DB) int {
	t.Helper()
	execAll(t, sqlDB,
		`INSERT INTO repo (id, origin_url, common_dir, first_seen, origin) VALUES ('r1', 'u', '/c', 't', '`+repoOrigin+`')`,
		`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin) VALUES ('b1', 'n', '/x', 'headless', 't', '', '`+bindingOrigin+`')`,
		`INSERT INTO "mastermind" (id, harness_kind, session_id, first_seen, last_seen, origin) VALUES ('m1', 'agy', 's', 't', 't', '`+mastermindOrigin+`')`,
		`INSERT INTO chains (id, name, status, phase, step, plan, plans, plan_paths, builder, created_at, updated_at, origin)
		 VALUES ('c1', 'n', 'open', 'p', 's', 1, 1, '[]', 'b', 't', 't', '`+chainsOrigin+`')`,
	)
	return outboxMark(t, sqlDB)
}

// testInsertShape pins the plain write: one entry, keyed on the primary key,
// attributed to the row's own id. An installation names itself, since its id is
// its installation id.
func testInsertShape(t *testing.T) {
	sqlDB := migratedWithForeignKeys(t)
	mark := seedRoots(t, sqlDB)
	execAll(t, sqlDB,
		`INSERT INTO installation (id, label, first_seen, last_seen) VALUES ('`+installationID+`', 'self', 't', 't')`,
	)
	wantEntries(t, sqlDB, mark, `installation ["`+installationID+`"] insert `+installationID)
}

// testUpdateShape pins that an update reports the row it wrote, not the row it
// replaced: the replaced key is gone once the statement commits.
func testUpdateShape(t *testing.T) {
	sqlDB := migratedWithForeignKeys(t)
	mark := seedRoots(t, sqlDB)
	execAll(t, sqlDB,
		`UPDATE repo SET origin_url = 'u2' WHERE id = 'r1'`,
	)
	wantEntries(t, sqlDB, mark, `repo ["r1"] update `+repoOrigin)
}

// testUpsertShape pins the replace: it is one write of one row, so it records
// the insert alone. A replace that also recorded a delete would export the same
// row twice and make an importer re-apply a delete for a row it still holds.
func testUpsertShape(t *testing.T) {
	sqlDB := migratedWithForeignKeys(t)
	mark := seedRoots(t, sqlDB)
	execAll(t, sqlDB,
		`INSERT OR REPLACE INTO repo (id, origin_url, common_dir, first_seen, origin) VALUES ('r1', 'u2', '/c', 't', '`+repoOrigin+`')`,
	)
	wantEntries(t, sqlDB, mark, `repo ["r1"] insert `+repoOrigin)
}

// testDeleteShape pins that a delete reports the row it removed and keeps its
// owner: the row is gone, so a reader cannot look the origin up afterwards.
func testDeleteShape(t *testing.T) {
	sqlDB := migratedWithForeignKeys(t)
	mark := seedRoots(t, sqlDB)
	execAll(t, sqlDB,
		`INSERT INTO repo (id, origin_url, common_dir, first_seen, origin) VALUES ('r2', 'u2', '/c2', 't', '`+repoOrigin+`')`,
		`DELETE FROM repo WHERE id = 'r2'`,
	)
	wantEntries(t, sqlDB, mark,
		`repo ["r2"] insert `+repoOrigin,
		`repo ["r2"] delete `+repoOrigin,
	)
}

// testCascadeShape pins the shape that has no writer to ask: deleting a parent
// removes its children without a statement per child. The parent's entry names
// its owner and travels, because it cascades on every importer too; each child's
// entry names no owner, because the parent it would read is already gone, and a
// reader skips it.
func testCascadeShape(t *testing.T) {
	sqlDB := migratedWithForeignKeys(t)
	mark := seedRoots(t, sqlDB)
	execAll(t, sqlDB,
		`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json, created_at, updated_at, origin)
		 VALUES ('rec1', 'o', 'n', 'open', 1, '/x', '{}', 't', 't', '`+bindingRecordOrigin+`')`,
		`INSERT INTO binding_event (record_id, seq, ts, round, direction, kind, confirmed, entry_json)
		 VALUES ('rec1', 1, 't', 1, 'out', 'k', 1, '{}')`,
		`INSERT INTO round_file (record_id, name, round, body, bytes, sha256, mtime, sealed_at)
		 VALUES ('rec1', 'f', 1, X'00', 1, 's', 't', 't')`,
	)
	deleting := outboxMark(t, sqlDB)
	execAll(t, sqlDB, `DELETE FROM binding_record WHERE id = 'rec1'`)

	wantEntries(t, sqlDB, deleting,
		`binding_event ["rec1",1] delete NULL`,
		`round_file ["rec1","f"] delete NULL`,
		`binding_record ["rec1"] delete `+bindingRecordOrigin,
	)
	wantEntries(t, sqlDB, mark,
		`binding_record ["rec1"] insert `+bindingRecordOrigin,
		`binding_event ["rec1",1] insert `+bindingRecordOrigin,
		`round_file ["rec1","f"] insert `+bindingRecordOrigin,
		`binding_event ["rec1",1] delete NULL`,
		`round_file ["rec1","f"] delete NULL`,
		`binding_record ["rec1"] delete `+bindingRecordOrigin,
	)
}

// testInheritedOriginShape pins owner resolution for the rows that carry no
// origin column: a child reads its owner through its parent, a grandchild
// through two hops, and a transcript through the table its owner id names.
//
// Every root here names a different installation, so each entry below is
// attributed by the parent that owns it and by nothing else: a trigger that read
// an owner from the wrong table records a value this list does not carry, and
// the shape fails.
func testInheritedOriginShape(t *testing.T) {
	sqlDB := migratedWithForeignKeys(t)
	mark := outboxMark(t, sqlDB)
	execAll(t, sqlDB,
		// The roots, each naming its own installation.
		`INSERT INTO repo (id, origin_url, common_dir, first_seen, origin) VALUES ('r1', 'u', '/c', 't', '`+repoOrigin+`')`,
		`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin) VALUES ('b1', 'n', '/x', 'headless', 't', '', '`+bindingOrigin+`')`,
		`INSERT INTO "mastermind" (id, harness_kind, session_id, first_seen, last_seen, origin) VALUES ('m1', 'agy', 's', 't', 't', '`+mastermindOrigin+`')`,
		`INSERT INTO chains (id, name, status, phase, step, plan, plans, plan_paths, builder, created_at, updated_at, origin)
		 VALUES ('c1', 'n', 'open', 'p', 's', 1, 1, '[]', 'b', 't', 't', '`+chainsOrigin+`')`,
		`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json, created_at, updated_at, origin)
		 VALUES ('rec1', 'o', 'n', 'open', 1, '/x', '{}', 't', 't', '`+bindingRecordOrigin+`')`,
		`INSERT INTO installation (id, label, first_seen, last_seen) VALUES ('`+installationID+`', 'self', 't', 't')`,
		// The binding's own children, and a grandchild of it.
		`INSERT INTO round (id, binding_id, number, started_at, outcome, switches) VALUES ('rd1', 'b1', 1, 't', 'done', 0)`,
		`INSERT INTO event (id, binding_id, seq, ts, kind, direction, confirmed, late, entry_json)
		 VALUES ('e1', 'b1', 1, 't', 'k', 'out', 1, 0, '{}')`,
		`INSERT INTO artifact (id, round_id, kind, text, bytes, sha256, captured_at) VALUES ('a1', 'rd1', 'log', 'x', 1, 's', 't')`,
		// A transcript names the table its owner id is an id in, so the two
		// below resolve through different roots and must differ.
		`INSERT INTO transcript (id, owner_kind, owner_id, seq, record_json, rendered) VALUES ('tr1', 'round', 'rd1', 1, '{}', 'x')`,
		`INSERT INTO transcript (id, owner_kind, owner_id, seq, record_json, rendered) VALUES ('tr2', 'mastermind', 'm1', 1, '{}', 'x')`,
		// The record's children.
		`INSERT INTO binding_event (record_id, seq, ts, round, direction, kind, confirmed, entry_json)
		 VALUES ('rec1', 1, 't', 1, 'out', 'k', 1, '{}')`,
		`INSERT INTO round_file (record_id, name, round, body, bytes, sha256, mtime, sealed_at)
		 VALUES ('rec1', 'f', 1, X'00', 1, 's', 't', 't')`,
		// The chain's children.
		`INSERT INTO chain_event (chain_id, seq, ts, phase, step, member, round, event, action)
		 VALUES ('c1', 1, 't', 'p', 's', 'm', 1, '{}', '{}')`,
		`INSERT INTO chain_member (chain_id, binding, actor, seq) VALUES ('c1', 'b1', 'builder', 1)`,
		`INSERT INTO chain_check (chain_id, run, step, visit, command, created_at)
		 VALUES ('c1', 1, 's', 1, 'echo', 0)`,
		// A delete of a live child, which still resolves through its parent.
		`DELETE FROM round_file WHERE record_id = 'rec1' AND name = 'f'`,
	)
	wantEntries(t, sqlDB, mark,
		`repo ["r1"] insert `+repoOrigin,
		`binding ["b1"] insert `+bindingOrigin,
		`mastermind ["m1"] insert `+mastermindOrigin,
		`chains ["c1"] insert `+chainsOrigin,
		`binding_record ["rec1"] insert `+bindingRecordOrigin,
		`installation ["`+installationID+`"] insert `+installationID,
		`round ["rd1"] insert `+bindingOrigin,
		`event ["e1"] insert `+bindingOrigin,
		`artifact ["a1"] insert `+bindingOrigin,
		`transcript ["tr1"] insert `+bindingOrigin,
		`transcript ["tr2"] insert `+mastermindOrigin,
		`binding_event ["rec1",1] insert `+bindingRecordOrigin,
		`round_file ["rec1","f"] insert `+bindingRecordOrigin,
		`chain_event ["c1",1] insert `+chainsOrigin,
		`chain_member ["c1","b1"] insert `+chainsOrigin,
		`chain_check ["c1",1] insert `+chainsOrigin,
		`round_file ["rec1","f"] delete `+bindingRecordOrigin,
	)
}

// testLocalOnlyShape pins the negative half of the classification: a table
// another installation may never read records nothing, so its values cannot
// reach the log by way of a trigger.
func testLocalOnlyShape(t *testing.T) {
	sqlDB := migratedWithForeignKeys(t)
	mark := seedRoots(t, sqlDB)

	for _, tbl := range localOnlyWrites {
		execAll(t, sqlDB, tbl.stmt)
	}
	wantEntries(t, sqlDB, mark)
}
