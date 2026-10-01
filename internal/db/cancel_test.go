//go:build unix

package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// TestCancelledExecIsNotRetried pins that a call cancelled on the wire reports
// the caller's own context error, never driver.ErrBadConn: database/sql retries
// a pool call that saw ErrBadConn, and an INSERT that already ran would then be
// applied twice. A dedicated connection is used so the driver's own error
// reaches the test instead of being folded into that retry.
func TestCancelledExecIsNotRetried(t *testing.T) {
	d := openTestDB(t)
	execOn(t, d, `CREATE TABLE t (n INTEGER)`)
	sock := startOwner(t, d)
	d2 := dialDB(t, sock)
	baseline := ownerConns(t, sock)

	conn, err := d2.sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}

	// The insert outlives the cancel: the engine finishes a statement it has
	// already started, so only the client's short grace ends before it does.
	const insert = `INSERT INTO t (n) WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 1000000) SELECT x FROM c WHERE x = 1`
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err = conn.ExecContext(ctx, insert)
	if err == nil {
		t.Fatal("the cancelled insert returned success")
	}
	if errors.Is(err, driver.ErrBadConn) {
		t.Errorf("cancelled insert error %v matches driver.ErrBadConn, so the statement would be retried", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled insert error = %v, want context.Canceled", err)
	}

	// Dropping the connection lets the owner finish and discard it, so the row
	// count below sees the statement's final effect.
	if err := conn.Close(); err != nil {
		t.Fatalf("conn close: %v", err)
	}
	waitOwnerConns(t, sock, baseline)

	var n int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n > 1 {
		t.Errorf("the cancelled insert left %d rows, want at most 1", n)
	}
}

// ownerConns reads the owner's live-connection count through a throwaway
// handshake. The count includes the probe itself, so two samples compare only
// when taken the same way.
func ownerConns(t *testing.T, sock string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	info, err := client.Info(ctx, sock)
	if err != nil {
		t.Fatalf("client.Info: %v", err)
	}
	return info.Conns
}

// waitOwnerConns blocks until the owner's live count falls back to want, so a
// caller can observe that a finished statement's connection was discarded.
func waitOwnerConns(t *testing.T, sock string, want int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		n := ownerConns(t, sock)
		if n == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("owner live connections = %d, want %d", n, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
