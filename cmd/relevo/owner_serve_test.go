//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
)

// TestDaemonRefusesATooLongSocketPath pins that a winning start whose state
// root cannot hold the socket refuses before it mints or migrates anything: the
// line names the path and the limit, and no relevo.db is left behind.
func TestDaemonRefusesATooLongSocketPath(t *testing.T) {
	base := t.TempDir()
	long := filepath.Join(base, strings.Repeat("x", 130))
	t.Setenv("HOME", base)
	t.Setenv("XDG_STATE_HOME", long)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))

	err := cmdDaemon(nil)
	if err == nil {
		t.Fatal("cmdDaemon with an over-long socket path: err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "sun_path") {
		t.Errorf("error %v must name the sun_path limit", err)
	}
	if !strings.Contains(err.Error(), "relevo.sock") {
		t.Errorf("error %v must name the socket path", err)
	}

	dbPath := filepath.Join(long, "relevo", "relevo.db")
	if _, serr := os.Stat(dbPath); !os.IsNotExist(serr) {
		t.Errorf("relevo.db exists after a refused path: stat error = %v, want not-exist", serr)
	}
}

// TestOwnerServesANewerSchema pins the newer-schema half of the serve contract:
// the daemon keeps a handle for the owner even when rt.DB is nil to pause its
// own ingest, so a dialled client still reaches the database.
func TestOwnerServesANewerSchema(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dbPath := filepath.Join(root, "relevo.db")

	raw := dbtest.RawOpen(t, dbPath)
	if _, err := raw.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (99, '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	_ = raw.Close()

	d, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if !d.Newer() {
		t.Fatal("a database at schema 99 is not reported newer")
	}

	if err := checkSocketPath(root); err != nil {
		t.Fatalf("checkSocketPath: %v", err)
	}
	ln, err := openOwnerListener(root)
	if err != nil {
		t.Fatalf("openOwnerListener: %v", err)
	}
	srv, err := serveOwner(d, ln)
	if err != nil {
		t.Fatalf("serveOwner: %v", err)
	}
	if srv == nil {
		t.Fatal("a newer-schema handle got no owner")
	}
	t.Cleanup(func() { _ = srv.Close() })

	status, err := db.ProbeOwner(filepath.Join(root, "relevo.sock"))
	if err != nil {
		t.Fatalf("ProbeOwner: %v", err)
	}
	if status.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", status.PID, os.Getpid())
	}
}
