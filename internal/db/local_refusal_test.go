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
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// A reader that must not see machine-local rows may not be answered from the
// shared file when it asks for the local one. The two ways it can be refused are
// the wire's RefuseNoLocal and the nil Local() on a handle from an owner serving
// no local file; a silent downgrade in either would hand the caller the very
// rows the split keeps out of the shared file.

// serveUnsplit opens a database with no local companion, serves it on a socket
// short enough for sun_path, and returns the socket.
func serveUnsplit(t *testing.T) string {
	t.Helper()
	shared, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	dir, err := os.MkdirTemp("/tmp", "rvo-refuse-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "o.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv := db.NewOwner(shared)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
		_ = shared.Close()
	})
	return sock
}

// TestALocalScopeDialAgainstAnOwnerWithNoLocalFileIsRefused pins the refusal on
// the real protocol: a connection asking for the local file against an owner
// serving only the shared one is told no, and no rows come back at all.
func TestALocalScopeDialAgainstAnOwnerWithNoLocalFileIsRefused(t *testing.T) {
	sock := serveUnsplit(t)

	local := sql.OpenDB(client.ScopedConnector(sock, false, wire.ScopeLocal))
	t.Cleanup(func() { _ = local.Close() })

	_, err := local.Exec("SELECT count(*) FROM kv")
	var refusal *wire.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("the local scope against an owner with no local file = %v, want a refusal", err)
	}
	if refusal.Code != wire.RefuseNoLocal {
		t.Errorf("the refusal code is %q, want %q", refusal.Code, wire.RefuseNoLocal)
	}
}

// TestTheRefusedConnectionAnswersNoRows pins that the refusal is not a refusal
// followed by a shared-file answer: the connection is closed by the refusal, so
// every later statement on it fails rather than quietly reporting the shared
// file's rows under the name the caller asked for.
func TestTheRefusedConnectionAnswersNoRows(t *testing.T) {
	sock := serveUnsplit(t)

	local := sql.OpenDB(client.ScopedConnector(sock, false, wire.ScopeLocal))
	t.Cleanup(func() { _ = local.Close() })

	var firstErr error
	for range 2 {
		var n int
		if err := local.QueryRow("SELECT count(*) FROM kv").Scan(&n); err != nil {
			firstErr = err
			continue
		}
		t.Fatalf("a refused local scope answered with %d rows from the shared file", n)
	}
	var refusal *wire.Refusal
	if !errors.As(firstErr, &refusal) {
		t.Fatalf("the first query = %v, want a refusal", firstErr)
	}
	if refusal.Code != wire.RefuseNoLocal {
		t.Errorf("the refusal code is %q, want %q", refusal.Code, wire.RefuseNoLocal)
	}
}

// TestADialFromAnOwnerWithNoLocalFileReportsNoLocalHandle pins the same refusal
// through the handle a caller actually holds: Local() is nil, so a machine-local
// reader has nothing to bind and a caller that insists is the one to refuse.
func TestADialFromAnOwnerWithNoLocalFileReportsNoLocalHandle(t *testing.T) {
	sock := serveUnsplit(t)

	dialled, err := db.Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = dialled.Close() })
	if dialled.Local() != nil {
		t.Error("a handle dialled from an owner serving no local file reports one")
	}
	if dialled.LocalOrSelf() != dialled {
		t.Error("LocalOrSelf returned a handle other than the dialled one, so a caller could read the shared file believing it asked for the local one")
	}
}

// TestASplitPairServesTheLocalScope pins the other side, so the refusal above is
// a fact about a missing local file and not about the scope being unusable: an
// owner that serves one answers the local scope from that file.
func TestASplitPairServesTheLocalScope(t *testing.T) {
	dir := t.TempDir()
	shared, err := db.OpenSplit(filepath.Join(dir, "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	if err := shared.Local().KVPut("serve.daemon", []byte(`{"pid":1}`)); err != nil {
		t.Fatalf("seed the local file: %v", err)
	}
	sockDir, err := os.MkdirTemp("/tmp", "rvo-local-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "o.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv := db.NewOwner(shared)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
		_ = shared.Close()
	})

	dialled, err := db.Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = dialled.Close() })
	local := dialled.Local()
	if local == nil {
		t.Fatal("a handle dialled from an owner serving a local file reports none")
	}
	if _, ok, err := local.KVGet("serve.daemon"); err != nil || !ok {
		t.Errorf("the local file's row through the dial = (present %t, err %v), want it there", ok, err)
	}
}
