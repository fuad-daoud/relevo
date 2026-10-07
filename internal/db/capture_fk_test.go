//go:build !modernc

package db

// The groups a rewrite is taken over: the file's own tables, closed over the
// references between them, each ordered parents first.

import (
	"context"
	"path/filepath"
	"testing"
)

// groupNamed is the group the named tables are in.
func groupNamed(groups []CaptureGroup, table string) (CaptureGroup, bool) {
	for _, group := range groups {
		for _, name := range group.Tables {
			if name == table {
				return group, true
			}
		}
	}
	return CaptureGroup{}, false
}

// TestTheGroupsAreClosedOverTheReferences pins the closure: two tables that
// reference each other are one group, because a delete of either is refused
// while the other exists on the remote, so neither can be rewritten alone.
func TestTheGroupsAreClosedOverTheReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "groups.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = pool.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE repo (id TEXT PRIMARY KEY)`,
		`CREATE TABLE binding (id TEXT PRIMARY KEY, repo_id TEXT REFERENCES repo(id))`,
		`CREATE TABLE round (id TEXT PRIMARY KEY, binding_id TEXT REFERENCES binding(id))`,
		// A table nothing references: its own group, whatever its name.
		`CREATE TABLE kv (key TEXT PRIMARY KEY, value TEXT)`,
	} {
		if _, err := pool.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	capture, err := openCaptureConnection(context.Background(), path, 5000)
	if err != nil {
		t.Fatalf("open capture: %v", err)
	}
	defer func() { _ = capture.Close() }()
	groups, err := CaptureGroups(context.Background(), capture.Conn())
	if err != nil {
		t.Fatalf("CaptureGroups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("the file's four tables are in %d groups (%v), want two: the referenced chain and the table nothing references",
			len(groups), groups)
	}
	chain, ok := groupNamed(groups, "binding")
	if !ok {
		t.Fatalf("no group names binding: %v", groups)
	}
	if len(chain.Tables) != 3 {
		t.Errorf("the group holding binding holds %v, want repo, binding and round together", chain.Tables)
	}
	if _, ok := groupNamed(groups, "kv"); !ok {
		t.Errorf("no group names kv: %v", groups)
	}
}

// TestAGroupIsOrderedParentsFirst pins the order the rewrite's insert half takes:
// no table before a table it references.
func TestAGroupIsOrderedParentsFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "order.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = pool.Close() }()
	// Named so that alphabetical order is the wrong one throughout.
	for _, stmt := range []string{
		`CREATE TABLE zz_round (id TEXT PRIMARY KEY, binding_id TEXT REFERENCES yy_binding(id))`,
		`CREATE TABLE yy_binding (id TEXT PRIMARY KEY, repo_id TEXT REFERENCES xx_repo(id))`,
		`CREATE TABLE xx_repo (id TEXT PRIMARY KEY)`,
	} {
		if _, err := pool.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	capture, err := openCaptureConnection(context.Background(), path, 5000)
	if err != nil {
		t.Fatalf("open capture: %v", err)
	}
	defer func() { _ = capture.Close() }()
	groups, err := CaptureGroups(context.Background(), capture.Conn())
	if err != nil {
		t.Fatalf("CaptureGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("the chain is in %d groups, want one: %v", len(groups), groups)
	}
	want := []string{"xx_repo", "yy_binding", "zz_round"}
	got := groups[0].Tables
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("the group is ordered %v, want %v", got, want)
		}
	}
	if groups[0].Key != "xx_repo,yy_binding,zz_round" {
		t.Errorf("the group's key is %q, want the ordered tables joined: a key that changes per run is a key a marker cannot resume from",
			groups[0].Key)
	}
}

// TestACycleIsStillOrdered pins the walk's answer for a reference set no order
// satisfies: it runs, and it runs the same way twice. A walk that refuses to
// order a cyclic set is a walk that never backfills such a file at all.
func TestACycleIsStillOrdered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cycle.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = pool.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE left_side (id TEXT PRIMARY KEY, other TEXT REFERENCES right_side(id))`,
		`CREATE TABLE right_side (id TEXT PRIMARY KEY, other TEXT REFERENCES left_side(id))`,
	} {
		if _, err := pool.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	capture, err := openCaptureConnection(context.Background(), path, 5000)
	if err != nil {
		t.Fatalf("open capture: %v", err)
	}
	defer func() { _ = capture.Close() }()
	first, err := CaptureGroups(context.Background(), capture.Conn())
	if err != nil {
		t.Fatalf("CaptureGroups: %v", err)
	}
	second, err := CaptureGroups(context.Background(), capture.Conn())
	if err != nil {
		t.Fatalf("CaptureGroups again: %v", err)
	}
	if len(first) != 1 || first[0].Tables == nil {
		t.Fatalf("the cyclic pair is in %d groups with tables %v, want one group with both tables",
			len(first), first)
	}
	if len(first[0].Tables) != 2 {
		t.Fatalf("the group holds %v, want both tables of the cycle", first[0].Tables)
	}
	for i := range first[0].Tables {
		if first[0].Tables[i] != second[0].Tables[i] {
			t.Errorf("the two runs ordered the cycle differently: %v and %v", first[0].Tables, second[0].Tables)
		}
	}
}

// TestTheGroupsLeaveTheDriversOwnTablesOut pins that the grouping walks the
// file's own tables and none of the driver's: the change set is not a table to
// rewrite, and a group's key naming one would put the driver's own rows into the
// walk's bookkeeping.
func TestTheGroupsLeaveTheDriversOwnTablesOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "driver.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(`CREATE TABLE binding (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	capture, err := openCaptureConnection(context.Background(), path, 5000)
	if err != nil {
		t.Fatalf("open capture: %v", err)
	}
	defer func() { _ = capture.Close() }()
	groups, err := CaptureGroups(context.Background(), capture.Conn())
	if err != nil {
		t.Fatalf("CaptureGroups: %v", err)
	}
	for _, group := range groups {
		for _, table := range group.Tables {
			if len(table) >= 6 && table[:6] == "turso_" {
				t.Errorf("the group names the driver's own %s", table)
			}
			if len(table) >= 7 && table[:7] == "sqlite_" {
				t.Errorf("the group names the engine's own %s", table)
			}
		}
	}
}
