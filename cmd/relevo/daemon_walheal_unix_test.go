//go:build unix

package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestServeOwnerInstallsTheShortReadHook pins the unix wiring: the owner the
// daemon serves carries the short-read hook, so a poisoned pool reaches the
// per-tick heal decision. It serves a temp database over a unix socket only,
// with no harness and no network.
func TestServeOwnerInstallsTheShortReadHook(t *testing.T) {
	dir, err := os.MkdirTemp("", "rvo-walheal")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	d, err := openDBDirect(filepath.Join(dir, "relevo.db"))
	if err != nil {
		t.Fatalf("openDBDirect: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	sock := filepath.Join(dir, "o.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	srv, err := serveOwner(d, ln, nil, func() {})
	if err != nil {
		t.Fatalf("serveOwner: %v", err)
	}
	if srv == nil {
		t.Fatal("serveOwner returned no server")
	}
	t.Cleanup(func() { _ = srv.Close() })
	if srv.OnShortReadWAL == nil {
		t.Fatal("OnShortReadWAL is nil, want the daemon's hook installed")
	}
}
