//go:build !modernc

package db

// The capture open against a file the daemon is writing to. Every other capture
// fixture in this package opens a quiet TempDir file nobody else holds: one
// writer that stops. A lived-in database has a daemon writing over it every
// tick, and the backfill that opens the capture connection runs inside that same
// daemon, so the open races the writes rather than following them.

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// churnMember is a member file the daemon's tick is already writing over, the
// shape the backfill's open meets in production: rows in no change set, a writer
// already transacting, and the open arriving from inside the same process.
func churnMember(t *testing.T, rows int) (*DB, string, *churner) {
	t.Helper()
	handle, path := memberFile(t)
	// The pre-capture rows are written through a raw pool, before anything asked
	// for capture, so they are in the database and in no change set.
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open the shared file's pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if _, err := pool.Exec(`CREATE TABLE ticks (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create ticks: %v", err)
	}
	for i := range rows {
		if _, err := pool.Exec(`INSERT INTO ticks (id) VALUES (?)`, "pre-"+strconv.Itoa(i)); err != nil {
			t.Fatalf("seed ticks %d: %v", i, err)
		}
	}
	// One write through the handle is what a lived-in file has already done by
	// the time the backfill runs: it is the transaction that opens the path's
	// capture connection, so the fixture's precondition holds before the open is
	// asked for rather than depending on the churn's timing.
	if err := handle.Tx(func(tx *Tx) error { return tx.KVPut("tick", []byte(`{"n":0}`)) }); err != nil {
		t.Fatalf("the tick's first write: %v", err)
	}
	return handle, path, churn(t, handle)
}

// churn writes over the member file through the handle until the test ends, so
// the open under test competes with a writer that never stops. It counts the
// writes that landed and keeps the first failure, because a writer that stopped
// on an error would quietly turn the fixture back into the quiet file every other
// capture test opens.
func churn(t *testing.T, handle *DB) *churner {
	t.Helper()
	c := &churner{done: make(chan struct{})}
	go func() {
		for i := 1; ; i++ {
			select {
			case <-c.done:
				return
			default:
			}
			err := handle.Tx(func(tx *Tx) error {
				return tx.KVPut("tick", []byte(`{"n":`+strconv.Itoa(i)+`}`))
			})
			if err != nil {
				c.mu.Lock()
				c.err = err
				c.mu.Unlock()
				return
			}
			c.writes.Add(1)
		}
	}()
	t.Cleanup(func() { close(c.done) })
	return c
}

// churner is what the fixture's writer reports back: how many of its writes
// landed, and the first that failed.
type churner struct {
	done   chan struct{}
	writes atomic.Int64
	mu     sync.Mutex
	err    error
}

// healthy reports whether the writer kept writing: a walk that is serialised
// against the daemon's writes costs the daemon nothing, so a failure here is the
// interleaving the gate exists to prevent.
func (c *churner) healthy() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if c.writes.Load() == 0 {
		return errors.New("the fixture's writer never landed a write")
	}
	return nil
}

// TestTheCaptureOpenCompletesUnderAChurningWriter is the reproduction: an open
// over a file the daemon is writing to must finish inside its bound. The pool the
// open draws from is one connection wide, and the daemon's own writer already
// holds that connection for as long as the file is a member, so an open that
// asks the pool for a connection of its own waits for a slot nothing releases.
func TestTheCaptureOpenCompletesUnderAChurningWriter(t *testing.T) {
	_, path, _ := churnMember(t, 8)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	capture, err := OpenCapture(ctx, path, 500)
	if err != nil {
		t.Fatalf("the capture open over a file the daemon is writing: %v", err)
	}
	defer func() { _ = capture.Close() }()
}

