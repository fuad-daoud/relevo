//go:build unix

package owner

import (
	"context"
	"database/sql"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ackGate is a net.Conn whose Write reports that it was entered and then blocks
// until released. A test arms it to hold a Done frame mid-write, so it can read
// the connection's transaction state while the ack is still in flight.
type ackGate struct {
	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func newAckGate() *ackGate {
	return &ackGate{entered: make(chan struct{}, 1), release: make(chan struct{})}
}

// arm makes the next Write block. Release afterwards unblocks it and every later
// Write, so the connection's own teardown cannot stall.
func (g *ackGate) arm() {
	g.mu.Lock()
	g.armed = true
	g.mu.Unlock()
}

func (g *ackGate) Write(p []byte) (int, error) {
	g.mu.Lock()
	armed := g.armed
	g.mu.Unlock()
	if !armed {
		return len(p), nil
	}
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
	return len(p), nil
}

func (g *ackGate) Read([]byte) (int, error)         { return 0, io.EOF }
func (g *ackGate) Close() error                     { return nil }
func (g *ackGate) LocalAddr() net.Addr              { return ackAddr{} }
func (g *ackGate) RemoteAddr() net.Addr             { return ackAddr{} }
func (g *ackGate) SetDeadline(time.Time) error      { return nil }
func (g *ackGate) SetReadDeadline(time.Time) error  { return nil }
func (g *ackGate) SetWriteDeadline(time.Time) error { return nil }

type ackAddr struct{}

func (ackAddr) Network() string { return "test" }
func (ackAddr) String() string  { return "test" }

// newAckConn builds a real owner conn over the gate with one pinned database
// connection, the same two calls the serving path uses.
func newAckConn(t *testing.T) (*conn, *sql.Conn, *ackGate) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "o.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	gate := newAckGate()
	c := newConn(New(db, 0, 0, "test", nil), gate)
	pinned, err := c.pin(context.Background())
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	t.Cleanup(func() { _ = pinned.Close() })
	return c, pinned, gate
}

// TestACommitStaysInTransactionUntilItsAckIsWritten pins the drain contract for
// a committed statement: the connection counts as in its transaction until the
// COMMIT's Done frame is written, so a drain that starts while the ack is in
// flight waits for it instead of cutting the answer off.
func TestACommitStaysInTransactionUntilItsAckIsWritten(t *testing.T) {
	c, pinned, gate := newAckConn(t)
	ctx := context.Background()

	begin, _ := newRequest(1)
	c.exec(ctx, begin, pinned, "BEGIN", nil)
	if !c.transactionOpen() {
		t.Fatal("BEGIN did not open a transaction")
	}

	gate.arm()
	commit, _ := newRequest(2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.exec(ctx, commit, pinned, "COMMIT", nil)
	}()

	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the COMMIT ack was never written")
	}
	// The membership must still be set here: the ack is not on the wire yet, so
	// dropping the connection now would lose the committed answer.
	if !c.transactionOpen() {
		t.Error("a COMMIT left the connection out of its transaction before its ack was written")
	}
	close(gate.release)
	<-done
	if c.transactionOpen() {
		t.Error("a COMMIT left the connection in its transaction after its ack was written")
	}
}

// TestABeginIsInTransactionBeforeItsAckIsWritten guards the other half of the
// bracket: a BEGIN counts as in a transaction from before its ack, so a drop
// cannot land between the statement and the frame that acknowledges it. It
// passes on the earlier order too, and is labelled as a regression guard.
func TestABeginIsInTransactionBeforeItsAckIsWritten(t *testing.T) {
	c, pinned, gate := newAckConn(t)
	ctx := context.Background()

	gate.arm()
	begin, _ := newRequest(1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.exec(ctx, begin, pinned, "BEGIN", nil)
	}()

	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the BEGIN ack was never written")
	}
	if !c.transactionOpen() {
		t.Error("a BEGIN was not in its transaction before its ack was written")
	}
	close(gate.release)
	<-done
	if !c.transactionOpen() {
		t.Error("a BEGIN left the connection out of its transaction after its ack was written")
	}
}
