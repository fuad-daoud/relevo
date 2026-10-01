//go:build unix

package owner

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// waitForSlot blocks until the server holds want pin slots, so a test acts only
// once the request it sent has really taken one.
func waitForSlot(t *testing.T, srv *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(srv.sem) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("len(srv.sem) never reached %d", want)
}

// TestCleanupReturnsTheSlotWhileAStatementRuns pins that a statement the engine
// will not interrupt cannot wedge a pin slot: the client sends an UPDATE that
// blocks behind another connection's BEGIN IMMEDIATE, then disconnects. cleanup
// must return the slot while the statement still steps, or one such query leaks
// one of the owner's slots for good.
func TestCleanupReturnsTheSlotWhileAStatementRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "o.db")
	// A long busy_timeout keeps the UPDATE blocked on the write lock for the
	// whole test, so cleanup cannot wait for it to finish.
	sqlDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	ctx := context.Background()
	if _, err := sqlDB.ExecContext(ctx, `CREATE TABLE probe (n INTEGER)`); err != nil {
		t.Fatalf("create probe: %v", err)
	}
	blocker, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("blocker Conn: %v", err)
	}
	t.Cleanup(func() {
		_, _ = blocker.ExecContext(ctx, "ROLLBACK")
		_ = blocker.Close()
	})
	if _, err := blocker.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("BEGIN IMMEDIATE: %v", err)
	}
	if _, err := blocker.ExecContext(ctx, `INSERT INTO probe (n) VALUES (1)`); err != nil {
		t.Fatalf("insert under the lock: %v", err)
	}

	l, sock := shortListener(t)
	srv := New(sqlDB, 3, 9, "01ORIGIN", nil)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	w, nc := dialRaw(t, sock)
	sendHello(t, w, wire.Version)
	welcome(t, w)

	// The UPDATE needs the write lock the blocker holds, so it steps until the
	// busy timeout and pins the connection the whole time.
	execRaw(t, w, 1, `UPDATE probe SET n = 2`)
	waitForSlot(t, srv, 1)

	_ = nc.Close()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(srv.sem) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the pin slot was not returned within 8s (len(sem)=%d)", len(srv.sem))
}
