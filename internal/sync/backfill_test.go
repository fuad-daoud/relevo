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
