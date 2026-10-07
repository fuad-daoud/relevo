//go:build !modernc

package db

// The rewrite's recorded order: the change set has to carry a group's deletes
// children first and its inserts parents first, because the remote applies them
// in that order with its foreign keys enforced.

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// recorded is one change set row: which table it is about and whether it is the
// delete half or the insert half.
type recorded struct {
	table string
	kind  string
}

// changeSet reads the change set in the order the engine recorded it, which is
// the order a push sends.
func changeSet(t *testing.T, path string) []recorded {
	t.Helper()
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = pool.Close() }()
	rows, err := pool.Query(`SELECT table_name, change_type FROM turso_cdc ` +
		`WHERE table_name IS NOT NULL ORDER BY change_id`)
	if err != nil {
		t.Fatalf("read turso_cdc: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []recorded
	for rows.Next() {
		var table string
		var kind int
		if err := rows.Scan(&table, &kind); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, recorded{table: table, kind: kindName(kind)})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// kindName is what the change set calls a change: 1 is the insert half of a
// rewrite and -1 the delete half.
func kindName(kind int) string {
	switch kind {
	case 1:
		return "insert"
	case -1:
		return "delete"
	default:
		return "other"
	}
}

// position is where the change set first mentions this table and kind.
func position(set []recorded, table, kind string) int {
	for i, row := range set {
		if row.table == table && row.kind == kind {
			return i
		}
	}
	return -1
}

// seedPair makes a parent and a child table whose names put the child first
// alphabetically, with a row in each, the way a machine wrote them before
// capture was on.
func seedPair(t *testing.T, path, parent, child string) {
	t.Helper()
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = pool.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE "` + parent + `" (id TEXT PRIMARY KEY, v TEXT NOT NULL)`,
		`CREATE TABLE "` + child + `" (id TEXT PRIMARY KEY, parent_id TEXT NOT NULL REFERENCES "` + parent + `"(id))`,
		`INSERT INTO "` + parent + `" (id, v) VALUES ('p1', 'parent')`,
		`INSERT INTO "` + child + `" (id, parent_id) VALUES ('c1', 'p1')`,
	} {
		if _, err := pool.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// TestTheRewriteRecordsDeletesChildrenFirst is the property the remote enforces
// on its delete half: the delete of a row it still has children for is refused,
// so the child's delete must come before the parent's.
//
// The tables are named so that the alphabetical order the walk used to take puts
// the child first, which is the order that refuses.
func TestTheRewriteRecordsDeletesChildrenFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "order.db")
	seedPair(t, path, "zyparent", "zychild")

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
		t.Fatalf("the parent and its child are in %d groups, want one closed pair", len(groups))
	}
	if _, err := rerecordOver(context.Background(), capture.Conn(), groups[0], nil, 16); err != nil {
		t.Fatalf("rerecord: %v", err)
	}

	set := changeSet(t, path)
	childDelete := position(set, "zychild", "delete")
	parentDelete := position(set, "zyparent", "delete")
	if childDelete < 0 || parentDelete < 0 {
		t.Fatalf("the change set does not carry both deletes: %v", set)
	}
	if childDelete > parentDelete {
		t.Errorf("the child's delete is at %d and the parent's at %d, so the remote meets the parent's first: %v",
			childDelete, parentDelete, set)
	}
}

// TestTheRewriteRecordsInsertsParentsFirst is the other half: the insert of a
// row whose parent the remote does not hold is refused, so the parent's insert
// must come first.
func TestTheRewriteRecordsInsertsParentsFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "order2.db")
	seedPair(t, path, "zyparent", "zychild")

	capture, err := openCaptureConnection(context.Background(), path, 5000)
	if err != nil {
		t.Fatalf("open capture: %v", err)
	}
	defer func() { _ = capture.Close() }()

	groups, err := CaptureGroups(context.Background(), capture.Conn())
	if err != nil {
		t.Fatalf("CaptureGroups: %v", err)
	}
	if _, err := rerecordOver(context.Background(), capture.Conn(), groups[0], nil, 16); err != nil {
		t.Fatalf("rerecord: %v", err)
	}

	set := changeSet(t, path)
	parentInsert := position(set, "zyparent", "insert")
	childInsert := position(set, "zychild", "insert")
	if parentInsert < 0 || childInsert < 0 {
		t.Fatalf("the change set does not carry both inserts: %v", set)
	}
	if parentInsert > childInsert {
		t.Errorf("the parent's insert is at %d and the child's at %d, so the remote meets the child's first: %v",
			parentInsert, childInsert, set)
	}
}

// TestTheRewritePutsEveryDeleteBeforeEveryInsert pins the whole order in one
// assertion, because a change set with one insert before a delete is refused as
// surely as one with the halves the wrong way round within a table.
func TestTheRewritePutsEveryDeleteBeforeEveryInsert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "order3.db")
	seedPair(t, path, "zyparent", "zychild")

	capture, err := openCaptureConnection(context.Background(), path, 5000)
	if err != nil {
		t.Fatalf("open capture: %v", err)
	}
	defer func() { _ = capture.Close() }()

	groups, err := CaptureGroups(context.Background(), capture.Conn())
	if err != nil {
		t.Fatalf("CaptureGroups: %v", err)
	}
	if _, err := rerecordOver(context.Background(), capture.Conn(), groups[0], nil, 16); err != nil {
		t.Fatalf("rerecord: %v", err)
	}

	set := changeSet(t, path)
	firstInsert, lastDelete := -1, -1
	for i, row := range set {
		if row.kind == "insert" && firstInsert < 0 {
			firstInsert = i
		}
		if row.kind == "delete" {
			lastDelete = i
		}
	}
	if firstInsert < 0 || lastDelete < 0 {
		t.Fatalf("the change set does not carry both halves: %v", set)
	}
	if lastDelete > firstInsert {
		t.Errorf("an insert is at %d and a delete as late as %d, so the remote meets an insert against a file it is still holding the parent of: %v",
			firstInsert, lastDelete, set)
	}
}

// TestTheRewriteLeavesTheRowsItRecorded is the other half of a rewrite being a
// rewrite: the rows are still the rows, with the same values and the same
// rowids, so a change set built from them carries the right bytes and a later
// pass resumes from the right place.
func TestTheRewriteLeavesTheRowsItRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = pool.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE parent (id TEXT PRIMARY KEY, v TEXT NOT NULL, n INTEGER)`,
		`INSERT INTO parent (id, v, n) VALUES ('p1', 'parent', 7)`,
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
	if _, err := rerecordOver(context.Background(), capture.Conn(), groups[0], nil, 16); err != nil {
		t.Fatalf("rerecord: %v", err)
	}

	var id, v string
	var n, rowid int64
	if err := pool.QueryRow(`SELECT rowid, id, v, n FROM parent`).Scan(&rowid, &id, &v, &n); err != nil {
		t.Fatalf("read the row back: %v", err)
	}
	if id != "p1" || v != "parent" || n != 7 {
		t.Errorf("the row after the rewrite is (%q, %q, %d), want ('p1', 'parent', 7)", id, v, n)
	}
	if rowid != 1 {
		t.Errorf("the row is at rowid %d after the rewrite, want 1: the rowids the walk resumes from have moved", rowid)
	}
}

// TestTheRewriteIsOneTransaction pins the property a half-recorded rewrite would
// break: a rewrite whose deletes committed and whose inserts did not would leave
// the file missing rows, which is the one thing this pass must never do. The
// failure here is injected, so what is asserted is that nothing was deleted.
func TestTheRewriteIsOneTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atomic.db")
	seedPair(t, path, "zyparent", "zychild")

	capture, err := openCaptureConnection(context.Background(), path, 5000)
	if err != nil {
		t.Fatalf("open capture: %v", err)
	}
	defer func() { _ = capture.Close() }()

	groups, err := CaptureGroups(context.Background(), capture.Conn())
	if err != nil {
		t.Fatalf("CaptureGroups: %v", err)
	}
	// A table the rewrite cannot read its columns for is the failure this stands
	// in for: one after the deletes have run and before the inserts.
	broken := CaptureGroup{Key: "broken", Tables: []string{"zychild", "no_such_table"}}
	if _, err := rerecordOver(context.Background(), capture.Conn(), broken, nil, 16); err == nil {
		t.Fatal("the rewrite over a table that is not there reported success")
	}

	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = pool.Close() }()
	var n int
	if err := pool.QueryRow(`SELECT count(*) FROM zychild`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("the child table holds %d rows after a rewrite that failed, want 1: the deletes committed without the inserts", n)
	}
	_ = groups
}

