//go:build !modernc

package sync

// The backfill at mark time, end to end over a scratch member file: the rows a
// machine wrote before capture was on, the enable that turns it on, and the
// change set the push that follows reads.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// seedLocal opens the machine-local file a marker's rows live in.
func seedLocal(t *testing.T) *db.DB {
	t.Helper()
	local, err := db.Open(filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatalf("open the machine-local file: %v", err)
	}
	t.Cleanup(func() { _ = local.Close() })
	return local
}

// seedMember writes rows into a file the way the daemon does -- through the
// handle's own pool, before any capture was on -- so the file holds rows that
// are in no change set.
func seedMember(t *testing.T, path string, tables map[string]int) {
	t.Helper()
	shared, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("open the shared file's pool: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	for table, rows := range tables {
		if _, err := shared.Exec(`CREATE TABLE IF NOT EXISTS ` + table + ` (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
		for i := range rows {
			if _, err := shared.Exec(`INSERT INTO `+table+` (id, body) VALUES (?, ?)`, i, "pre-capture"); err != nil {
				t.Fatalf("insert into %s: %v", table, err)
			}
		}
	}
}

// changeSetRows is how many rows the change set holds for one table, counted the
// way a push counts them.
func changeSetRows(t *testing.T, path, table string) int {
	t.Helper()
	pool, err := db.OpenRawReadOnly(path)
	if err != nil {
		t.Fatalf("open the member read-only: %v", err)
	}
	defer func() { _ = pool.Close() }()
	var n int
	if err := pool.QueryRow(`SELECT COUNT(*) FROM turso_cdc WHERE table_name = ?`, table).Scan(&n); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			// The capture tables do not exist until something asked for capture,
			// so their absence is a change set of nothing rather than a failure.
			return 0
		}
		t.Fatalf("count the change set for %s: %v", table, err)
	}
	return n
}

// TestTheBackfillPutsPreCaptureRowsIntoTheChangeSet is the acceptance shape: a
// machine holding rows the daemon wrote before capture was turned on, and the
// pass that puts them where a push can carry them.
func TestTheBackfillPutsPreCaptureRowsIntoTheChangeSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	seedMember(t, path, map[string]int{"binding": 6})
	local := seedLocal(t)

	if got := changeSetRows(t, path, "binding"); got != 0 {
		t.Fatalf("the seeded rows are already in the change set (%d), so the gap this closes is not there", got)
	}

	res, err := Backfill(context.Background(), local, path)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if res.Rows != 6 {
		t.Errorf("the backfill recorded %d rows, want 6", res.Rows)
	}
	if got := changeSetRows(t, path, "binding"); got == 0 {
		t.Error("the change set is still empty, so the push that follows cannot carry those rows")
	}

	// And the rows are still the rows: a backfill that rewrote them into
	// something else would carry the wrong bytes.
	pool, err := db.OpenRawReadOnly(path)
	if err != nil {
		t.Fatalf("open the member read-only: %v", err)
	}
	defer func() { _ = pool.Close() }()
	var n int
	if err := pool.QueryRow(`SELECT COUNT(*) FROM binding WHERE body = 'pre-capture'`).Scan(&n); err != nil {
		t.Fatalf("count the table's own rows: %v", err)
	}
	if n != 6 {
		t.Errorf("the table holds %d of its own rows after the backfill, want 6", n)
	}
}

// TestTheBackfillRecordsParentsBeforeChildren pins the order the change set has
// to carry for a remote with foreign keys enforced, across the re-land boundary:
// rows a machine wrote before capture was on are in the file and in no change
// set, and the pass that puts them there has to put the parent before the child.
//
// The tables are named so the alphabetical order the pass used to take walks the
// child first, which is the order the remote refuses. The assertion reads the
// change set the engine recorded, which is the order the push sends.
func TestTheBackfillRecordsParentsBeforeChildren(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	seedReferencingMember(t, path)
	local := seedLocal(t)

	res, err := Backfill(context.Background(), local, path)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if res.Rows < 2 {
		t.Fatalf("the backfill recorded %d rows, want the parent and the child", res.Rows)
	}

	order := changeSetOrder(t, path)
	parent := indexOf(order, "fk_parent", "insert")
	child := indexOf(order, "fk_child", "insert")
	if parent < 0 || child < 0 {
		t.Fatalf("the change set carries %v, want an insert for each table", order)
	}
	if parent > child {
		t.Errorf("the parent's insert is at %d and the child's at %d, so the remote meets the child's row before the row it names: %v",
			parent, child, order)
	}
}

// TestTheBackfillDeletesChildrenBeforeParents pins the other half, which is the
// one that refused even when the tables were named in order: a rewrite of rows
// the remote already holds carries a delete of a parent the remote still has
// children for.
func TestTheBackfillDeletesChildrenBeforeParents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	seedReferencingMember(t, path)
	local := seedLocal(t)

	if _, err := Backfill(context.Background(), local, path); err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	// A second pass over a fresh machine-local file, so the marker does not say
	// the walk is finished: this is the same rows recorded again, which is what
	// a machine whose enable ran twice would do.
	second := seedLocal(t)
	if _, err := Backfill(context.Background(), second, path); err != nil {
		t.Fatalf("the second Backfill: %v", err)
	}

	order := changeSetOrder(t, path)
	// The second pass's own entries are the last of each kind, so the positions
	// compared are taken from the tail rather than the first the file ever held.
	childDelete := lastIndexOf(order, "fk_child", "delete")
	parentDelete := lastIndexOf(order, "fk_parent", "delete")
	if childDelete < 0 || parentDelete < 0 {
		t.Fatalf("the second pass recorded no deletes: %v", order)
	}
	if childDelete > parentDelete {
		t.Errorf("the child's delete is at %d and the parent's at %d, so the remote meets the parent's delete while it still holds the child: %v",
			childDelete, parentDelete, order)
	}
}

// seedReferencingMember writes a parent and a child table with a row in each, the
// way a machine wrote them before capture was on: through the file's own pool,
// which records nothing.
func seedReferencingMember(t *testing.T, path string) {
	t.Helper()
	pool, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("open the shared file's pool: %v", err)
	}
	defer func() { _ = pool.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE fk_parent (id TEXT PRIMARY KEY, v TEXT NOT NULL)`,
		`CREATE TABLE fk_child (id TEXT PRIMARY KEY, parent_id TEXT NOT NULL REFERENCES fk_parent(id))`,
	} {
		if _, err := pool.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := pool.Exec(`INSERT INTO fk_parent (id, v) VALUES ('p1', 'parent')`); err != nil {
		t.Fatalf("seed the parent: %v", err)
	}
	if _, err := pool.Exec(`INSERT INTO fk_child (id, parent_id) VALUES ('c1', 'p1')`); err != nil {
		t.Fatalf("seed the child: %v", err)
	}
}

// changeSetOrder is the change set as the push reads it: one entry per table and
// half, in the order the engine recorded them.
func changeSetOrder(t *testing.T, path string) []string {
	t.Helper()
	pool, err := db.OpenRawReadOnly(path)
	if err != nil {
		t.Fatalf("open the member read-only: %v", err)
	}
	defer func() { _ = pool.Close() }()
	rows, err := pool.Query(`SELECT table_name, change_type FROM turso_cdc ` +
		`WHERE table_name IS NOT NULL ORDER BY change_id`)
	if err != nil {
		t.Fatalf("read the change set: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var order []string
	for rows.Next() {
		var table string
		var kind int
		if err := rows.Scan(&table, &kind); err != nil {
			t.Fatalf("read a change set row: %v", err)
		}
		half := "insert"
		if kind < 0 {
			half = "delete"
		}
		order = append(order, table+":"+half)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("the change set rows: %v", err)
	}
	return order
}

// indexOf is where the change set first names this table and half, or -1.
func indexOf(order []string, table, half string) int {
	want := table + ":" + half
	for i, entry := range order {
		if entry == want {
			return i
		}
	}
	return -1
}

// lastIndexOf is where the change set last names this table and half, or -1. A
// second pass appends to the same change set, so the pass under test is the tail.
func lastIndexOf(order []string, table, half string) int {
	want := table + ":" + half
	for i := len(order) - 1; i >= 0; i-- {
		if order[i] == want {
			return i
		}
	}
	return -1
}

// TestASecondBackfillOverTheSameFileRecordsNothing pins resumability from the
// other end: once the marker is cleared a finished walk does not run again, so a
// later enable does not re-record a database whose rows are all captured.
func TestASecondBackfillOverTheSameFileRecordsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	seedMember(t, path, map[string]int{"binding": 3})
	local := seedLocal(t)

	if _, err := Backfill(context.Background(), local, path); err != nil {
		t.Fatalf("the first backfill: %v", err)
	}
	marker, ok, err := ReadBackfill(local)
	if err != nil {
		t.Fatalf("ReadBackfill: %v", err)
	}
	if !ok {
		t.Fatal("the marker was removed by a walk that reached the end, so a later pass cannot tell this file was walked")
	}
	if !marker.Complete {
		t.Error("the marker does not say the walk finished")
	}

	before := changeSetRows(t, path, "binding")
	if _, err := Backfill(context.Background(), local, path); err != nil {
		t.Fatalf("the second backfill: %v", err)
	}
	if after := changeSetRows(t, path, "binding"); after != before {
		t.Errorf("the second backfill grew the change set from %d to %d", before, after)
	}
}

// TestTheBackfillResumesFromItsMarker pins the resumable half: an interrupted
// pass leaves the tables it had not finished marked at the rowid it reached, and
// the next pass continues from there rather than starting over.
func TestTheBackfillResumesFromItsMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	seedMember(t, path, map[string]int{"binding": 4})
	local := seedLocal(t)

	// A marker that says the table was walked to rowid 2, which is where an
	// interrupted pass would have left it.
	if err := WriteBackfill(local, backfillMarker{
		Tables: map[string]backfillProgress{"binding": {At: 2}},
		Rows:   2,
	}); err != nil {
		t.Fatalf("WriteBackfill: %v", err)
	}

	res, err := Backfill(context.Background(), local, path)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	// Two rows below the marker, so the pass recorded the two it had not.
	if res.Rows != 2 {
		t.Errorf("the resumed pass recorded %d rows, want the 2 below the marker", res.Rows)
	}
}

// TestTheBackfillIsBounded pins the budget: a table larger than the budget is
// left part-walked, the marker says so, and the pass says it was not finished --
// so the caller knows another attempt is due rather than believing the rows are
// all carried.
func TestTheBackfillIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("the bounded walk writes BackfillBudget rows")
	}
	path := filepath.Join(t.TempDir(), "relevo.db")
	seedMember(t, path, map[string]int{"binding": BackfillBudget + 8})
	local := seedLocal(t)

	res, err := Backfill(context.Background(), local, path)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if res.Rows > BackfillBudget {
		t.Errorf("the pass recorded %d rows, want at most the budget of %d", res.Rows, BackfillBudget)
	}
	if !res.Exhausted {
		t.Error("the pass stopped short of the budget but did not say it was unfinished")
	}
	marker, ok, err := ReadBackfill(local)
	if err != nil {
		t.Fatalf("ReadBackfill: %v", err)
	}
	if !ok {
		t.Fatal("the marker was cleared although the walk did not finish, so the remaining rows would never be walked")
	}
	if marker.Complete {
		t.Error("the marker says the walk finished when the budget stopped part way through")
	}
	if marker.Tables["binding"].Done {
		t.Error("the marker says the table was finished when the budget stopped part way through it")
	}

	// The rest is what the next attempt is for.
	resumed, err := Backfill(context.Background(), local, path)
	if err != nil {
		t.Fatalf("the resumed backfill: %v", err)
	}
	if resumed.Rows == 0 {
		t.Error("the resumed pass recorded nothing, so the rows the budget left are still in no change set")
	}
}

