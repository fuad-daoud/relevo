//go:build !modernc

package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	turso "turso.tech/database/tursogo"
)

// The change-capture pragma the sync engine sets on each of its own connections,
// and which a reverted revision of this tree also asked for on every connection
// of every writable pool over a sync member. It is spelled out here rather than
// taken from the engine because the engine must not carry it: what follows is
// the evidence for that, not a shape to re-land.
const captureConnPragma = "PRAGMA capture_data_changes_conn('full,turso_cdc')"

// tickTable is the application table both scenarios write and read.
const tickTable = `CREATE TABLE ticks (id INTEGER PRIMARY KEY, at INTEGER)`

// openScratch opens path with the turso driver alone, no pool, so a scenario can
// hold a connection and keep using it without database/sql reusing it.
func openScratch(t *testing.T, path string, busyMS int) *sql.DB {
	t.Helper()
	raw, err := sql.Open("turso", fmt.Sprintf("%s?_busy_timeout=%d", path, busyMS))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return raw
}

// seedCaptureFixture builds a scratch database that already carries the capture
// tables, so a scenario starts from a file that has captured before rather than
// one that has never turned capture on. Whether those tables are already there
// is not what decides the block; the test says so by starting from both.
func seedCaptureFixture(t *testing.T, path string, captured bool) {
	t.Helper()
	raw := openScratch(t, path, 5000)
	for _, stmt := range []string{tickTable, `PRAGMA journal_mode = wal`} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if captured {
		if _, err := raw.Exec(captureConnPragma); err != nil {
			t.Fatalf("capture pragma: %v", err)
		}
	}
}

// heldWriter keeps an open write transaction on the database, so the single
// write slot is not free from the moment the scenario starts until it is closed.
// A tick writer whose transactions overlap its next tick is the shape this
// stands in for.
type heldWriter struct {
	tx *sql.Tx
}

