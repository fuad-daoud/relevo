package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// migratedSQLDB opens a driver connection to path and applies the whole
// embedded migration series, so a test reads the schema a real open leaves.
func migratedSQLDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB := rawSQLDB(t, filepath.Join(t.TempDir(), "relevo.db"))
	if err := applyMigrations(sqlDB, migrationFiles); err != nil {
		t.Fatalf("applyMigrations: %v", err)
	}
	return sqlDB
}

// triggerBodies returns every trigger in the database keyed by name.
func triggerBodies(t *testing.T, sqlDB *sql.DB) map[string]string {
	t.Helper()
	rows, err := sqlDB.Query(`SELECT name, sql FROM sqlite_schema WHERE type = 'trigger'`)
	if err != nil {
		t.Fatalf("read sqlite_schema: %v", err)
	}
	defer func() { _ = rows.Close() }()

	bodies := map[string]string{}
	for rows.Next() {
		var name string
		var body sql.NullString
		if err := rows.Scan(&name, &body); err != nil {
			t.Fatalf("scan trigger: %v", err)
		}
		bodies[name] = body.String
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate triggers: %v", err)
	}
	return bodies
}

// outboxKeyArgs returns the columns a trigger's json_array names, in order,
// with the NEW. or OLD. row alias stripped. Spaces are dropped first because
// the engine rewrites a call's open paren with one before it.
func outboxKeyArgs(body string) ([]string, bool) {
	tight := strings.ReplaceAll(body, " ", "")
	open := strings.Index(tight, "json_array(")
	if open < 0 {
		return nil, false
	}
	close := strings.Index(tight[open:], ")")
	if close < 0 {
		return nil, false
	}
	raw := tight[open+len("json_array(") : open+close]

	var cols []string
	for _, arg := range strings.Split(raw, ",") {
		arg = strings.TrimPrefix(arg, "NEW.")
		arg = strings.TrimPrefix(arg, "OLD.")
		cols = append(cols, arg)
	}
	return cols, true
}

// TestOutboxTriggersCoverEverySharedTable pins the trigger set: every shared
// table carries all three operations, each keyed on that table's primary-key
// columns and on the row the operation is about, and the table the triggers
// write into exists.
func TestOutboxTriggersCoverEverySharedTable(t *testing.T) {
	sqlDB := migratedSQLDB(t)

	if _, err := sqlDB.Exec(`INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('probe', '[]', 'insert', NULL)`); err != nil {
		t.Fatalf("sync_outbox is missing or unusable: %v", err)
	}

	bodies := triggerBodies(t, sqlDB)
	for _, table := range SharedTables {
		for _, op := range outboxOps {
			name := "sync_outbox_" + table.Name + op.Suffix
			body, ok := bodies[name]
			if !ok {
				t.Errorf("%s is missing", name)
				continue
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
				continue
			}
			if strings.Join(cols, ",") != strings.Join(table.PrimaryKey, ",") {
				t.Errorf("%s keys on %v, want %v", name, cols, table.PrimaryKey)
			}
			// A delete reports the row it removed; an insert or an update
			// reports the row it wrote. Keying an update on the row it replaced
			// would name a key that no longer exists.
			alias := "NEW."
			if op.Name == "delete" {
				alias = "OLD."
			}
			key := tight[strings.Index(tight, "json_array("):]
			key = key[:strings.Index(key, ")")]
			if !strings.HasPrefix(key, "json_array("+alias) {
				t.Errorf("%s keys on %s, want %s for a %s", name, key, alias, op.Name)
			}
		}
	}
}

