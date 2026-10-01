//go:build unix

package db

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// queryTestOwnerSock binds a short unix socket under /tmp, so a test can serve
// a database on it without risking the sun_path limit.
func queryTestOwnerSock(t *testing.T) (net.Listener, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "q.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, sock
}

// TestQueryReadOnlyThroughTheOwnerRefusesAWrite pins that the seam behaves the
// same on a dialled handle: the statement reaches the owner's pinned
// connection, and query_only refuses the write there.
func TestQueryReadOnlyThroughTheOwnerRefusesAWrite(t *testing.T) {
	d := directOpenTestDB(t)
	if _, err := d.sqlDB.Exec(`CREATE TABLE probe (n INTEGER)`); err != nil {
		t.Fatalf("create probe: %v", err)
	}

	l, sock := queryTestOwnerSock(t)
	srv := NewOwner(d)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	dialled, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = dialled.Close() })

	err = dialled.QueryReadOnly(context.Background(), `WITH x AS (SELECT 1 AS n) INSERT INTO probe (n) SELECT n FROM x`, noQueryRows)
	if err == nil {
		t.Fatal("a CTE-wrapped INSERT through the owner was not refused")
	}

	var count int
	if qerr := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM probe`).Scan(&count); qerr != nil {
		t.Fatalf("count probe: %v", qerr)
	}
	if count != 0 {
		t.Errorf("probe holds %d rows after a refused insert, want 0", count)
	}
}

// TestZeroColumnResultThroughTheOwnerReturnsPromptly pins the containment of a
// statement whose result carries no columns. Turso executes
// `PRAGMA writable_schema=1` even behind query_only and answers with no columns
// at all; the wire client used to report an endless zero-width row, so the
// caller grew the heap until the process was killed. The statement must now come
// back at once, without moving the heap, and the read-only verb must refuse it
// before it reaches the owner in the first place.
func TestZeroColumnResultThroughTheOwnerReturnsPromptly(t *testing.T) {
	d := directOpenTestDB(t)
	l, sock := queryTestOwnerSock(t)
	srv := NewOwner(d)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	dialled, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = dialled.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	done := make(chan error, 1)
	go func() {
		rows, err := dialled.sqlDB.QueryContext(ctx, `PRAGMA writable_schema=1`)
		if err != nil {
			done <- err
			return
		}
		for rows.Next() {
		}
		err = rows.Err()
		_ = rows.Close()
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a zero-column result through the owner = %v, want a result", err)
		}
	case <-time.After(2 * time.Second):
		// The cancel makes database/sql's own row reader stop, so a client that
		// still spins does not eat the test process.
		cancel()
		t.Fatal("a zero-column result through the owner did not return within 2 s")
	}

	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > before.HeapAlloc {
		if growth := after.HeapAlloc - before.HeapAlloc; growth > 8<<20 {
			t.Errorf("the heap grew by %d bytes around the statement, want it not to grow", growth)
		}
	}

	if err := dialled.QueryReadOnly(context.Background(), `PRAGMA writable_schema=1`, noQueryRows); !errors.Is(err, ErrPragmaNotReadOnly) {
		t.Errorf("the read-only verb on a pragma assignment = %v, want ErrPragmaNotReadOnly", err)
	}
}
