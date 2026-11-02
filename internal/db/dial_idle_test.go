//go:build unix

package db

import (
	"path/filepath"
	"testing"
	"time"
)

// The owner holds a connection slot for every client connection until it
// closes, so these tests count the owner's live connections: a dialled pool
// that kept idle connections would hold slots nobody is using, and enough
// long-lived clients would leave a new one waiting for a slot that never frees.

// servedSplit serves a split pair on a fresh socket, so a dialled handle has
// both the shared and the machine-local pool.
func servedSplit(t *testing.T) string {
	t.Helper()
	shared, err := OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	return startOwner(t, shared)
}

func TestDialledPoolsCloseIdleOwnerConnections(t *testing.T) {
	sock := servedSplit(t)
	base := ownerConns(t, sock)

	d := dialWith(t, sock, Options{idleTime: 50 * time.Millisecond})
	if d.Local() == nil {
		t.Fatal("the dialled handle has no machine-local pool")
	}
	if _, _, err := d.KVGet("k"); err != nil {
		t.Fatalf("KVGet on the shared pool: %v", err)
	}
	if _, _, err := d.Local().KVGet("k"); err != nil {
		t.Fatalf("KVGet on the local pool: %v", err)
	}
	if n := ownerConns(t, sock); n < base+2 {
		t.Fatalf("owner live connections = %d after a read on each pool, want at least %d", n, base+2)
	}

	waitOwnerConns(t, sock, base)
}

func TestDialledPoolKeepsOneIdleOwnerConnection(t *testing.T) {
	sock := servedSplit(t)
	base := ownerConns(t, sock)

	d := dialWith(t, sock, Options{idleTime: time.Hour})
	// A read while a transaction holds the first connection opens a second one,
	// so the pool has two to give back when both finish.
	err := d.Tx(func(*Tx) error {
		_, _, err := d.KVGet("k")
		return err
	})
	if err != nil {
		t.Fatalf("read inside a transaction: %v", err)
	}

	waitOwnerConns(t, sock, base+1)
}
