//go:build unix

package db

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// serveCrashOwner accepts connections and handles each in its own goroutine.
func serveCrashOwner(l net.Listener, dbh *sql.DB, have, know int) {
	for {
		nc, err := l.Accept()
		if err != nil {
			return
		}
		go crashOwnerConn(nc, dbh, have, know)
	}
}

// crashOwnerConn is an owner that greets one client, runs each statement it is
// sent against the real database, and then drops the connection without
// answering -- the crash window in which a statement already ran but its result
// never reached the client.
func crashOwnerConn(nc net.Conn, dbh *sql.DB, have, know int) {
	defer func() { _ = nc.Close() }()
	w := wire.NewConn(nc)
	if !greet(w, have, know) {
		return
	}
	for {
		frame, err := w.Read()
		if err != nil {
			return
		}
		kind, _ := wire.Kind(frame)
		if kind == wire.KindClose {
			return
		}
		if kind != wire.KindExec && kind != wire.KindQuery {
			continue
		}
		query, ok := frameQuery(frame)
		if !ok {
			return
		}
		_, _ = dbh.Exec(query)
		return
	}
}

// greet answers one hello with a welcome, reporting whether the handshake
// completed.
func greet(w *wire.Conn, have, know int) bool {
	frame, err := w.Read()
	if err != nil {
		return false
	}
	if kind, _ := wire.Kind(frame); kind != wire.KindHello {
		return false
	}
	payload, err := wire.Encode(wire.KindWelcome, &wire.Welcome{
		Header:     wire.Header{Type: wire.TypeWelcome},
		Proto:      wire.Proto,
		MinClient:  wire.Version,
		Version:    wire.Version,
		SchemaHave: have,
		SchemaKnow: know,
	}, nil)
	if err != nil {
		return false
	}
	return w.Write(payload) == nil
}

// frameQuery returns the SQL an exec or query frame carries.
func frameQuery(frame []byte) (string, bool) {
	var m struct {
		wire.Header
		Query string `json:"query"`
	}
	if _, err := wire.Decode(frame, &m); err != nil {
		return "", false
	}
	return m.Query, true
}

// TestLostConnectionAfterExecIsNotRetried pins the bad-connection discipline:
// once a request frame has been sent, a lost connection must not report
// driver.ErrBadConn, or database/sql retries the statement on a fresh
// connection and it runs twice.
func TestLostConnectionAfterExecIsNotRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	direct, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	execOn(t, direct, `CREATE TABLE t (n INTEGER)`)
	have, know := direct.SchemaVersions()

	l, sock := shortListener(t)
	go serveCrashOwner(l, direct.sqlDB, have, know)

	d2 := dialDB(t, sock)
	if _, err := d2.sqlDB.Exec(`INSERT INTO t (n) VALUES (1)`); err == nil {
		t.Fatal("Exec succeeded against an owner that dropped the connection")
	} else if errors.Is(err, driver.ErrBadConn) {
		t.Errorf("Exec error %v matches driver.ErrBadConn, so the statement would be retried", err)
	}

	var n int
	if err := direct.sqlDB.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("table holds %d rows, want exactly 1", n)
	}
}
