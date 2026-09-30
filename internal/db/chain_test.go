package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// testChain is a valid chain row: two plans, the first in progress, and one
// distinct member binding per part.
func testChain(name string) ChainRow {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return ChainRow{
		ID:              NewID(),
		Name:            name,
		Status:          "running",
		Phase:           "build",
		Step:            "building",
		Plan:            1,
		Plans:           2,
		PlanPathsJSON:   []byte(`["/plans/001.md","/plans/002.md"]`),
		SettingsJSON:    []byte(`{"max_corrections":1}`),
		Builder:         name,
		Reviewer:        name + "-rev",
		Planner:         name + "-plan",
		Security:        name + "-sec",
		Base:            "main",
		Branch:          "relevo/" + name,
		Repo:            "https://example.test/r.git",
		Worktree:        "/work/" + name,
		Feature:         "f",
		Ticket:          "#1",
		MasterMindID:    "mm1",
		PlanStartCommit: "commit-plan-start",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func putChain(t *testing.T, d *DB, c ChainRow) {
	t.Helper()
	if err := d.Tx(func(tx *Tx) error { return tx.ChainPut(c) }); err != nil {
		t.Fatalf("ChainPut(%q): %v", c.Name, err)
	}
}

func appendChainEvent(t *testing.T, d *DB, id string, e ChainEventRow) {
	t.Helper()
	if err := d.Tx(func(tx *Tx) error { return tx.ChainEventAppend(id, e) }); err != nil {
		t.Fatalf("ChainEventAppend(%s): %v", id, err)
	}
}

// tableColumns returns the column names PRAGMA table_info reports for table.
func tableColumns(t *testing.T, sqlDB *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := sqlDB.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.Null[string]
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	return out
}

func assertColumns(t *testing.T, sqlDB *sql.DB, table string, want []string) {
	t.Helper()
	have := tableColumns(t, sqlDB, table)
	for _, col := range want {
		if !have[col] {
			t.Errorf("%s is missing column %s", table, col)
		}
	}
}

// TestMigration016AddsChainsAndChainEvent pins the two tables, their columns
// and indexes, that a second apply is a no-op, and that each version is
// recorded exactly once.
func TestMigration016AddsChainsAndChainEvent(t *testing.T) {
	sqlDB := rawSQLDB(t, filepath.Join(t.TempDir(), "relevo.db"))
	sixteen := migrationFilesUpTo(t, 16)
	if err := applyMigrations(sqlDB, sixteen); err != nil {
		t.Fatalf("applyMigrations through 016: %v", err)
	}

	assertColumns(t, sqlDB, "chains", []string{
		"id", "origin", "owner", "name", "status", "reason", "phase", "step",
		"plan", "plans", "plan_paths", "corrections", "awaiting_member", "awaiting_round",
		"settings", "builder", "reviewer", "planner", "security", "base", "branch",
		"repo", "worktree", "feature", "ticket", "server", "mastermind_id",
		"created_at", "updated_at",
	})
	assertColumns(t, sqlDB, "chain_event", []string{
		"chain_id", "seq", "ts", "phase", "step", "member", "round", "event", "action", "reason",
	})

	for _, idx := range []string{
		"chains_origin_owner_name_uidx",
		"chains_owner_builder_idx",
		"chains_owner_reviewer_idx",
		"chains_owner_planner_idx",
		"chains_owner_security_idx",
	} {
		var name string
		if err := sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&name); err != nil {
			t.Errorf("index %s is missing: %v", idx, err)
		}
	}

	if err := applyMigrations(sqlDB, sixteen); err != nil {
		t.Fatalf("second applyMigrations 016: %v", err)
	}
	var rows, versions int
	if err := sqlDB.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT version) FROM schema_version`).Scan(&rows, &versions); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if rows != 16 || versions != 16 {
		t.Errorf("schema_version has %d rows and %d versions, want 16 and 16", rows, versions)
	}
}

// TestMigration016KeepsExistingRows pins that adding the chain tables leaves
// an existing binding record alone.
func TestMigration016KeepsExistingRows(t *testing.T) {
	sqlDB := rawSQLDB(t, filepath.Join(t.TempDir(), "relevo.db"))
	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 15)); err != nil {
		t.Fatalf("applyMigrations through 015: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO binding_record
			(id, owner, name, state, round, cwd, record_json, created_at, updated_at)
		VALUES ('r1', '', 'api', 'active', 1, '/repo', '{}', '2026-09-01T10:00:00.000Z', '2026-09-01T10:00:00.000Z')`); err != nil {
		t.Fatalf("insert binding_record: %v", err)
	}

	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 16)); err != nil {
		t.Fatalf("applyMigrations 016: %v", err)
	}
	var name string
	if err := sqlDB.QueryRow(`SELECT name FROM binding_record WHERE id = 'r1'`).Scan(&name); err != nil {
		t.Fatalf("select binding_record after 016: %v", err)
	}
	if name != "api" {
		t.Errorf("binding_record name = %q, want api", name)
	}
}