// localOnlyWrites writes one row into each table no other installation may
// read, so a test can prove the classification reaches past the triggers.
var localOnlyWrites = []struct {
	name string
	stmt string
}{
	{"kv", `INSERT INTO kv (key, value_json, updated_at) VALUES ('probe', '"v"', '2026-10-08T00:00:00.000Z')`},
	{"secret", `INSERT INTO secret (name, value, updated_at) VALUES ('probe', 'v', '2026-10-08T00:00:00.000Z')`},
	{"config_doc", `INSERT INTO config_doc (name, body, updated_at) VALUES ('probe', '{}', '2026-10-08T00:00:00.000Z')`},
	{"config_revision", `INSERT INTO config_revision (rev, at, source, message, version, changes, snapshot) VALUES (1, '2026-10-08T00:00:00.000Z', 'probe', 'm', 1, '{}', '{}')`},
	{"config_meta", `UPDATE config_meta SET version = version + 1 WHERE id = 1`},
	{"config_import", `INSERT INTO config_import (name, source_path, body, imported_at) VALUES ('probe', '/probe', X'00', '2026-10-08T00:00:00.000Z')`},
	{"session_consent", `INSERT INTO session_consent (harness_kind, session_id) VALUES ('agy', 's')`},
	{"ingest_cursor", `INSERT INTO ingest_cursor (source, byte_offset, head_sha, updated_at) VALUES ('/probe', 0, '', '2026-10-08T00:00:00.000Z')`},
}

// TestOutboxLeavesLocalOnlyTablesAlone pins the other half of the
// classification: a table no other installation may read gets no trigger, so a
// write to it records nothing.
func TestOutboxLeavesLocalOnlyTablesAlone(t *testing.T) {
	sqlDB := migratedSQLDB(t)

	bodies := triggerBodies(t, sqlDB)
	for _, tbl := range localOnlyWrites {
		for _, op := range outboxOps {
			if name := "sync_outbox_" + tbl.name + op.Suffix; bodies[name] != "" {
				t.Errorf("%s exists; %s must never travel", name, tbl.name)
			}
		}
		if _, err := sqlDB.Exec(tbl.stmt); err != nil {
			t.Fatalf("write %s: %v", tbl.name, err)
		}
	}

	var recorded int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sync_outbox`).Scan(&recorded); err != nil {
		t.Fatalf("count sync_outbox: %v", err)
	}
	if recorded != 0 {
		t.Errorf("local-only writes recorded %d outbox rows, want 0", recorded)
	}
}

// TestSharedTablesMatchTheScopesClassification pins the list the triggers are
// built from against the document that classifies them, so a table added to one
// and not the other is caught here rather than by a reader on another machine.
func TestSharedTablesMatchTheScopesClassification(t *testing.T) {
	raw, err := os.ReadFile("migrations/SCOPES.md")
	if err != nil {
		t.Fatalf("read SCOPES.md: %v", err)
	}
	shared, _, _ := strings.Cut(string(raw), "## Machine-local, never synced")

	for _, table := range SharedTables {
		if !strings.Contains(shared, "`"+table.Name+"`") {
			t.Errorf("%s is in SharedTables but not in SCOPES.md's shared table list", table.Name)
		}
	}
}

// TestSharedTablesAreParentsBeforeChildren pins the list's order against the
// foreign keys the schema actually carries: a table that needs a parent must
// come after it, or a consumer walking the list meets a row before the parent
// it belongs to.
func TestSharedTablesAreParentsBeforeChildren(t *testing.T) {
	sqlDB := migratedSQLDB(t)

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

// foreignKeyParents returns the distinct tables that tbl references.
func foreignKeyParents(t *testing.T, sqlDB *sql.DB, tbl string) []string {
	t.Helper()
	rows, err := sqlDB.Query(`SELECT "table" FROM pragma_foreign_key_list(?)`, tbl)
	if err != nil {
		t.Fatalf("foreign_key_list %s: %v", tbl, err)
	}
	defer func() { _ = rows.Close() }()

	var parents []string
	seen := map[string]bool{}
	for rows.Next() {
		var parent string
		if err := rows.Scan(&parent); err != nil {
			t.Fatalf("scan foreign key of %s: %v", tbl, err)
		}
		if !seen[parent] {
			seen[parent] = true
			parents = append(parents, parent)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign keys of %s: %v", tbl, err)
	}
	return parents
}
