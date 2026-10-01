//go:build unix

package db

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
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
