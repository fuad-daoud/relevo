package db

import (
	"path/filepath"
	"testing"
	"time"
)

// putChainMember writes one member row in its own transaction.
func putChainMember(t *testing.T, d *DB, m ChainMemberRow) {
	t.Helper()
	if err := d.Tx(func(tx *Tx) error { return tx.ChainMembersPut(m.ChainID, []ChainMemberRow{m}) }); err != nil {
		t.Fatalf("ChainMembersPut(%+v): %v", m, err)
	}
}

// putChainCheck writes one check row in its own transaction.
func putChainCheck(t *testing.T, d *DB, c ChainCheckRow) {
	t.Helper()
	if err := d.Tx(func(tx *Tx) error { return tx.ChainCheckPut(c) }); err != nil {
		t.Fatalf("ChainCheckPut(%+v): %v", c, err)
	}
}

// TestMigration021AddsChainColumns pins the additive migration: applying 021
// adds the workflow, state and parent columns and the member and check tables,
// leaves a row written before it reading the empty defaults, and a second apply
// records nothing new.
func TestMigration021AddsChainColumns(t *testing.T) {
	sqlDB := rawSQLDB(t, filepath.Join(t.TempDir(), "relevo.db"))
	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 20)); err != nil {
		t.Fatalf("applyMigrations through 020: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO chains
			(id, name, status, phase, step, plan, plans, plan_paths, builder, created_at, updated_at)
		VALUES ('c1', 'x', 'running', 'build', 'building', 1, 1, '["/p/1.md"]', 'x',
			'2026-09-01T10:00:00.000Z', '2026-09-01T10:00:00.000Z')`); err != nil {
		t.Fatalf("insert chains row: %v", err)
	}

	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 21)); err != nil {
		t.Fatalf("applyMigrations 021: %v", err)
	}
	assertColumns(t, sqlDB, "chains", []string{"workflow", "state", "parent"})
	assertColumns(t, sqlDB, "chain_member", []string{"chain_id", "binding", "actor", "seq"})
	assertColumns(t, sqlDB, "chain_check", []string{
		"chain_id", "run", "step", "visit", "command", "pid", "started_at", "attempt",
		"result", "exit_code", "duration_ms", "log", "note", "created_at",
	})
	var name string
	if err := sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'chain_member_binding_idx'`).Scan(&name); err != nil {
		t.Errorf("index chain_member_binding_idx is missing: %v", err)
	}

	var workflow, state, parent string
	if err := sqlDB.QueryRow(`SELECT workflow, state, parent FROM chains WHERE id = 'c1'`).
		Scan(&workflow, &state, &parent); err != nil {
		t.Fatalf("select the new columns: %v", err)
	}
	if workflow != "" || state != "" || parent != "" {
		t.Errorf("old row custom columns = %q/%q/%q, want the empty defaults", workflow, state, parent)
	}

	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 21)); err != nil {
		t.Fatalf("second applyMigrations 021: %v", err)
	}
	var rows, versions int
	if err := sqlDB.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT version) FROM schema_version`).Scan(&rows, &versions); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if rows != 21 || versions != 21 {
		t.Errorf("schema_version has %d rows and %d versions, want 21 and 21", rows, versions)
	}
}

// TestChainMemberLookupPrefersTable pins the lookup order: a binding the member
// table names resolves to that chain even when another chain's legacy columns
// also name it.
func TestChainMemberLookupPrefersTable(t *testing.T) {
	d := openTestDB(t)
	first := testChain("one")
	first.Reviewer = "shared"
	putChain(t, d, first)
	second := testChain("two")
	putChain(t, d, second)

	putChainMember(t, d, ChainMemberRow{ChainID: second.ID, Binding: "shared", Actor: "reviewer", Seq: 1})

	got, ok, err := d.ChainGetByMember("", "shared")
	if err != nil || !ok {
		t.Fatalf("ChainGetByMember(shared) = (found %v, err %v), want (true, nil)", ok, err)
	}
	if got.ID != second.ID {
		t.Errorf("ChainGetByMember(shared) id = %s, want the table's chain %s", got.ID, second.ID)
	}

	// A binding only the table names is found too.
	putChainMember(t, d, ChainMemberRow{ChainID: second.ID, Binding: "table-only", Actor: "builder", Seq: 0})
	if _, ok, err := d.ChainGetByMember("", "table-only"); err != nil || !ok {
		t.Errorf("ChainGetByMember(table-only) = (found %v, err %v), want (true, nil)", ok, err)
	}
}

// TestChainGetByMemberFallsBackToLegacyColumns pins that a chain written
// without member rows still resolves through any of its four legacy columns.
func TestChainGetByMemberFallsBackToLegacyColumns(t *testing.T) {
	d := openTestDB(t)
	c := testChain("x")
	putChain(t, d, c)

	for _, member := range []string{c.Builder, c.Reviewer, c.Planner, c.Security} {
		got, ok, err := d.ChainGetByMember("", member)
		if err != nil || !ok {
			t.Fatalf("ChainGetByMember(%q) = (found %v, err %v), want (true, nil)", member, ok, err)
		}
		if got.ID != c.ID {
			t.Errorf("ChainGetByMember(%q) id = %s, want %s", member, got.ID, c.ID)
		}
	}

	if _, ok, err := d.ChainGetByMember("", "nobody"); err != nil || ok {
		t.Errorf("ChainGetByMember(nobody) = (found %v, err %v), want (false, nil)", ok, err)
	}
}

// TestChainDeleteRemovesMembersAndChecks pins that ChainDelete removes the
// chain's member and check rows, which carry no foreign key of their own.
func TestChainDeleteRemovesMembersAndChecks(t *testing.T) {
	d := openTestDB(t)
	c := testChain("x")
	putChain(t, d, c)
	putChainMember(t, d, ChainMemberRow{ChainID: c.ID, Binding: "x-rev", Actor: "reviewer", Seq: 1})
	putChainCheck(t, d, ChainCheckRow{ChainID: c.ID, Run: 1, Step: "check", Command: "make check",
		Result: "green", CreatedAt: time.Now().UTC().Truncate(time.Millisecond)})

	if err := d.Tx(func(tx *Tx) error { return tx.ChainDelete("", "x") }); err != nil {
		t.Fatalf("ChainDelete: %v", err)
	}

	if _, ok, err := d.ChainGet("", "x"); err != nil || ok {
		t.Errorf("ChainGet after delete = (found %v, err %v), want (false, nil)", ok, err)
	}
	members, err := d.ChainMembers(c.ID)
	if err != nil || len(members) != 0 {
		t.Errorf("ChainMembers after delete = (%+v, %v), want none", members, err)
	}
	if _, ok, err := d.ChainCheckGet(c.ID, 1); err != nil || ok {
		t.Errorf("ChainCheckGet after delete = (found %v, err %v), want (false, nil)", ok, err)
	}
}

// TestChainCheckNextRunIsMonotonic pins the run allocator: it is one past the
// highest recorded run, so a re-run never reuses a number.
func TestChainCheckNextRunIsMonotonic(t *testing.T) {
	d := openTestDB(t)
	c := testChain("x")
	putChain(t, d, c)

	next := func() int {
		t.Helper()
		var run int
		if err := d.Tx(func(tx *Tx) error {
			var err error
			run, err = tx.ChainCheckNextRun(c.ID)
			return err
		}); err != nil {
			t.Fatalf("ChainCheckNextRun: %v", err)
		}
		return run
	}

	if got := next(); got != 1 {
		t.Fatalf("first next run = %d, want 1", got)
	}
	for _, run := range []int{1, 2, 5} {
		putChainCheck(t, d, ChainCheckRow{ChainID: c.ID, Run: run, Step: "check", Command: "make check",
			CreatedAt: time.Now().UTC().Truncate(time.Millisecond)})
		if got, want := next(), run+1; got != want {
			t.Fatalf("next run after %d = %d, want %d", run, got, want)
		}
	}

	got, ok, err := d.ChainCheckGet(c.ID, 5)
	if err != nil || !ok {
		t.Fatalf("ChainCheckGet(5) = (found %v, err %v), want (true, nil)", ok, err)
	}
	if got.Command != "make check" || got.Step != "check" {
		t.Errorf("ChainCheckGet(5) = %+v, want the stored check", got)
	}
}

// TestChainCustomColumnsRoundTrip pins that the custom-workflow columns survive
// a put and a get on a database that carries them.
func TestChainCustomColumnsRoundTrip(t *testing.T) {
	d := openTestDB(t)
	c := testChain("x")
	c.WorkflowJSON = []byte(`{"name":"default"}`)
	c.StateJSON = []byte(`{"status":"running"}`)
	c.Parent = "super"
	putChain(t, d, c)

	got, ok, err := d.ChainGet("", "x")
	if err != nil || !ok {
		t.Fatalf("ChainGet(x) = (found %v, err %v), want (true, nil)", ok, err)
	}
	if string(got.WorkflowJSON) != `{"name":"default"}` || string(got.StateJSON) != `{"status":"running"}` || got.Parent != "super" {
		t.Errorf("custom columns = %s/%s/%q, want the stored values", got.WorkflowJSON, got.StateJSON, got.Parent)
	}
}