// TestMigration017AddsPlanStartCommit pins the plan-start column: applying 017
// adds it with an empty default, leaves a row written before it reading ”, and
// a second apply records nothing new.
func TestMigration017AddsPlanStartCommit(t *testing.T) {
	sqlDB := rawSQLDB(t, filepath.Join(t.TempDir(), "relevo.db"))
	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 16)); err != nil {
		t.Fatalf("applyMigrations through 016: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO chains
			(id, name, status, phase, step, plan, plans, plan_paths, builder, created_at, updated_at)
		VALUES ('c1', 'x', 'running', 'build', 'building', 1, 1, '["/p/1.md"]', 'x',
			'2026-09-01T10:00:00.000Z', '2026-09-01T10:00:00.000Z')`); err != nil {
		t.Fatalf("insert chains row: %v", err)
	}

	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 17)); err != nil {
		t.Fatalf("applyMigrations 017: %v", err)
	}
	assertColumns(t, sqlDB, "chains", []string{"plan_start_commit"})
	var start string
	if err := sqlDB.QueryRow(`SELECT plan_start_commit FROM chains WHERE id = 'c1'`).Scan(&start); err != nil {
		t.Fatalf("select plan_start_commit: %v", err)
	}
	if start != "" {
		t.Errorf("old row plan_start_commit = %q, want the empty default", start)
	}

	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 17)); err != nil {
		t.Fatalf("second applyMigrations 017: %v", err)
	}
	var rows, versions int
	if err := sqlDB.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT version) FROM schema_version`).Scan(&rows, &versions); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if rows != 17 || versions != 17 {
		t.Errorf("schema_version has %d rows and %d versions, want 17 and 17", rows, versions)
	}
}

func TestChainPutGetRoundTrip(t *testing.T) {
	d := openTestDB(t)
	c := testChain("x")
	putChain(t, d, c)

	got, ok, err := d.ChainGet("", "x")
	if err != nil || !ok {
		t.Fatalf("ChainGet(x) = (found %v, err %v), want (true, nil)", ok, err)
	}
	assertChainMatches(t, got, c)

	// A second put updates in place and keeps the row's id and created_at.
	next := got
	next.Step = "reviewing"
	next.Reason = "waiting on a verdict"
	putChain(t, d, next)

	again, ok, err := d.ChainGet("", "x")
	if err != nil || !ok {
		t.Fatalf("ChainGet(x) after the second put = (found %v, err %v)", ok, err)
	}
	if again.ID != c.ID {
		t.Errorf("second put id = %s, want the existing %s", again.ID, c.ID)
	}
	if again.Step != "reviewing" || again.Reason != "waiting on a verdict" {
		t.Errorf("after put: step %q reason %q, want reviewing and the new reason", again.Step, again.Reason)
	}
	if !again.CreatedAt.Equal(c.CreatedAt) {
		t.Errorf("created_at = %v, want the original %v (a put keeps it)", again.CreatedAt, c.CreatedAt)
	}

	if _, ok, err := d.ChainGet("", "missing"); err != nil || ok {
		t.Errorf("ChainGet(missing) = (found %v, err %v), want (false, nil)", ok, err)
	}

	if err := d.Tx(func(tx *Tx) error { return tx.ChainDelete("", "x") }); err != nil {
		t.Fatalf("ChainDelete: %v", err)
	}
	if _, ok, err := d.ChainGet("", "x"); err != nil || ok {
		t.Errorf("ChainGet after delete = (found %v, err %v), want (false, nil)", ok, err)
	}
}