// TestTheRewriteOrdersASelfReference pins the case inside one table: a row whose
// own table references it has to go back after the row it names, or the remote
// meets the reference before the parent.
func TestTheRewriteOrdersASelfReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "self.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(`CREATE TABLE fork (
		id TEXT PRIMARY KEY,
		forked_from TEXT REFERENCES fork(id))`); err != nil {
		t.Fatalf("create: %v", err)
	}
	// The child is written first in rowid order, so the read's own order is the
	// wrong one. The local foreign key is satisfied either way because the
	// parent's row is there before the rewrite reads either of them.
	for _, stmt := range []string{
		`INSERT INTO fork (rowid, id, forked_from) VALUES (2, 'parent', NULL)`,
		`INSERT INTO fork (rowid, id, forked_from) VALUES (1, 'child', 'parent')`,
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
	if _, err := rerecordOver(context.Background(), capture.Conn(), groups[0], nil, 16); err != nil {
		t.Fatalf("rerecord: %v", err)
	}

	pool2, err := OpenRawReadOnly(path)
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer func() { _ = pool2.Close() }()
	rows, err := pool2.Query(`SELECT change_type, after FROM turso_cdc ` +
		`WHERE table_name = 'fork' ORDER BY change_id`)
	if err != nil {
		t.Fatalf("read the change set: %v", err)
	}
	defer func() { _ = rows.Close() }()
	parentAt, childAt := -1, -1
	for i := 0; rows.Next(); i++ {
		var kind int
		var after sql.NullString
		if err := rows.Scan(&kind, &after); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if kind != 1 || !after.Valid {
			continue
		}
		switch {
		case strings.Contains(after.String, "parent") && !strings.Contains(after.String, "child"):
			parentAt = i
		case strings.Contains(after.String, "child"):
			childAt = i
		default:
			t.Logf("insert %d carries %q", i, after.String)
		}
	}
	if parentAt < 0 || childAt < 0 {
		t.Fatalf("the change set does not carry both inserts: parent=%d child=%d", parentAt, childAt)
	}
	if parentAt > childAt {
		t.Errorf("the parent's insert is at %d and the child's at %d, so the remote meets the child's reference first",
			parentAt, childAt)
	}
}
