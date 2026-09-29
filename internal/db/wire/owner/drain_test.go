//go:build unix

package owner

import (
	"context"
	"database/sql"
	"net"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// readKind reads one frame and requires its kind.
func readKind(t *testing.T, w *wire.Conn, nc net.Conn, want byte) {
	t.Helper()
	if err := nc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	frame, err := w.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if kind, err := wire.Kind(frame); err != nil || kind != want {
		t.Fatalf("frame kind = %d (%v), want %d", kind, err, want)
	}
}

// TestServeRefusesANewTransactionWhileDraining pins the drain contract: a
// request that arrives while draining on a connection with no open transaction
// is refused restarting, while a connection already inside one is let through
// so its transaction can commit.
func TestServeRefusesANewTransactionWhileDraining(t *testing.T) {
	srv, sock := startServer(t)

	a, na := dialRaw(t, sock)
	sendHello(t, a, wire.Version)
	welcome(t, a)
	execRaw(t, a, 1, "BEGIN")
	readDone(t, a, na)

	// b is handshaken before the drain, so its request meets the drain rather
	// than sitting unaccepted in the backlog.
	b, nb := dialRaw(t, sock)
	sendHello(t, b, wire.Version)
	welcome(t, b)

	done := make(chan error, 1)
	go func() { done <- srv.Drain(context.Background()) }()

	execRaw(t, b, 1, `CREATE TABLE t (n INTEGER)`)
	if err := nb.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	got := refusal(t, b)
	if got.Code != wire.RefuseRestarting {
		t.Errorf("refusal code = %q, want %q", got.Code, wire.RefuseRestarting)
	}

	// The connection inside the transaction still commits.
	execRaw(t, a, 2, "COMMIT")
	readDone(t, a, na)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Drain did not return after the transaction committed")
	}
}

// TestDrainWaitsForAnOpenTransactionToCommit pins that Drain does not drop the
// clients while a transaction is still open: the transaction must be able to
// commit first.
func TestDrainWaitsForAnOpenTransactionToCommit(t *testing.T) {
	srv, sock := startServer(t)

	a, na := dialRaw(t, sock)
	sendHello(t, a, wire.Version)
	welcome(t, a)
	execRaw(t, a, 1, "BEGIN")
	readDone(t, a, na)

	done := make(chan error, 1)
	go func() { done <- srv.Drain(context.Background()) }()

	select {
	case <-done:
		t.Fatal("Drain returned while a transaction was still open")
	case <-time.After(200 * time.Millisecond):
	}

	execRaw(t, a, 2, "COMMIT")
	readDone(t, a, na)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Drain did not return after the transaction committed")
	}
}

// startFKServer is startServer with foreign-key enforcement on, so a deferred
// constraint can be made to fail a COMMIT deterministically.
func startFKServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "o.db")
	sqlDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	l, sock := shortListener(t)
	srv := New(sqlDB, 3, 9, "01ORIGIN")
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = l.Close()
	})
	return srv, sock
}

// TestAFailedCommitKeepsTheConnectionInATransaction pins the membership rule: a
// COMMIT that fails leaves the connection open, because SQLite keeps that
// transaction alive; only a COMMIT that succeeds or a ROLLBACK clears it.
func TestAFailedCommitKeepsTheConnectionInATransaction(t *testing.T) {
	srv, sock := startFKServer(t)

	a, na := dialRaw(t, sock)
	sendHello(t, a, wire.Version)
	welcome(t, a)
	c := liveConn(t, srv)

	execRaw(t, a, 1, `CREATE TABLE parent (id INTEGER PRIMARY KEY)`)
	readDone(t, a, na)
	execRaw(t, a, 2, `CREATE TABLE child (id INTEGER PRIMARY KEY, pid INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED)`)
	readDone(t, a, na)

	execRaw(t, a, 3, "BEGIN")
	readDone(t, a, na)
	if !c.transactionOpen() {
		t.Fatal("BEGIN did not mark the connection in a transaction")
	}

	// The deferred foreign key is checked at COMMIT, so the statement succeeds
	// and the COMMIT fails.
	execRaw(t, a, 4, `INSERT INTO child (id, pid) VALUES (1, 999)`)
	readDone(t, a, na)
	execRaw(t, a, 5, "COMMIT")
	readKind(t, a, na, wire.KindError)
	if !c.transactionOpen() {
		t.Error("a failed COMMIT cleared the connection's transaction")
	}

	execRaw(t, a, 6, "ROLLBACK")
	readDone(t, a, na)
	if c.transactionOpen() {
		t.Error("ROLLBACK did not clear the connection's transaction")
	}
}
