//go:build !modernc

package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestThePoolBoundsItsOpenConnections pins the ceiling openPool puts on a pool,
// and that a caller past it waits rather than opening another connection. The
// wait is the whole point: without a ceiling every concurrent caller opens its
// own, and each of those connects pays the file's own open cost against a
// database that has a single write slot behind it.
func TestThePoolBoundsItsOpenConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open the pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	if got := pool.Stats().MaxOpenConnections; got != maxOpenConns {
		t.Errorf("MaxOpenConnections = %d, want %d", got, maxOpenConns)
	}

	// More callers than the ceiling, each holding its connection for as long as
	// it can, so the pool is saturated rather than merely busy.
	hold := make(chan struct{})
	var wg sync.WaitGroup
	var peak atomic.Int64
	for range maxOpenConns + 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := pool.Conn(context.Background())
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			note(&peak, int64(pool.Stats().OpenConnections))
			<-hold
		}()
	}
	waitFor(t, func() bool { return pool.Stats().WaitCount > 0 },
		"the pool never made a caller wait, so the ceiling was never reached")
	note(&peak, int64(pool.Stats().OpenConnections))

	if got := pool.Stats().OpenConnections; got > maxOpenConns {
		t.Errorf("the pool opened %d connections, want at most %d", got, maxOpenConns)
	}
	close(hold)
	wg.Wait()
}

// TestABlockedOpenSurfacesAsAnError pins the failure the ceiling is for. With
// the write slot held by another connection, a caller that needs a fresh
// connection cannot get one; it must be told so within a bound rather than
// opening connections without end.
func TestABlockedOpenSurfacesAsAnError(t *testing.T) {
	// A busy timeout short enough to keep the test quick; the open's own ceiling
	// is what is under test, not how long a wait lasts.
	busyTimeoutMS = 200
	defer func() { busyTimeoutMS = 5000 }()

	path := filepath.Join(t.TempDir(), "blocked.db")
	pool, err := OpenRaw(path)
	if err != nil {
		t.Fatalf("open the pool: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(`CREATE TABLE ticks (id INTEGER PRIMARY KEY, at INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Saturate the pool: every connection it may open is checked out and held.
	held := make([]*sql.Conn, 0, maxOpenConns)
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for range maxOpenConns {
		conn, err := pool.Conn(context.Background())
		if err != nil {
			t.Fatalf("check out connection %d: %v", len(held), err)
		}
		held = append(held, conn)
	}
	before := runtime.NumGoroutine()

	// One more caller, on a context with a deadline. The pool cannot give it a
	// connection, so the deadline is what ends the wait -- and the answer is an
	// error rather than a connection nobody counted.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	conn, err := pool.Conn(ctx)
	if err == nil {
		_ = conn.Close()
		t.Fatal("a caller past the pool's ceiling was given a connection")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the blocked open failed with %v, want the context's deadline", err)
	}

	if after := runtime.NumGoroutine(); after > before+8 {
		t.Errorf("goroutines went from %d to %d over one blocked open, want no pile-up", before, after)
	}
	if got := pool.Stats().OpenConnections; got > maxOpenConns {
		t.Errorf("the pool opened %d connections while a caller was blocked, want at most %d", got, maxOpenConns)
	}
}

// waitFor polls cond until it holds or the test runs out of patience, so a
// scheduling delay is not reported as the failure it is not.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// note keeps the highest count any concurrent caller saw, which is the number
// that says whether the ceiling held under contention.
func note(high *atomic.Int64, n int64) {
	for {
		old := high.Load()
		if n <= old || high.CompareAndSwap(old, n) {
			return
		}
	}
}

// TestEveryPooledConnectionAppliesThePageCacheBound pins that the cache bound is
// set per connection, not just on the first one the pool opens: two connections
// held at once must both read it back.
func TestEveryPooledConnectionAppliesThePageCacheBound(t *testing.T) {
	pool, err := OpenRaw(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("open the pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	for i := range 3 {
		conn, err := pool.Conn(context.Background())
		if err != nil {
			t.Fatalf("check out connection %d: %v", i, err)
		}
		defer func() { _ = conn.Close() }()
		var got int
		if err := conn.QueryRowContext(context.Background(), `PRAGMA cache_size`).Scan(&got); err != nil {
			t.Fatalf("read cache_size on connection %d: %v", i, err)
		}
		if want := -pageCacheKiB; got != want {
			t.Errorf("connection %d cache_size = %d, want %d", i, got, want)
		}
	}
}

// TestAnIdlePoolKeepsOnlyItsIdleBound pins that a burst past maxIdleConns is
// closed back down on release instead of being kept for the process's life.
func TestAnIdlePoolKeepsOnlyItsIdleBound(t *testing.T) {
	pool, err := OpenRaw(filepath.Join(t.TempDir(), "idle.db"))
	if err != nil {
		t.Fatalf("open the pool: %v", err)
	}
	defer func() { _ = pool.Close() }()

	held := make([]*sql.Conn, 0, maxIdleConns*2)
	for range maxIdleConns * 2 {
		conn, err := pool.Conn(context.Background())
		if err != nil {
			t.Fatalf("check out: %v", err)
		}
		held = append(held, conn)
	}
	for _, c := range held {
		_ = c.Close()
	}
	st := pool.Stats()
	if st.Idle > maxIdleConns {
		t.Errorf("idle connections = %d, want at most %d", st.Idle, maxIdleConns)
	}
	if st.MaxIdleClosed == 0 {
		t.Error("no connection was closed for exceeding the idle bound")
	}
}
