//go:build unix

package owner

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// staleWALErr is the rebuilt short-read failure the owner answers with: the
// wire error carries the engine's text and code, so the classifier meets it
// the way it meets the driver's own error.
func staleWALErr() *wire.Error {
	return wire.NewError(1, 10, 10, "I/O error: short read on WAL frame at offset 32: expected 4096 bytes, got 0")
}

func openTestPool(t *testing.T, name string) *sql.DB {
	t.Helper()
	pool, err := sql.Open("sqlite", "file:"+name+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if _, err := pool.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return pool
}

// testConn builds a connection against srv the way handle does, without a
// listener: the pair under it is closed by the test.
func testConn(t *testing.T, srv *Server) *conn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	t.Cleanup(func() { _ = server.Close() })
	return newConn(srv, server)
}

// TestRecoverStaleSwapsThePool pins the swap: a stale-WAL failure replaces the
// serving pool with the hook's fresh one, and only then.
func TestRecoverStaleSwapsThePool(t *testing.T) {
	dir := t.TempDir()
	old := openTestPool(t, dir+"/old.db")
	fresh := openTestPool(t, dir+"/fresh.db")
	var calls int
	srv := New(old, 3, 9, "01ORIGIN", nil)
	srv.SetReopen(func(local bool) (*sql.DB, error) {
		calls++
		if local {
			t.Error("hook asked for the local file by a shared-file recovery")
		}
		return fresh, nil
	})

	if got, ok := srv.recoverStale(false, staleWALErr()); !ok || got != fresh {
		t.Fatalf("recoverStale = %v, %v, want the fresh pool, true", got, ok)
	}
	if srv.poolFor(false) != fresh {
		t.Error("the server still serves the poisoned pool after the swap")
	}
	if calls != 1 {
		t.Errorf("hook ran %d times, want exactly once", calls)
	}
	// A refusal is not a stale WAL: the pool stays put and the hook sleeps.
	if _, ok := srv.recoverStale(false, &wire.Refusal{Code: wire.RefuseBusy}); ok {
		t.Error("recoverStale swapped on a refusal")
	}
	if calls != 1 {
		t.Errorf("hook ran %d times, want exactly once", calls)
	}
}

// TestRecoverStaleKeepsThePoolWithoutAFreshOne pins the failure half: a hook
// error leaves the old pool serving, so the statement fails with its own error
// instead of stranding later statements on a closed pool.
func TestRecoverStaleKeepsThePoolWithoutAFreshOne(t *testing.T) {
	dir := t.TempDir()
	old := openTestPool(t, dir+"/old.db")
	srv := New(old, 3, 9, "01ORIGIN", nil)
	srv.SetReopen(func(bool) (*sql.DB, error) { return nil, errors.New("reopen failed") })

	if _, ok := srv.recoverStale(false, staleWALErr()); ok {
		t.Fatal("recoverStale swapped on a hook failure")
	}
	if srv.poolFor(false) != old {
		t.Error("the server dropped the old pool on a hook failure")
	}
}

// TestRecoverStaleServesTheLocalFile pins the scope: a local failure swaps
// the local pool and asks the hook for it, never the shared one.
func TestRecoverStaleServesTheLocalFile(t *testing.T) {
	dir := t.TempDir()
	shared := openTestPool(t, dir+"/shared.db")
	local := openTestPool(t, dir+"/local.db")
	freshLocal := openTestPool(t, dir+"/fresh-local.db")
	var sawLocal bool
	srv := New(shared, 3, 9, "01ORIGIN", nil)
	srv.ServeLocal(local)
	srv.SetReopen(func(l bool) (*sql.DB, error) {
		sawLocal = l
		return freshLocal, nil
	})

	if _, ok := srv.recoverStale(true, staleWALErr()); !ok {
		t.Fatal("recoverStale refused a local stale WAL")
	}
	if !sawLocal {
		t.Error("the hook was not asked for the local file")
	}
	if srv.poolFor(true) != freshLocal {
		t.Error("the server still serves the stale local pool after the swap")
	}
	if srv.poolFor(false) != shared {
		t.Error("a local recovery moved the shared pool")
	}
}

// TestRetryStaleSwapsTheConnection pins the read retry: after a stale-WAL
// failure outside any transaction, the connection moves onto a fresh-pool
// connection the caller retries on.
func TestRetryStaleSwapsTheConnection(t *testing.T) {
	dir := t.TempDir()
	old := openTestPool(t, dir+"/old.db")
	fresh := openTestPool(t, dir+"/fresh.db")
	var calls int
	srv := New(old, 3, 9, "01ORIGIN", nil)
	srv.SetReopen(func(bool) (*sql.DB, error) {
		calls++
		return fresh, nil
	})
	c := testConn(t, srv)

	// retryStale runs where pin ran: with the slot already held.
	c.s.sem <- struct{}{}
	pinned, err := old.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	c.mu.Lock()
	c.pinned = pinned
	c.mu.Unlock()

	got, ok := c.retryStale(context.Background(), "SELECT 1", staleWALErr())
	if !ok || got == nil {
		t.Fatalf("retryStale = %v, %v, want a fresh connection, true", got, ok)
	}
	c.mu.Lock()
	cur := c.pinned
	c.mu.Unlock()
	if cur != got {
		t.Error("the connection did not move onto the fresh pool")
	}
	if calls != 1 {
		t.Errorf("hook ran %d times, want exactly once", calls)
	}
	// Release the retry the way the statement's release would: the slot the
	// test took for pin must come back exactly once.
	_ = got.Close()
	<-c.s.sem
}

