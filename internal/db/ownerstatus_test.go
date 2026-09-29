//go:build unix

package db_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// TestProbeOwnerReportsFieldsAgainstARealOwner pins what the doctor row reads:
// the socket it dialled, the owner's pid, the protocol version and the live
// connection count.
func TestProbeOwnerReportsFieldsAgainstARealOwner(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	l, sock := shortSock(t)
	srv := db.NewOwner(d)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = l.Close()
	})

	status, err := db.ProbeOwner(sock)
	if err != nil {
		t.Fatalf("ProbeOwner: %v", err)
	}
	if status.Socket != sock {
		t.Errorf("Socket = %q, want %q", status.Socket, sock)
	}
	if status.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", status.PID, os.Getpid())
	}
	if status.Version != wire.Version {
		t.Errorf("Version = %d, want %d", status.Version, wire.Version)
	}
	if status.Conns < 1 {
		t.Errorf("Conns = %d, want at least the probing connection", status.Conns)
	}
}
