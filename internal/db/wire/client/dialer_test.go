//go:build unix

package client_test

import (
	"context"
	"database/sql"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// TestSetDialerRoutesInfoAndPooledConns pins that one hook serves both the
// handshake and the driver's pooled connections: every dial goes through it.
func TestSetDialerRoutesInfoAndPooledConns(t *testing.T) {
	sock, _ := startOwner(t)

	var calls int32
	client.SetDialer(func(ctx context.Context, s string) (net.Conn, error) {
		atomic.AddInt32(&calls, 1)
		var d net.Dialer
		return d.DialContext(ctx, "unix", s)
	})
	t.Cleanup(func() { client.SetDialer(nil) })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.Info(ctx, sock); err != nil {
		t.Fatalf("Info: %v", err)
	}
	if atomic.LoadInt32(&calls) == 0 {
		t.Fatal("Info did not go through the dial hook")
	}

	before := atomic.LoadInt32(&calls)
	sqlDB, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := sqlDB.Exec(`CREATE TABLE dialer_pin (n INTEGER)`); err != nil {
		t.Fatalf("exec through a pooled connection: %v", err)
	}
	if atomic.LoadInt32(&calls) <= before {
		t.Error("a pooled connection did not go through the dial hook")
	}
}