// TestRetryStaleKeepsWritesAndTransactions pins what a retry never touches: a
// statement inside a transaction, transaction control, and any other error
// answer without swapping and without calling the hook.
func TestRetryStaleKeepsWritesAndTransactions(t *testing.T) {
	dir := t.TempDir()
	old := openTestPool(t, dir+"/old.db")
	fresh := openTestPool(t, dir+"/fresh.db")
	var calls int
	srv := New(old, 3, 9, "01ORIGIN", nil)
	// Every guard above answers before the hook, so the single call below is
	// the only one: more would mean a guard reached through.
	srv.SetReopen(func(bool) (*sql.DB, error) {
		calls++
		return fresh, nil
	})

	pinned, err := old.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer func() { _ = pinned.Close() }()

	c := testConn(t, srv)
	// The slot pin took: the guards below answer before dropPinned, so it
	// stays held through them.
	c.s.sem <- struct{}{}
	c.mu.Lock()
	c.pinned = pinned
	c.mu.Unlock()

	c.mu.Lock()
	c.inTx = true
	c.mu.Unlock()
	if _, ok := c.retryStale(context.Background(), "SELECT 1", staleWALErr()); ok {
		t.Error("retryStale retried inside a transaction")
	}
	c.mu.Lock()
	c.inTx = false
	c.mu.Unlock()
	for _, q := range []string{"BEGIN IMMEDIATE", "COMMIT", "ROLLBACK"} {
		if _, ok := c.retryStale(context.Background(), q, staleWALErr()); ok {
			t.Errorf("retryStale retried transaction control %q", q)
		}
	}
	if _, ok := c.retryStale(context.Background(), "SELECT 1", wire.NewError(1, 5, 5, "busy")); ok {
		t.Error("retryStale retried a non-stale error")
	}
	if _, ok := c.retryStale(context.Background(), "SELECT 1", staleWALErr()); !ok {
		t.Fatal("retryStale refused a plain stale read after the refusals")
	}
	if calls != 1 {
		t.Errorf("hook ran %d times, want exactly once", calls)
	}
	// The last call swapped the connection and its slot: the pin slot went
	// back in dropPinned and the retry took a fresh one. Hand both back the
	// way the statement's release would.
	c.mu.Lock()
	cur := c.pinned
	c.pinned = nil
	c.mu.Unlock()
	if cur == nil {
		t.Fatal("no fresh connection pinned after the retry")
	}
	_ = cur.Close()
	<-c.s.sem
}

// TestRetryStaleFailsClosed pins the fallbacks: a fresh pool that will not
// connect and a request that will not wait both answer without retrying, and
// the slot pin took comes back either way.
func TestRetryStaleFailsClosed(t *testing.T) {
	dir := t.TempDir()
	old := openTestPool(t, dir+"/old.db")
	good := openTestPool(t, dir+"/fresh.db")
	srv := New(old, 3, 9, "01ORIGIN", nil)
	closed, err := sql.Open("sqlite", "file:"+dir+"/closed.db")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	srv.SetReopen(func(bool) (*sql.DB, error) { return closed, nil })
	c := testConn(t, srv)

	pinned, err := old.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer func() { _ = pinned.Close() }()
	// The slot pin took: the failed retry below drops it back in dropPinned.
	c.s.sem <- struct{}{}
	c.mu.Lock()
	c.pinned = pinned
	c.mu.Unlock()

	// The hook's pool is closed: its connections fail, so no retry.
	if _, ok := c.retryStale(context.Background(), "SELECT 1", staleWALErr()); ok {
		t.Error("retryStale retried on a pool that will not connect")
	}
	c.mu.Lock()
	if c.pinned != nil {
		t.Error("a failed retry left a connection pinned")
	}
	c.mu.Unlock()

	// A cancelled request takes no slot for the replacement: fill every
	// slot and cancel, so takeSlot can only give up. Nothing is pinned, so
	// dropPinned is a no-op and the slot count stays whole.
	srv.SetReopen(func(bool) (*sql.DB, error) { return good, nil })
	for i := 0; i < cap(c.s.sem); i++ {
		c.s.sem <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(c.s.sem); i++ {
			<-c.s.sem
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := c.retryStale(ctx, "SELECT 1", staleWALErr()); ok {
		t.Error("retryStale retried for a cancelled request")
	}
}

// TestTakeSlotTimesOutWhenFull pins the last takeSlot branch: with every slot
// held and a live request, the wait gives up at the bound instead of blocking
// the recovery forever.
func TestTakeSlotTimesOutWhenFull(t *testing.T) {
	dir := t.TempDir()
	old := openTestPool(t, dir+"/old.db")
	srv := New(old, 3, 9, "01ORIGIN", nil)
	c := testConn(t, srv)
	SetSlotWait(50 * time.Millisecond)
	defer SetSlotWait(0)
	for i := 0; i < cap(c.s.sem); i++ {
		c.s.sem <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(c.s.sem); i++ {
			<-c.s.sem
		}
	}()
	if c.takeSlot(context.Background()) {
		t.Error("takeSlot took a slot from a full server")
	}
}