// assertChainMatches compares the stored columns a caller can set, leaving the
// origin to the handle that wrote the row.
func assertChainMatches(t *testing.T, got, want ChainRow) {
	t.Helper()
	if got.ID != want.ID || got.Name != want.Name {
		t.Errorf("id/name = %s/%q, want %s/%q", got.ID, got.Name, want.ID, want.Name)
	}
	if got.Status != want.Status || got.Phase != want.Phase || got.Step != want.Step || got.Reason != want.Reason {
		t.Errorf("status/phase/step/reason = %q/%q/%q/%q, want %q/%q/%q/%q",
			got.Status, got.Phase, got.Step, got.Reason, want.Status, want.Phase, want.Step, want.Reason)
	}
	if got.Plan != want.Plan || got.Plans != want.Plans || got.Corrections != want.Corrections {
		t.Errorf("plan/plans/corrections = %d/%d/%d, want %d/%d/%d",
			got.Plan, got.Plans, got.Corrections, want.Plan, want.Plans, want.Corrections)
	}
	if string(got.PlanPathsJSON) != string(want.PlanPathsJSON) {
		t.Errorf("plan_paths = %s, want %s", got.PlanPathsJSON, want.PlanPathsJSON)
	}
	if string(got.SettingsJSON) != string(want.SettingsJSON) {
		t.Errorf("settings = %s, want %s", got.SettingsJSON, want.SettingsJSON)
	}
	if got.Builder != want.Builder || got.Reviewer != want.Reviewer || got.Planner != want.Planner || got.Security != want.Security {
		t.Errorf("members = %q/%q/%q/%q, want %q/%q/%q/%q",
			got.Builder, got.Reviewer, got.Planner, got.Security, want.Builder, want.Reviewer, want.Planner, want.Security)
	}
	if got.Base != want.Base || got.Branch != want.Branch || got.Repo != want.Repo ||
		got.Worktree != want.Worktree || got.Feature != want.Feature || got.Ticket != want.Ticket ||
		got.Server != want.Server || got.MasterMindID != want.MasterMindID ||
		got.PlanStartCommit != want.PlanStartCommit {
		t.Errorf("placement/facts = %+v, want %+v", got, want)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("timestamps = %v/%v, want %v/%v", got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
	}
}

// TestChainGetByMemberFindsEachPart pins that any of the four member columns
// resolves the chain.
func TestChainGetByMemberFindsEachPart(t *testing.T) {
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

func TestChainListIsOwnerScoped(t *testing.T) {
	d := openTestDB(t)
	first := testChain("a")
	putChain(t, d, first)
	second := testChain("b")
	second.Owner = "owner2"
	putChain(t, d, second)

	list, err := d.ChainList("")
	if err != nil {
		t.Fatalf("ChainList(local): %v", err)
	}
	if len(list) != 1 || list[0].Name != "a" {
		t.Fatalf("ChainList(local) = %+v, want only a", list)
	}

	other, err := d.ChainList("owner2")
	if err != nil {
		t.Fatalf("ChainList(owner2): %v", err)
	}
	if len(other) != 1 || other[0].Name != "b" {
		t.Fatalf("ChainList(owner2) = %+v, want only b", other)
	}

	if _, ok, err := d.ChainGet("", "b"); err != nil || ok {
		t.Errorf("another owner's chain is visible: (found %v, err %v)", ok, err)
	}
}

func TestChainEventsAppendInSeqOrder(t *testing.T) {
	d := openTestDB(t)
	c := testChain("x")
	putChain(t, d, c)

	now := time.Now().UTC().Truncate(time.Millisecond)
	kinds := []string{"builder_closed", "reviewer_closed", "planner_closed"}
	for i, kind := range kinds {
		appendChainEvent(t, d, c.ID, ChainEventRow{
			TS:     now.Add(time.Duration(i) * time.Millisecond),
			Phase:  "build",
			Step:   "building",
			Member: c.Builder,
			Round:  i + 1,
			Event:  `{"kind":"` + kind + `"}`,
			Action: `{"kind":"send","member":"` + c.Reviewer + `","seed":"reviewer"}`,
		})
	}

	got, err := d.ChainEvents(c.ID)
	if err != nil {
		t.Fatalf("ChainEvents: %v", err)
	}
	if len(got) != len(kinds) {
		t.Fatalf("ChainEvents returned %d rows, want %d", len(got), len(kinds))
	}
	for i, e := range got {
		if e.Seq != i+1 {
			t.Errorf("row %d seq = %d, want %d", i, e.Seq, i+1)
		}
		if e.ChainID != c.ID || e.Member != c.Builder || e.Round != i+1 {
			t.Errorf("row %d = %+v, want chain %s member %q round %d", i, e, c.ID, c.Builder, i+1)
		}
		if !e.TS.Equal(now.Add(time.Duration(i) * time.Millisecond)) {
			t.Errorf("row %d ts = %v, want %v", i, e.TS, now.Add(time.Duration(i)*time.Millisecond))
		}
	}
	if got[0].Event != `{"kind":"builder_closed"}` || got[0].Action == "" {
		t.Errorf("row 0 event/action = %q/%q, want the stored JSON", got[0].Event, got[0].Action)
	}
}

func TestChainPutRejectsAnInvalidRow(t *testing.T) {
	d := openTestDB(t)
	cases := []struct {
		name   string
		mutate func(*ChainRow)
	}{
		{"no plans", func(c *ChainRow) { c.Plans = 0 }},
		{"plan past the last", func(c *ChainRow) { c.Plan = 2; c.Plans = 1 }},
		{"empty status", func(c *ChainRow) { c.Status = "" }},
		{"empty id", func(c *ChainRow) { c.ID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := testChain("x")
			tc.mutate(&c)
			err := d.Tx(func(tx *Tx) error { return tx.ChainPut(c) })
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("ChainPut = %v, want ErrInvalid", err)
			}
		})
	}
}

// TestChainEventAppendAllocatesSeq pins that the caller does not name a seq:
// the stored value is the next after the chain's current maximum.
func TestChainEventAppendAllocatesSeq(t *testing.T) {
	d := openTestDB(t)
	c := testChain("x")
	putChain(t, d, c)

	for i := 0; i < 2; i++ {
		appendChainEvent(t, d, c.ID, ChainEventRow{
			Seq: 99, Phase: "build", Step: "building", Member: c.Builder, Round: 1,
			Event: "{}", Action: "{}",
		})
	}

	got, err := d.ChainEvents(c.ID)
	if err != nil {
		t.Fatalf("ChainEvents: %v", err)
	}
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("allocated seqs = %+v, want 1 then 2", got)
	}
}
