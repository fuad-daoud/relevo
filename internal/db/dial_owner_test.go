//go:build unix

package db_test

import (
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

func shortSock(t *testing.T) (net.Listener, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "o.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, sock
}

// rawAheadDB is a database whose schema_version is above any embedded
// migration, so a direct open reports it newer.
func rawAheadDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ahead.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (99, '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	return raw, path
}

func TestDialOfANewerDatabaseReportsNewer(t *testing.T) {
	raw, path := rawAheadDB(t)
	direct, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	directHave, directKnow := direct.SchemaVersions()
	if !direct.Newer() {
		t.Fatal("a direct open of a newer database does not report Newer")
	}

	l, sock := shortSock(t)
	srv := owner.New(raw, directHave, directKnow, "01ORIGIN")
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = l.Close()
	})

	dialled, err := db.Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = dialled.Close() })

	if !dialled.Newer() {
		t.Error("a dialled handle of a newer database does not report Newer")
	}
	have, know := dialled.SchemaVersions()
	if have != directHave || know != directKnow {
		t.Errorf("dialled versions = %d/%d, want the direct open's %d/%d", have, know, directHave, directKnow)
	}
	if err := dialled.CheckMigrate(); err == nil || !errors.Is(err, db.ErrNewerSchema) {
		t.Errorf("CheckMigrate = %v, want ErrNewerSchema", err)
	}
}

func TestDialKnowComesFromTheBinaryNotTheOwner(t *testing.T) {
	raw, path := rawAheadDB(t)
	plain, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	_, embedded := plain.SchemaVersions()

	// An owner that advertises a different know must not move the dialled
	// handle's answer: know is this binary's embedded maximum, exactly what a
	// direct open computes.
	l, sock := shortSock(t)
	srv := owner.New(raw, 99, 1, "01ORIGIN")
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = l.Close()
	})

	dialled, err := db.Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = dialled.Close() })

	_, know := dialled.SchemaVersions()
	if know != embedded {
		t.Errorf("dialled know = %d, want this binary's embedded maximum %d", know, embedded)
	}
	if !dialled.Newer() {
		t.Error("a database at 99 is not reported newer")
	}
}
