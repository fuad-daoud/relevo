//go:build unix

package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
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
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := OpenWith(path, Options{BusyTimeout: 2 * time.Minute})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	execOn(t, d, `CREATE TABLE t (n INTEGER)`)

	// Hold the write lock on a dedicated conn so the owner's INSERT blocks
	// in the engine for as long as the test wants.
	holder, err := d.sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("holder Conn: %v", err)
	}
	defer func() { _ = holder.Close() }()
	if _, err := holder.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("holder BEGIN IMMEDIATE: %v", err)
	}

	sock := startOwner(t, d)
	d2 := dialDB(t, sock)
	baseline := ownerConns(t, sock)

	conn, err := d2.sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	execErr := make(chan error, 1)
	go func() {
		_, err := conn.ExecContext(ctx, `INSERT INTO t (n) VALUES (1)`)
		execErr <- err
	}()

	// Wait until the owner has pinned a connection for the request.
	pollUntil(t, func() bool {
		return d.sqlDB.Stats().InUse > 1
	}, "the owner never pinned a connection for the insert")

	cancel()

	err = <-execErr
	if err == nil {
		t.Fatal("the cancelled insert returned success")
	}
	if errors.Is(err, driver.ErrBadConn) {
		t.Errorf("cancelled insert error %v matches driver.ErrBadConn, so the statement would be retried", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled insert error = %v, want context.Canceled", err)
	}

	// Release the write lock and close the dedicated conn.
	_, _ = holder.ExecContext(context.Background(), "ROLLBACK")
	_ = holder.Close()

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
	pollUntil(t, func() bool {
		return ownerConns(t, sock) == want
	}, fmt.Sprintf("owner live connections did not fall back to %d", want))
}
