//go:build unix

package owner

import (
	"context"
	"database/sql"
	"time"
)

// pin returns the database connection this request runs on: the one already
// held inside a transaction, else a fresh pool connection holding one of the
// server's slots. The slot is taken on the request's context with a bounded
// wait, so a client over the cap is refused as busy past the bound instead of
// hanging -- the handshake never waits on the cap, and a disconnect releases
// the wait.
func (c *conn) pin(ctx context.Context) (*sql.Conn, error) {
	c.mu.Lock()
	if c.pinned != nil {
		pinned := c.pinned
		c.mu.Unlock()
		return pinned, nil
	}
	c.mu.Unlock()

	select {
	case c.s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(slotWait):
		return nil, &slotBusyError{held: len(c.s.sem), max: maxConns}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	pinned, err := c.s.poolFor(c.local).Conn(ctx)
	if err != nil {
		<-c.s.sem
		return nil, err
	}
	c.pinned = pinned
	return pinned, nil
}

// release returns the pinned connection to the pool when no transaction is
// open, freeing the slot pin took. Inside a transaction the connection is
// kept: the transaction's later statements must run on it. A nil take means
// cleanup already discarded it, so the slot is freed exactly once between the
// two.
func (c *conn) release() {
	if c.transactionOpen() || c.holdsSession() {
		return
	}
	pinned := c.takePinned()
	if pinned == nil {
		return
	}
	// Outside a transaction there is nothing to roll back; the pool reuses
	// the connection, so the discard's bad-conn mark stays cleanup's.
	_, _ = pinned.ExecContext(context.Background(), "ROLLBACK")
	_ = pinned.Close()
	<-c.s.sem
}

func (c *conn) holdsSession() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

// takePinned detaches the client's pinned connection, returning nil when there
// is none. A non-nil result owns the connection slot the pin took.
func (c *conn) takePinned() *sql.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	pinned := c.pinned
	c.pinned = nil
	return pinned
}