// TestABackfillOverAGroupThatTakesMoreThanOneCallConverges pins the shape every
// other fixture misses: a group whose walk needs more than one call, so its small
// table is walked to the end long before its large one is.
//
// A pass that hands a finished table back to the rewrite records that table
// again on every call and every pass, so the marker's rows run to a multiple of
// the database's own and the walk never converges. The child here is four rows
// and the parent is larger than the budget, so the child is done in the first
// call and must not be named again by the twenty-odd calls after it.
func TestABackfillOverAGroupThatTakesMoreThanOneCallConverges(t *testing.T) {
	if testing.Short() {
		t.Skip("the walk writes more than the budget")
	}
	const children = 4
	const parents = BackfillBudget + 8
	path := filepath.Join(t.TempDir(), "relevo.db")
	seedReferencingRows(t, path, parents, children)
	local := seedLocal(t)

	var total int64
	for pass := range 8 {
		marker, _, err := ReadBackfill(local)
		if err != nil {
			t.Fatalf("pass %d: ReadBackfill: %v", pass+1, err)
		}
		if marker.Complete {
			break
		}
		res, err := Backfill(context.Background(), local, path)
		if err != nil {
			t.Fatalf("pass %d: Backfill: %v", pass+1, err)
		}
		total += res.Rows
		if res.Rows == 0 {
			t.Fatalf("pass %d recorded nothing and left the walk at %+v", pass+1, marker.Tables)
		}
	}

	marker, ok, err := ReadBackfill(local)
	if err != nil {
		t.Fatalf("ReadBackfill: %v", err)
	}
	if !ok || !marker.Complete {
		t.Fatalf("the walk did not converge in 8 passes: %+v", marker.Tables)
	}

	// The walk records every row once, so the marker's total is the database's own
	// row count rather than a multiple of it.
	if want := int64(parents + children); total != want {
		t.Errorf("the walk recorded %d rows over every pass, want the %d the file holds, so finished tables were recorded again", total, want)
	}

	// The finished table keeps where it got to: a resume point reset to zero is
	// what hands the table back to the rewrite.
	if at := marker.Tables["fk_child"].At; at != children+1 {
		t.Errorf("the finished table's resume point is %d, want %d, so it is walked again from its first row", at, children+1)
	}

	// And the change set says the same thing the marker does: each row of the
	// small table was recorded once, as the delete and the insert of one rewrite.
	if got, want := changeSetRows(t, path, "fk_child"), 2*children; got != want {
		t.Errorf("the change set holds %d rows for the finished table, want %d, so it was rewritten more than once", got, want)
	}
}

