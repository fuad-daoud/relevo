//go:build unix

package client_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// shortListener binds a unix socket directly under /tmp, because the path is
// length limited and a t.TempDir() under a long root can exceed it.
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

func startOwner(t *testing.T) (string, *db.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	l, sock := shortListener(t)
	srv := db.NewOwner(d)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock, d
}

func TestInfoReportsTheOwnerSchemaAndOrigin(t *testing.T) {
	sock, d := startOwner(t)
	have, know := d.SchemaVersions()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	info, err := client.Info(ctx, sock)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Have != have || info.Know != know {
		t.Errorf("Info = have %d know %d, want %d/%d", info.Have, info.Know, have, know)
	}
	if info.Origin != "" {
		t.Errorf("Info origin = %q, want empty for a plain open", info.Origin)
	}
}

func TestDriverRunsExecAndQueryWithEveryValueClass(t *testing.T) {
	sock, _ := startOwner(t)
	sqlDB, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	if _, err := sqlDB.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, s TEXT, b BLOB, f REAL, n TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	blob := []byte{0, 1, 255}
	res, err := sqlDB.Exec(`INSERT INTO t (s, b, f, n) VALUES (?, ?, ?, ?)`, "hello", blob, 2.5, nil)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Errorf("RowsAffected = %d, want 1", n)
	}
	if id, err := res.LastInsertId(); err != nil || id == 0 {
		t.Errorf("LastInsertId = %d, %v", id, err)
	}

	var (
		id int64
		s  string
		b  []byte
		f  float64
		n  sql.Null[string]
	)
	if err := sqlDB.QueryRow(`SELECT id, s, b, f, n FROM t`).Scan(&id, &s, &b, &f, &n); err != nil {
		t.Fatalf("select: %v", err)
	}
	if id == 0 || s != "hello" || !bytes.Equal(b, blob) || f != 2.5 || n.Valid {
		t.Errorf("read back id=%d s=%q b=%v f=%v n=%v", id, s, b, f, n)
	}
}

func TestDriverRoundTripsAValueLargerThanTheBatchBudget(t *testing.T) {
	sock, _ := startOwner(t)
	sqlDB, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	if _, err := sqlDB.Exec(`CREATE TABLE t (b BLOB)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	big := bytes.Repeat([]byte{0x7F}, 3<<20)
	if _, err := sqlDB.Exec(`INSERT INTO t (b) VALUES (?)`, big); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var got []byte
	if err := sqlDB.QueryRow(`SELECT b FROM t`).Scan(&got); err != nil {
		t.Fatalf("select: %v", err)
	}
	if !bytes.Equal(got, big) {
		t.Fatalf("blob changed: got %d bytes", len(got))
	}
}

// fakeRefusal answers one handshake with a refusal, so a client-side mapping
// can be pinned without a real owner.
func fakeRefusal(t *testing.T, code string) string {
	t.Helper()
	l, sock := shortListener(t)
	go func() {
		nc, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = nc.Close() }()
		w := wire.NewConn(nc)
		if _, err := w.Read(); err != nil {
			return
		}
		payload, err := wire.Encode(wire.KindRefuse, &wire.Refusal{
			Header:  wire.Header{Type: wire.TypeRefuse},
			Code:    code,
			Message: code,
		}, nil)
		if err != nil {
			return
		}
		_ = w.Write(payload)
	}()
	return sock
}

func TestClientMapsRestartingRefusal(t *testing.T) {
	sock := fakeRefusal(t, wire.RefuseRestarting)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := client.Info(ctx, sock)
	if err == nil {
		t.Fatal("Info succeeded against a refusing owner")
	}
	var ref *wire.Refusal
	if !errors.As(err, &ref) {
		t.Fatalf("error %v is not a refusal", err)
	}
	if ref.Code != wire.RefuseRestarting {
		t.Errorf("refusal code = %q, want %q", ref.Code, wire.RefuseRestarting)
	}
}

// sawHello accepts one connection, reads its hello, answers a welcome, and
// reports the hello's ad-hoc marker on the returned channel.
func sawHello(t *testing.T, l net.Listener) <-chan bool {
	t.Helper()
	got := make(chan bool, 1)
	go func() {
		nc, err := l.Accept()
		if err != nil {
			got <- false
			return
		}
		defer func() { _ = nc.Close() }()
		w := wire.NewConn(nc)
		frame, err := w.Read()
		if err != nil {
			got <- false
			return
		}
		var h wire.Hello
		if _, err := wire.Decode(frame, &h); err != nil {
			got <- false
			return
		}
		got <- h.AdHoc
		payload, err := wire.Encode(wire.KindWelcome, &wire.Welcome{
			Header:  wire.Header{Type: wire.TypeWelcome},
			Proto:   wire.Proto,
			Version: wire.Version,
		}, nil)
		if err == nil {
			_ = w.Write(payload)
		}
	}()
	return got
}

// TestConnectorMarksItsHelloAdHoc pins the ad-hoc marker: a pool built from
// client.Connector sends the bit in its handshake, and a plain
// sql.Open(client.DriverName, ...) never does. The fake owner answers the
// handshake and closes; Ping is enough to open the connection, so no request
// frame is sent.
func TestConnectorMarksItsHelloAdHoc(t *testing.T) {
	l, sock := shortListener(t)
	got := sawHello(t, l)
	adhoc := sql.OpenDB(client.Connector(sock, true))
	t.Cleanup(func() { _ = adhoc.Close() })
	if err := adhoc.Ping(); err != nil {
		t.Fatalf("ping the ad-hoc pool: %v", err)
	}
	if !<-got {
		t.Error("the ad-hoc connector's hello did not carry the ad-hoc bit")
	}

	l2, sock2 := shortListener(t)
	got2 := sawHello(t, l2)
	plain, err := sql.Open(client.DriverName, sock2)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	if err := plain.Ping(); err != nil {
		t.Fatalf("ping the plain pool: %v", err)
	}
	if <-got2 {
		t.Error("a plain sql.Open hello carried the ad-hoc bit")
	}
}
