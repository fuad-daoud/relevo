//go:build unix

package db_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// TestProbeOwnerReportsFieldsAgainstARealOwner pins what the doctor row reads:
// the socket it dialled, the owner's pid, the protocol version, the live
// connection count and the pinned slot count beside it.
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

	// One held connection pins a slot, so the probe reports real work beside
	// the live total rather than two bare counts.
	holder, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("holder open: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	hc, err := holder.Conn(context.Background())
	if err != nil {
		t.Fatalf("holder Conn: %v", err)
	}
	t.Cleanup(func() { _ = hc.Close() })
	if _, err := hc.ExecContext(context.Background(), `CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatalf("holder exec: %v", err)
	}

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
	if status.Conns < 2 {
		t.Errorf("Conns = %d, want at least the holder and the probing connection", status.Conns)
	}
	if status.Pinned != 1 {
		t.Errorf("Pinned = %d, want 1 with one pinned connection", status.Pinned)
	}
}