// seedReferencingRows writes a parent table with the given number of rows and a
// child table with its own, every child naming a parent row that is there, so
// the two tables are one foreign-key-closed group whose walk takes as many calls
// as their sizes ask for.
func seedReferencingRows(t *testing.T, path string, parents, children int) {
	t.Helper()
	pool, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("open the shared file's pool: %v", err)
	}
	defer func() { _ = pool.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE fk_parent (id INTEGER PRIMARY KEY, v TEXT NOT NULL)`,
		`CREATE TABLE fk_child (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES fk_parent(id))`,
	} {
		if _, err := pool.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	for i := range parents {
		if _, err := pool.Exec(`INSERT INTO fk_parent (id, v) VALUES (?, ?)`, i+1, "parent"); err != nil {
			t.Fatalf("seed parent %d: %v", i, err)
		}
	}
	for i := range children {
		if _, err := pool.Exec(`INSERT INTO fk_child (id, parent_id) VALUES (?, ?)`, i+1, 1); err != nil {
			t.Fatalf("seed child %d: %v", i, err)
		}
	}
}

// TestTheEnableRunsTheBackfillBeforeItsFirstPush pins the ordering, which is the
// whole of where the pass belongs: after the seed decision, before the first
// push. A pass that ran after the push would leave its rows for the next one, and
// an enable whose first push is the seed would seed a remote without them.
func TestTheEnableRunsTheBackfillBeforeItsFirstPush(t *testing.T) {
	local := seedLocal(t)
	var order []string
	enabler := &Enabler{
		Local:     local,
		Intake:    TokenIntake{FromStdin: true, Stdin: []byte("token")},
		RemoteURL: "libsql://relevo-test.invalid",
		Preflight: func() db.Preflight {
			order = append(order, "preflight")
			return db.Preflight{}
		},
		LocalHasHistory: func() (bool, error) { return true, nil },
		CloudEmpty:      func(context.Context, Settings, []byte) (bool, error) { return true, nil },
		SeedPath:        filepath.Join(t.TempDir(), "relevo-seed.db"),
		Open: func(context.Context, OpenConfig) (SyncClient, error) {
			order = append(order, "open")
			return &orderRecorder{calls: &order}, nil
		},
		Backfill: func(context.Context) (BackfillResult, error) {
			order = append(order, "backfill")
			return BackfillResult{Rows: 6}, nil
		},
	}
	res, err := enabler.Enable(context.Background())
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if res.Backfill.Rows != 6 {
		t.Errorf("the enable reported %d backfilled rows, want 6", res.Backfill.Rows)
	}
	want := []string{"preflight", "open", "backfill", "push", "pull"}
	if len(order) != len(want) {
		t.Fatalf("the enable ran %v, want %v", order, want)
	}
	for i, step := range want {
		if order[i] != step {
			t.Errorf("step %d was %q, want %q (full order %v)", i, order[i], step, order)
		}
	}
}

// orderRecorder is a client that writes each call into the shared order.
type orderRecorder struct{ calls *[]string }

func (c *orderRecorder) Push(context.Context) error {
	*c.calls = append(*c.calls, "push")
	return nil
}

func (c *orderRecorder) Pull(context.Context) (bool, error) {
	*c.calls = append(*c.calls, "pull")
	return false, nil
}

func (c *orderRecorder) Stats(context.Context) (Stats, error) { return Stats{}, nil }
func (c *orderRecorder) Checkpoint(context.Context) error     { return nil }

// TestABackfillFailureIsReported pins that a pass that did not run is a failure
// the caller hears about. A backfill that swallowed its error would leave rows
// the push cannot carry while the enable reported success.
func TestABackfillFailureIsReported(t *testing.T) {
	local := seedLocal(t)
	enabler := &Enabler{
		Local:           local,
		Preflight:       func() db.Preflight { return db.Preflight{} },
		LocalHasHistory: func() (bool, error) { return true, nil },
		CloudEmpty:      func(context.Context, Settings, []byte) (bool, error) { return true, nil },
		SeedPath:        filepath.Join(t.TempDir(), "relevo-seed.db"),
		Intake:          TokenIntake{FromStdin: true, Stdin: []byte("token")},
		Open: func(context.Context, OpenConfig) (SyncClient, error) {
			return &orderRecorder{calls: &[]string{}}, nil
		},
		Backfill: func(context.Context) (BackfillResult, error) {
			return BackfillResult{}, context.DeadlineExceeded
		},
	}
	if _, err := enabler.Enable(context.Background()); err == nil {
		t.Fatal("the enable reported success although its backfill did not run")
	}
}
