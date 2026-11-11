//go:build unix

package owner

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// recoverStale swaps the pool serving local for a fresh one from the reopen
// hook, reporting the fresh pool. Only a stale-WAL failure swaps: the caller
// passes the statement's own error, and anything else answers false with the
// pool untouched. False also answers a missing hook and a hook failure, so
// the statement fails with its own error instead of a recovery one; a failed
// probe leaves a best-effort pool behind and a later statement retries
// the swap. The hook closes the pool it replaces only when nothing holds a
// connection out of it, so it never strands the handle on a closed pool.
func (s *Server) recoverStale(local bool, cause error) (*sql.DB, bool) {
	if !wire.IsShortReadWAL(cause) {
		return nil, false
	}
	s.recoverMu.Lock()
	defer s.recoverMu.Unlock()
	s.mu.Lock()
	hook := s.reopen
	s.mu.Unlock()
	if hook == nil {
		return nil, false
	}
	fresh, err := hook(local)
	if err != nil {
		return nil, false
	}
	s.mu.Lock()
	if local && s.localDB != nil {
		s.localDB = fresh
	} else {
		s.dbh = fresh
	}
	s.mu.Unlock()
	return fresh, true
}

// dropPinned discards the connection pin took and frees its slot, so a stale
// pool swap neither returns a poisoned connection to a pool nor leaks the
// slot. A retry takes a fresh slot for its replacement below; every other
// path answers with the statement's own error, and the deferred release finds
// nothing left to return.
func (c *conn) dropPinned() {
	pinned := c.takePinned()
	if pinned == nil {
		return
	}
	// The old connection read the stale WAL; mark it so the pool drops it
	// instead of handing it to the next statement.
	_ = pinned.Raw(func(any) error { return driver.ErrBadConn })
	_ = pinned.Close()
	<-c.s.sem
}

// takeSlot waits on one connection slot the way pin does, for the retry's
// replacement connection. It reports whether the slot arrived before the
// request or the slot bound gave out.
func (c *conn) takeSlot(ctx context.Context) bool {
	select {
	case c.s.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	case <-time.After(slotWait):
		return false
	}
}

// retryStale moves this connection onto a recovered pool and reports the fresh
// connection for one retry of the read that failed on the stale one. It
// answers false when the error is not a stale WAL, when the statement controls
// a transaction or runs inside one, and when no fresh pool arrives: the caller
// then answers with the statement's own error. Only a read lands here, never
// a write: a retried write could apply twice, while a read outside a
// transaction answers the same on either pool.
//
// The pinned connection is dropped before the hook runs: a stale pool keeps
// poisoning pools opened beside it while one of its connections is out, so
// the probe the hook runs would fail on the connection this very retry holds.
func (c *conn) retryStale(ctx context.Context, query string, err error) (*sql.Conn, bool) {
	if c.transactionOpen() {
		return nil, false
	}
	if open, closeTx := txEffect(query); open || closeTx {
		return nil, false
	}
	// Other errors leave the connection alone, like exec does: only a stale
	// pool swap discards a pinned connection, and only a short read swaps.
	if !wire.IsShortReadWAL(err) {
		return nil, false
	}
	c.dropPinned()
	freshPool, ok := c.s.recoverStale(c.local, err)
	if !ok {
		return nil, false
	}
	fresh, ferr := freshPool.Conn(ctx)
	if ferr != nil {
		return nil, false
	}
	if !c.takeSlot(ctx) {
		_ = fresh.Close()
		return nil, false
	}
	c.mu.Lock()
	c.pinned = fresh
	c.mu.Unlock()
	return fresh, true
}