// TestABackfillOverAChurningFileRecordsRealChanges is the property the pass
// exists for, measured on the change set itself: the rows the walk rewrites
// reach turso_cdc as the engine's own writes, over a file that is busy the whole
// time. The churning writer touches another table, so a change-set row for this
// one can only have come from the walk.
func TestABackfillOverAChurningFileRecordsRealChanges(t *testing.T) {
	_, path, _ := churnMember(t, 8)
	if got := capturedChanges(t, path, "ticks"); got != 0 {
		t.Fatalf("the seeded rows are already in the change set (%d changes), so the gap this closes is not there", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	capture, err := OpenCapture(ctx, path, 500)
	if err != nil {
		t.Fatalf("the capture open over a file the daemon is writing: %v", err)
	}
	defer func() { _ = capture.Close() }()

	rows, _, err := rewriteTableOver(ctx, capture, "ticks", -1, 16)
	if err != nil {
		t.Fatalf("the rewrite over a churning file: %v", err)
	}
	if rows != 8 {
		t.Errorf("the rewrite recorded %d rows, want 8", rows)
	}
	if got := capturedChanges(t, path, "ticks"); got == 0 {
		t.Error("the backfill left the change set empty, so the pre-capture rows are still in no change set")
	}
}

// TestTheTablesAreReadableOverAChurningFile pins the read the pass takes before
// it walks anything: the file's own table list, over a file being written to the
// whole time.
func TestTheTablesAreReadableOverAChurningFile(t *testing.T) {
	_, path, _ := churnMember(t, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	capture, err := OpenCapture(ctx, path, 500)
	if err != nil {
		t.Fatalf("the capture open over a file the daemon is writing: %v", err)
	}
	defer func() { _ = capture.Close() }()

	tables, err := capture.Tables(ctx)
	if err != nil {
		t.Fatalf("read the tables over a churning file: %v", err)
	}
	if !slicesContains(tables, "ticks") {
		t.Errorf("the table list over a churning file is %v, want it to name ticks", tables)
	}
}

// TestTheCaptureGateHandsOverOneWriterAtATime pins what the gate is for: the walk
// and the daemon's writes share one connection, so a second caller waits rather
// than interleaving its BEGIN and COMMIT with the first's. The wait is bounded by
// the caller's own context, which is what stops a daemon write from parking
// behind a walk that has already given up.
func TestTheCaptureGateHandsOverOneWriterAtATime(t *testing.T) {
	_, path, _ := churnMember(t, 4)

	held, err := captureConnFor(context.Background(), path, 500)
	if err != nil {
		t.Fatalf("the held capture connection: %v", err)
	}
	_, release, err := held.acquire(context.Background())
	if err != nil {
		t.Fatalf("take the connection: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, _, err := held.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a second writer over the held connection = %v, want the caller's own deadline", err)
	}
	release()

	// The token is back, so the next writer gets the connection rather than
	// waiting for a hand-back that already happened.
	after, cancelAfter := context.WithTimeout(context.Background(), time.Second)
	defer cancelAfter()
	if _, release, err = held.acquire(after); err != nil {
		t.Fatalf("take the connection again: %v", err)
	}
	release()
}

// TestTheWalkBatchesOverAChurningFile pins that the walk really walks: many
// rows, several batches, and the daemon writing the whole time. It is the
// end-to-end shape the gate protects, and the writer's own writes are checked
// afterwards because a walk that costs the daemon a failure has moved the cost
// rather than removed it.
func TestTheWalkBatchesOverAChurningFile(t *testing.T) {
	_, path, writer := churnMember(t, 64)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	capture, err := OpenCapture(ctx, path, 500)
	if err != nil {
		t.Fatalf("the capture open over a file the daemon is writing: %v", err)
	}
	defer func() { _ = capture.Close() }()

	var (
		at      int64 = -1
		batches int
		total   int64
	)
	for {
		rows, next, err := rewriteTableOver(ctx, capture, "ticks", at, 8)
		if err != nil {
			t.Fatalf("rewrite batch %d over a churning file: %v", batches, err)
		}
		batches++
		total += rows
		if rows < 8 {
			break
		}
		at = next
		if batches > 20 {
			t.Fatal("the walk did not finish")
		}
	}
	if total != 64 {
		t.Errorf("the walk recorded %d rows, want 64", total)
	}
	if batches < 2 {
		t.Errorf("the walk took %d batches over 64 rows, want it batched", batches)
	}
	if err := writer.healthy(); err != nil {
		t.Errorf("the daemon's own writes failed while the walk ran: %v", err)
	}
}

// TestAFinishedWalkLeavesTheDaemonsWritesAlone pins that giving a borrowed
// connection back does not close the one the daemon's own writes go through. A
// walk that took the daemon's writes down with it would lose every row the tick
// wrote while it ran, which is a worse failure than the one the borrow avoids.
func TestAFinishedWalkLeavesTheDaemonsWritesAlone(t *testing.T) {
	handle, path, writer := churnMember(t, 4)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	capture, err := OpenCapture(ctx, path, 500)
	if err != nil {
		t.Fatalf("the capture open over a file the daemon is writing: %v", err)
	}
	if _, _, err := rewriteTableOver(ctx, capture, "ticks", -1, 16); err != nil {
		t.Fatalf("the rewrite: %v", err)
	}
	if err := capture.Close(); err != nil {
		t.Fatalf("close the borrowed connection: %v", err)
	}

	// The handle writes after the walk gave the connection back, so a close that
	// took the held connection with it fails here rather than silently dropping
	// the row.
	if err := handle.Tx(func(tx *Tx) error { return tx.KVPut("tick", []byte(`{"n":999}`)) }); err != nil {
		t.Fatalf("the tick's write after the walk closed: %v", err)
	}
	if err := writer.healthy(); err != nil {
		t.Errorf("the daemon's own writes failed across the walk: %v", err)
	}
}

// TestACaptureOpenNamesTheStepItBlocked pins the bound's other half: a failure
// says which step of the open ran out of time, so a reader is not left choosing
// between the pool, the pragma and the first captured write.
func TestACaptureOpenNamesTheStepItBlocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "steps.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open the pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	// The capture pool is one connection wide; take it, so its own checkout is
	// the step that cannot complete.
	capturePool, err := capturePoolFor(path, 200)
	if err != nil {
		t.Fatalf("the capture pool: %v", err)
	}
	holder, err := capturePool.Conn(context.Background())
	if err != nil {
		t.Fatalf("hold the capture connection: %v", err)
	}
	defer func() { _ = holder.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	capture, err := OpenCapture(ctx, path, 200)
	if err == nil {
		_ = capture.Close()
		t.Fatal("the capture open succeeded while its own connection was checked out")
	}
	if !strings.Contains(err.Error(), "the capture connection") {
		t.Errorf("the failed open named no step: %v", err)
	}
}

// slicesContains reports whether the table list names the table, without a
// second import in a file that has no other use for one.
func slicesContains(tables []string, name string) bool {
	for _, table := range tables {
		if table == name {
			return true
		}
	}
	return false
}
