//go:build unix

package db_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// TestBusyCodeCrossesTheWireUnderEitherDriver pins that the owner names the
// SQLite code of a driver error on the wire. The assertion is on the raw wire
// error, so a string fallback cannot mask a missing engine mapping: under
// modernc the driver carries the code, under Turso only the engine mapping does.
func TestBusyCodeCrossesTheWireUnderEitherDriver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	d, err := db.OpenWith(path, db.Options{BusyTimeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	l, sock := shortSock(t)
	srv := db.NewOwner(d)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

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
	if _, err := hc.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("holder BEGIN IMMEDIATE: %v", err)
	}
	t.Cleanup(func() { _, _ = hc.ExecContext(context.Background(), "ROLLBACK") })

	loser, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("loser open: %v", err)
	}
	t.Cleanup(func() { _ = loser.Close() })

	_, err = loser.ExecContext(context.Background(), "BEGIN IMMEDIATE")
	if err == nil {
		t.Fatal("the second BEGIN IMMEDIATE = nil, want a busy error")
	}
	code, ok := wire.CodeOf(err)
	if !ok || (code != 5 && code != 19) {
		t.Fatalf("wire.CodeOf(%v) = (%d, ok %v), want 5 or 19", err, code, ok)
	}
}
