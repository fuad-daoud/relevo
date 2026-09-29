//go:build unix

package client_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"database/sql"

	_ "modernc.org/sqlite"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// serveFakeWelcome reads one hello and answers a welcome.
func serveFakeWelcome(nc net.Conn, w *wire.Conn) bool {
	if _, err := w.Read(); err != nil {
		return false
	}
	payload, err := wire.Encode(wire.KindWelcome, &wire.Welcome{
		Header:     wire.Header{Type: wire.TypeWelcome},
		Proto:      wire.Proto,
		MinClient:  wire.Version,
		Version:    wire.Version,
		SchemaHave: 3,
		SchemaKnow: 9,
		Origin:     "01ORIGIN",
		PID:        1,
		Conns:      1,
	}, nil)
	if err != nil {
		return false
	}
	return w.Write(payload) == nil
}

func refuseRestarting(w *wire.Conn) {
	payload, err := wire.Encode(wire.KindRefuse, &wire.Refusal{
		Header:  wire.Header{Type: wire.TypeRefuse},
		Code:    wire.RefuseRestarting,
		Message: "the owner is restarting",
	}, nil)
	if err != nil {
		return
	}
	_ = w.Write(payload)
}

// restartingOwner answers every handshake and refuses every exec with
// restarting, so the driver's pool retries exhaust and the last refusal reaches
// the caller.
func restartingOwner(t *testing.T) string {
	t.Helper()
	l, sock := shortListener(t)
	go func() {
		for {
			nc, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = nc.Close() }()
				w := wire.NewConn(nc)
				if !serveFakeWelcome(nc, w) {
					return
				}
				if _, err := w.Read(); err != nil {
					return
				}
				refuseRestarting(w)
			}()
		}
	}()
	return sock
}

// TestDriverSeesARestartingRefusal pins the mid-request mapping: a refusal that
// arrives after the request was sent is not a lost connection. It wraps
// driver.ErrBadConn so database/sql may retry, and stays inspectable.
func TestDriverSeesARestartingRefusal(t *testing.T) {
	sock := restartingOwner(t)
	sqlDB, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	_, err = sqlDB.ExecContext(context.Background(), `CREATE TABLE t (n INTEGER)`)
	if err == nil {
		t.Fatal("Exec against a refuse-forever owner succeeded")
	}
	if !errors.Is(err, driver.ErrBadConn) {
		t.Errorf("error %v must be driver.ErrBadConn so the pool retries", err)
	}
	var ref *wire.Refusal
	if !errors.As(err, &ref) || ref.Code != wire.RefuseRestarting {
		t.Errorf("error %v must carry the restarting refusal", err)
	}
}

// serveRefuseThenRun is the fake owner for the retry pin: the first connection
// refuses its exec with restarting, every later one runs the SQL against db.
func serveRefuseThenRun(nc net.Conn, n int, raw *sql.DB) {
	defer func() { _ = nc.Close() }()
	w := wire.NewConn(nc)
	if !serveFakeWelcome(nc, w) {
		return
	}
	frame, err := w.Read()
	if err != nil {
		return
	}
	if kind, _ := wire.Kind(frame); kind != wire.KindExec {
		return
	}
	if n == 1 {
		refuseRestarting(w)
		return
	}
	var m wire.Exec
	if _, err := wire.Decode(frame, &m); err != nil {
		return
	}
	if _, err := raw.Exec(m.Query); err != nil {
		payload, _ := wire.Encode(wire.KindError, wire.NewError(m.ID, 1, 1, err.Error()), nil)
		_ = w.Write(payload)
		return
	}
	payload, _ := wire.Encode(wire.KindDone, &wire.Done{
		Header:       wire.Header{Type: wire.TypeDone, ID: m.ID},
		RowsAffected: 1,
	}, nil)
	_ = w.Write(payload)
}

// serveRefuseThenRunListener accepts every connection and hands it to
// serveRefuseThenRun with its arrival number.
func serveRefuseThenRunListener(t *testing.T, raw *sql.DB) string {
	t.Helper()
	l, sock := shortListener(t)
	go func() {
		conns := 0
		for {
			nc, err := l.Accept()
			if err != nil {
				return
			}
			conns++
			go serveRefuseThenRun(nc, conns, raw)
		}
	}()
	return sock
}

// TestARestartingRefusalIsRetriedOnce pins the retry contract: a pool-level
// exec refused restarting is retried on a fresh connection, and once an owner
// serves it the statement is applied exactly once.
func TestARestartingRefusalIsRetriedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	sock := serveRefuseThenRunListener(t, raw)
	sqlDB, err := sql.Open(client.DriverName, sock)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	if _, err := sqlDB.Exec(`INSERT INTO t (n) VALUES (1)`); err != nil {
		t.Fatalf("exec after a refused first attempt: %v", err)
	}
	var count int
	if err := raw.QueryRow(`SELECT count(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("rows = %d, want exactly 1", count)
	}
}
