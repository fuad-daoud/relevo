//go:build unix

package db

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// shortListener binds a unix socket directly under /tmp: the path is length
// limited, and a t.TempDir() under a long root can exceed it.
func shortListener(t *testing.T) (net.Listener, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "o.sock")
	if len(sock) >= 104 {
		t.Fatalf("socket path %q is %d bytes, over the 104-byte sun_path", sock, len(sock))
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, sock
}

// startOwner serves d on a fresh short-path socket and returns it.
func startOwner(t *testing.T, d *DB) string {
	t.Helper()
	l, sock := shortListener(t)
	srv := NewOwner(d)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// dialDB dials sock with the owner's own answer for origin.
func dialDB(t *testing.T, sock string) *DB {
	t.Helper()
	return dialWith(t, sock, Options{})
}

func dialWith(t *testing.T, sock string, o Options) *DB {
	t.Helper()
	d, err := dial(sock, o, true)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestDialAndNewOwnerAgreeOnSchemaOriginAndRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := OpenWith(path, Options{Origin: "01ORIGINORIGINORIGINORIGIN"})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	sock := startOwner(t, d)
	d2 := dialDB(t, sock)

	have, know := d.SchemaVersions()
	have2, know2 := d2.SchemaVersions()
	if have2 != have || know2 != know {
		t.Errorf("dialled versions = %d/%d, want %d/%d", have2, know2, have, know)
	}
	if d2.Newer() {
		t.Error("dialled handle reports a newer schema for a current database")
	}
	if d2.origin != "01ORIGINORIGINORIGINORIGIN" {
		t.Errorf("dialled origin = %q, want the owner's", d2.origin)
	}
	if !d2.hasConfig() {
		t.Error("dialled handle reports no config tables")
	}

	if err := d2.Tx(func(tx *Tx) error {
		return tx.KVPut("dialled", []byte(`{"x":1}`))
	}); err != nil {
		t.Fatalf("write through the dialled handle: %v", err)
	}
	raw, ok, err := d.KVGet("dialled")
	if err != nil || !ok || string(raw) != `{"x":1}` {
		t.Fatalf("owner read = %q, %v, %v", raw, ok, err)
	}
}

func TestDialRefusesAnUnreachableSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	_, err = Dial(filepath.Join(dir, "missing.sock"))
	if err == nil {
		t.Fatal("Dial succeeded with no owner listening")
	}
	if !errors.Is(err, ErrOpen) {
		t.Errorf("Dial error %v does not wrap ErrOpen", err)
	}
}

func TestNewOwnerServesRowsReadByADirectHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	sock := startOwner(t, d)
	d2 := dialDB(t, sock)

	if err := d2.Tx(func(tx *Tx) error {
		return tx.KVPut("served", []byte(`{"ok":true}`))
	}); err != nil {
		t.Fatalf("write through the dialled handle: %v", err)
	}
	raw, ok, err := d.KVGet("served")
	if err != nil || !ok || string(raw) != `{"ok":true}` {
		t.Fatalf("owner read = %q, %v, %v", raw, ok, err)
	}
}

// TestVacuumRefusesADialledHandle pins that Vacuum refuses a handle reached over
// the wire: the swap closes and reopens the pool, which only the handle that
// owns the file may do, so a dialled handle is refused rather than left with a
// stranded pool.
func TestVacuumRefusesADialledHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	direct := directOpen(t, path, Options{})
	sock := startOwner(t, direct)
	dialled := dialDB(t, sock)

	if err := dialled.Vacuum(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Vacuum on a dialled handle = %v, want ErrInvalid", err)
	}
}

// TestCloseNotifiesTheOwnerHopObserver pins the seam dbtest's owner mode uses:
// when a handle opened through the hop closes, the observer installed beside the
// hop is told once with the socket, so the harness can stop that path's owner
// and close its direct handle the way the daemon does on its own stop.
func TestCloseNotifiesTheOwnerHopObserver(t *testing.T) {
	prevHop, prevClosed := ownerHop, ownerHopClosed
	t.Cleanup(func() { ownerHop, ownerHopClosed = prevHop, prevClosed })

	path := filepath.Join(t.TempDir(), "relevo.db")
	direct := directOpen(t, path, Options{})
	sock := startOwner(t, direct)

	SetOwnerHop(func(string, Options, func() (*DB, error)) (string, error) { return sock, nil })
	var closed []string
	SetOwnerHopClosed(func(s string) { closed = append(closed, s) })

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open through the hop: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close dialled handle: %v", err)
	}
	if len(closed) != 1 || closed[0] != sock {
		t.Fatalf("close observer got %v, want [%s]", closed, sock)
	}
	// A second close must not notify again.
	_ = d.Close()
	if len(closed) != 1 {
		t.Errorf("close observer ran %d times, want 1", len(closed))
	}
}