// startHeldWriter opens a transaction that inserts a row and never commits.
func startHeldWriter(t *testing.T, path string, busyMS int) *heldWriter {
	t.Helper()
	raw := openScratch(t, path, busyMS)
	conn, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatalf("writer connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("writer begin: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO ticks (at) VALUES (1)`); err != nil {
		t.Fatalf("writer insert: %v", err)
	}
	return &heldWriter{tx: tx}
}

func (w *heldWriter) halt() {
	if w == nil {
		return
	}
	_ = w.tx.Rollback()
}

// saturatingWriter writes its next transaction the instant the last one commits,
// so the write slot is contended rather than merely busy.
type saturatingWriter struct {
	stop chan struct{}
	done chan error
}

// startSaturatingWriter writes to path until it is stopped. Its exit error is
// reported, because a writer that died early would make a scenario look quiet
// for the wrong reason.
func startSaturatingWriter(t *testing.T, path string, busyMS int) *saturatingWriter {
	t.Helper()
	raw := openScratch(t, path, busyMS)
	conn, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatalf("writer connection: %v", err)
	}
	w := &saturatingWriter{stop: make(chan struct{}), done: make(chan error, 1)}
	go func() {
		defer close(w.done)
		defer func() { _ = conn.Close() }()
		for i := 0; ; i++ {
			select {
			case <-w.stop:
				w.done <- nil
				return
			default:
			}
			if _, err := conn.ExecContext(context.Background(),
				`INSERT INTO ticks (at) VALUES (?)`, time.Now().UnixNano()); err != nil {
				w.done <- err
				return
			}
		}
	}()
	return w
}

func (w *saturatingWriter) halt() {
	if w == nil {
		return
	}
	close(w.stop)
}

// writerErr reports why a saturating writer stopped, read after halt. A writer
// that died early would make a scenario look quiet for the wrong reason, so the
// reason is always reported.
func (w *saturatingWriter) writerErr() error {
	if w == nil {
		return nil
	}
	return <-w.done
}

// startFanoutWriter runs several gapless writers at once, the way a daemon's own
// fan-out puts several connections on one file. Whether that is enough to keep
// the write slot permanently occupied is the question, not an assumption, so the
// scenario reports what it measured.
func startFanoutWriter(t *testing.T, path string, busyMS, writers int) func() {
	t.Helper()
	fan := &saturatingWriter{stop: make(chan struct{}), done: make(chan error, writers)}
	var group sync.WaitGroup
	group.Add(writers)
	for i := 0; i < writers; i++ {
		w := startSaturatingWriter(t, path, busyMS)
		go func() {
			defer group.Done()
			<-fan.stop
			w.halt()
			fan.done <- w.writerErr()
		}()
	}
	return func() {
		close(fan.stop)
		group.Wait()
		close(fan.done)
		for err := range fan.done {
			if err != nil {
				t.Logf("fan-out writer exited: %v", err)
			}
		}
	}
}

func fanoutAsWriter(writers int) startWriter {
	return func(t *testing.T, path string, busyMS int) func() {
		return startFanoutWriter(t, path, busyMS, writers)
	}
}

// openScenarioPool builds the pool under test over an already-seeded file: the
// same connector stack openPool builds, with the capture pragma appended when
// withCapture is set. The pool is left unbounded, which is what openPool does.
func openScenarioPool(t *testing.T, path string, busyMS int, withCapture bool) *sql.DB {
	t.Helper()
	conn, err := turso.NewConnector(fmt.Sprintf("%s?_busy_timeout=%d", path, busyMS))
	if err != nil {
		t.Fatalf("connector: %v", err)
	}
	pragmas := openPragmas(false)
	if withCapture {
		pragmas = append(append([]string{}, pragmas...), captureConnPragma)
	}
	pool := sql.OpenDB(repairConnector{Connector: &pragmaConnector{base: conn, pragmas: pragmas}})
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// readOutcome is one read through the pool: how long it took and what it said.
type readOutcome struct {
	d   time.Duration
	err error
}

// readSequentially runs reads single-file queries in order. Each one needs a
// connection, so each pays for a connect and for whatever that connect's
// pragmas do. In order rather than concurrently, so the cost of a single blocked
// connect is what the numbers show.
func readSequentially(t *testing.T, pool *sql.DB, reads int) (outcomes []readOutcome, perRead time.Duration) {
	t.Helper()
	outcomes = make([]readOutcome, 0, reads)
	start := time.Now()
	for i := 0; i < reads; i++ {
		one := 0
		begin := time.Now()
		err := pool.QueryRowContext(context.Background(), `SELECT 1`).Scan(&one)
		outcomes = append(outcomes, readOutcome{time.Since(begin), err})
	}
	return outcomes, time.Since(start) / time.Duration(reads)
}

// countBlocked is how many reads waited for the write slot rather than running
// straight away, judged against the busy timeout the pool was opened with.
func countBlocked(outcomes []readOutcome, busy time.Duration) int {
	blocked := 0
	for _, o := range outcomes {
		if o.d > busy/2 {
			blocked++
		}
	}
	return blocked
}

func countFailed(outcomes []readOutcome) int {
	failed := 0
	for _, o := range outcomes {
		if o.err != nil {
			failed++
		}
	}
	return failed
}

// logOutcomes reports each read's cost and error, and returns the two counts the
// assertions are about.
func logOutcomes(t *testing.T, label string, outcomes []readOutcome, perRead, busy time.Duration) (blocked, failed int) {
	t.Helper()
	for i, o := range outcomes {
		if o.err != nil {
			t.Logf("%s: read %d took %s and failed: %v", label, i, o.d.Round(time.Millisecond), o.err)
		}
	}
	blocked = countBlocked(outcomes, busy)
	failed = countFailed(outcomes)
	t.Logf("%s: %d reads, %s each, %d waited for the write slot, %d failed",
		label, len(outcomes), perRead.Round(time.Millisecond), blocked, failed)
	return blocked, failed
}

// TestCapturePragmaOnThePoolOpenPathFailsReadsUnderWriteLockSaturation is the
// reproduction, in the failing-then-passing shape: one scenario, run twice.
//
// With the capture pragma on the pool's per-connection path, a database whose
// write slot is held or saturated turns every read into a connect that queues
// for the write slot behind the writer and then fails busy. With the pragma
// removed, the same scenario is immediate. The pragma is a write, so it belongs
// nowhere near a path every connection takes.
// startWriter opens the writer a scenario runs against, and returns the func
// that stops it. Both writer shapes are funnelled through one signature so a
// scenario can be run against either.
type startWriter func(*testing.T, string, int) func()

func heldAsWriter() startWriter {
	return func(t *testing.T, path string, busyMS int) func() {
		w := startHeldWriter(t, path, busyMS)
		return w.halt
	}
}

func saturatingAsWriter() startWriter {
	return func(t *testing.T, path string, busyMS int) func() {
		w := startSaturatingWriter(t, path, busyMS)
		return func() {
			w.halt()
			t.Logf("saturating writer stopped: %v", w.writerErr())
		}
	}
}

func TestCapturePragmaOnThePoolOpenPathFailsReadsUnderWriteLockSaturation(t *testing.T) {
	const busyMS = 300
	busy := time.Duration(busyMS) * time.Millisecond
	const reads = 4

	// The matrix discriminates the two things the pragma's behaviour could
	// depend on, apart from each other: whether the capture pragma is on the
	// pool's open path, and whether the file already carries the capture tables
	// from an earlier capture. The second is the only way CDC state left by a
	// previous open could matter, so it is varied on its own.
	for _, withCapture := range []bool{false, true} {
		for _, tablesPresent := range []bool{false, true} {
			for _, shape := range []struct {
				name  string
				start startWriter
			}{
				{"write_slot_held", heldAsWriter()},
				{"write_slot_saturated", saturatingAsWriter()},
				{"write_slot_fanout", fanoutAsWriter(4)},
			} {
				name := fmt.Sprintf("capture_pragma=%v/capture_tables_present=%v/%s",
					withCapture, tablesPresent, shape.name)
				t.Run(name, func(t *testing.T) {
					scenario(t, busyMS, busy, reads, tablesPresent, withCapture, shape.start)
				})
			}
		}
	}

	// The failing-then-passing assertion: the pragma decides the outcome, so the
	// pair is what names it.
	t.Run("pragma_decides_the_outcome", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "decides.db")
		seedCaptureFixture(t, path, true)
		writer := startHeldWriter(t, path, busyMS)
		defer writer.halt()

		withCapture := openScenarioPool(t, path, busyMS, true)
		withOutcomes, withPerRead := readSequentially(t, withCapture, reads)
		withBlocked, withFailed := logOutcomes(t, "capture on the open path", withOutcomes, withPerRead, busy)

		withoutCapture := openScenarioPool(t, path, busyMS, false)
		withoutOutcomes, withoutPerRead := readSequentially(t, withoutCapture, reads)
		withoutBlocked, withoutFailed := logOutcomes(t, "capture removed", withoutOutcomes, withoutPerRead, busy)

		if withBlocked == 0 && withFailed == 0 {
			t.Fatalf("reads through the capture pool were never blocked, so the hang did not reproduce")
		}
		if withoutBlocked != 0 || withoutFailed != 0 {
			t.Fatalf("reads through the plain pool were blocked too: %d waited, %d failed",
				withoutBlocked, withoutFailed)
		}
	})
}

// scenario runs one writer shape against one pool shape and logs what the reads
// cost. It asserts nothing on its own, because the saturating writer's timing is
// not something to assert on; the held-writer arm and the deciding arm carry the
// assertions.
func scenario(t *testing.T, busyMS int, busy time.Duration, reads int, captured, withCapture bool,
	start startWriter) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenario.db")
	seedCaptureFixture(t, path, captured)
	halt := start(t, path, busyMS)
	defer halt()

	pool := openScenarioPool(t, path, busyMS, withCapture)
	pool.SetMaxIdleConns(0)
	outcomes, perRead := readSequentially(t, pool, reads)
	label := "without capture"
	if withCapture {
		label = "with capture"
	}
	logOutcomes(t, label, outcomes, perRead, busy)
	t.Logf("pool stats: %+v", pool.Stats())
}

// pinWal leaves path in the state a lived-in file has and a fresh fixture does
// not: a reader holding a snapshot so no checkpoint can move past it, a
// checkpoint threshold low enough that every commit would have to backfill, and a
// log grown well past the point where it would otherwise have been checkpointed.
func pinWal(t *testing.T, path string, busyMS int) {
	t.Helper()
	raw := openScratch(t, path, busyMS)
	conn, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	for _, p := range []string{`PRAGMA wal_autocheckpoint = 1`, `PRAGMA journal_size_limit = 0`} {
		if _, err := conn.ExecContext(context.Background(), p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	// One row, so the pinning reader has a snapshot over something.
	if _, err := conn.ExecContext(context.Background(),
		`INSERT INTO ticks (at) VALUES (1)`); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	pinner, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatalf("pinner: %v", err)
	}
	defer func() { _ = pinner.Close() }()
	if _, err := pinner.ExecContext(context.Background(), `BEGIN`); err != nil {
		t.Fatalf("pinner begin: %v", err)
	}
	rows, err := pinner.QueryContext(context.Background(), `SELECT * FROM ticks`)
	if err != nil {
		t.Fatalf("pinner query: %v", err)
	}
	if !rows.Next() {
		t.Fatal("pinner saw no rows")
	}
	defer func() { _ = rows.Close() }()

	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("grow begin: %v", err)
	}
	blob := strings.Repeat("x", 4096)
	for i := 0; i < 400; i++ {
		if _, err := tx.ExecContext(context.Background(),
			`INSERT INTO ticks (at) VALUES (?)`, blob); err != nil {
			t.Fatalf("grow insert %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("grow commit: %v", err)
	}
	walBytes := int64(-1)
	if fi, err := os.Stat(path + "-wal"); err == nil {
		walBytes = fi.Size()
	}
	t.Logf("wal pinned at %d bytes with a reader holding its snapshot", walBytes)
}

// TestCapturePragmaBlockTracksTheWriteSlotNotTheWal discriminates the write
// slot from the write-ahead log. A log that cannot be backfilled is one of the
// things a lived-in file has and a scratch fixture does not, so it is built
// here: a reader that never finishes pins the log, autocheckpoint is turned down
// so every commit would have to backfill, and the log is grown well past the
// point where it would otherwise be checkpointed.
//
// The capture pragma is then asked for twice over that file, once with the write
// slot free and once with it held. If the log were what the block is about, the
// first ask would block too.
func TestCapturePragmaBlockTracksTheWriteSlotNotTheWal(t *testing.T) {
	const busyMS = 300
	busy := time.Duration(busyMS) * time.Millisecond

	path := filepath.Join(t.TempDir(), "pinned.db")
	seedCaptureFixture(t, path, true)
	pinWal(t, path, busyMS)

	freeSlot := openScenarioPool(t, path, busyMS, true)
	outcomes, perRead := readSequentially(t, freeSlot, 4)
	blocked, failed := logOutcomes(t, "pinned wal, write slot free", outcomes, perRead, busy)
	if blocked != 0 || failed != 0 {
		t.Fatalf("the capture pragma blocked on a pinned wal with the write slot free: %d waited, %d failed",
			blocked, failed)
	}

	held := startHeldWriter(t, path, busyMS)
	defer held.halt()
	// A second pool, because the first one's connections were built while the
	// write slot was free: reusing them would ask nothing of the write slot.
	heldSlot := openScenarioPool(t, path, busyMS, true)
	outcomes, perRead = readSequentially(t, heldSlot, 4)
	blocked, failed = logOutcomes(t, "pinned wal, write slot held", outcomes, perRead, busy)
	if blocked == 0 && failed == 0 {
		t.Fatal("the capture pragma did not block once the write slot was held")
	}
}

// TestOpenPragmasCarryNoCapturePragma is the guard that keeps the reverted
// behaviour out. The engine's per-connection list is what a pool applies to
// every connection it builds, so a capture pragma appearing there is the reverted
// revision, whatever the rest of the tree says.
func TestOpenPragmasCarryNoCapturePragma(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		for _, p := range openPragmas(readOnly) {
			if strings.Contains(strings.ToLower(p), "capture_data_changes") {
				t.Errorf("openPragmas(%v) carries the capture pragma %q", readOnly, p)
			}
		}
	}
}

// TestCapturePragmaIsAWrite pins the mechanism the reproduction rests on: the
// pragma queues for the database write slot, so it waits while another
// connection holds it, while a pragma that only configures the connection does
// not wait at all.
func TestCapturePragmaIsAWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "write.db")
	raw := openScratch(t, path, 5000)
	if _, err := raw.Exec(tickTable); err != nil {
		t.Fatalf("create: %v", err)
	}

	holder, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	defer func() { _ = holder.Close() }()
	tx, err := holder.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO ticks (at) VALUES (1)`); err != nil {
		t.Fatalf("held insert: %v", err)
	}

	// The second connection is the one that runs the pragmas, so what it waits
	// for is the write slot the first connection is holding.
	second, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatalf("second connection: %v", err)
	}
	defer func() { _ = second.Close() }()

	// The configuring pragma returns at once on that same held database.
	start := time.Now()
	if _, err := second.ExecContext(context.Background(), `PRAGMA temp_store = MEMORY`); err != nil {
		t.Fatalf("temp_store: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("a pragma that only configures the connection took %s under a held write lock", d)
	}

	// The capture pragma is a write, so it waits, and then reports busy.
	done := make(chan error, 1)
	go func() {
		_, err := second.ExecContext(context.Background(), captureConnPragma)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the capture pragma succeeded while another connection held the write lock")
		}
		t.Logf("capture pragma under a held write lock: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("the capture pragma neither returned nor failed")
	}
}
