package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var errSimulatedBusy = errors.New("turso: database is busy: database is locked")

// simulateEngineBug leaves conn inside a transaction and reports busy, the
// state the engine leaves behind when a busy BEGIN IMMEDIATE races a commit.
func simulateEngineBug(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return err
	}
	return errSimulatedBusy
}

func realBegin(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE")
	return err
}

func setBeginHook(t *testing.T, h func(context.Context, *sql.Conn) error) {
	t.Helper()
	beginHook = h
	t.Cleanup(func() { beginHook = nil })
}

func openBusyTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// requirePooledConnBegins borrows a conn from the pool and starts a clean
// BEGIN IMMEDIATE on it.
func requirePooledConnBegins(t *testing.T, d *DB) {
	t.Helper()
	conn, err := d.sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("BEGIN IMMEDIATE on a pooled conn = %v, want nil", err)
	}
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatalf("ROLLBACK: %v", err)
	}
}

func TestTxBusyBeginLeavesTheConnectionUsable(t *testing.T) {
	d := openBusyTestDB(t)
	calls := 0
	setBeginHook(t, func(ctx context.Context, conn *sql.Conn) error {
		calls++
		if calls == 1 {
			return simulateEngineBug(ctx, conn)
		}
		return realBegin(ctx, conn)
	})

	runs := 0
	err := d.Tx(func(*Tx) error {
		runs++
		return nil
	})
	if err != nil {
		t.Fatalf("Tx = %v, want nil after the retried BEGIN", err)
	}
	if runs != 1 {
		t.Errorf("fn ran %d times, want exactly 1", runs)
	}
	if calls != 2 {
		t.Errorf("BEGIN attempted %d times, want 2", calls)
	}
	setBeginHook(t, nil)
	requirePooledConnBegins(t, d)
}

func TestTxGivesUpOnABusyBeginLeavesTheConnectionUsable(t *testing.T) {
	d := openBusyTestDB(t)
	d.beginRetry = 100 * time.Millisecond
	setBeginHook(t, simulateEngineBugEveryTime)

	ran := false
	err := d.Tx(func(*Tx) error {
		ran = true
		return nil
	})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Tx = %v, want errors.Is(..., ErrBusy)", err)
	}
	if ran {
		t.Error("fn ran, but no BEGIN succeeded")
	}
	setBeginHook(t, nil)
	requirePooledConnBegins(t, d)
}

func simulateEngineBugEveryTime(ctx context.Context, conn *sql.Conn) error {
	return simulateEngineBug(ctx, conn)
}

func TestMigrationBusyBeginLeavesTheConnectionUsable(t *testing.T) {
	d := openBusyTestDB(t)
	setBeginHook(t, simulateEngineBugEveryTime)

	fsys := fstest.MapFS{"migrations/9999_x.sql": {Data: []byte("SELECT 1;")}}
	err := applyOneMigration(d.sqlDB, fsys, "9999_x.sql", 9999)
	if err == nil || !strings.Contains(err.Error(), "begin migration") {
		t.Fatalf("applyOneMigration = %v, want a begin migration error", err)
	}
	setBeginHook(t, nil)
	requirePooledConnBegins(t, d)
}

func TestRepairInvalidTextBusyBeginLeavesTheConnectionUsable(t *testing.T) {
	d := openBusyTestDB(t)
	conn, err := d.sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	setBeginHook(t, simulateEngineBugEveryTime)

	_, err = repairInvalidText(conn)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("repairInvalidText = %v, want errors.Is(..., ErrBusy)", err)
	}
	_ = conn.Close()
	setBeginHook(t, nil)
	requirePooledConnBegins(t, d)
}
