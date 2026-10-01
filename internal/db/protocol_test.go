//go:build unix

package db

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// rawHandshake dials sock and completes the client half of the handshake, so a
// test can drive the protocol without the driver.
func rawHandshake(t *testing.T, sock string) *wire.Conn {
	t.Helper()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	w := wire.NewConn(nc)
	payload, err := wire.Encode(wire.KindHello, &wire.Hello{
		Header:  wire.Header{Type: wire.TypeHello},
		Proto:   wire.Proto,
		Version: wire.Version,
	}, nil)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := w.Write(payload); err != nil {
		t.Fatalf("Write hello: %v", err)
	}
	frame, err := w.Read()
	if err != nil {
		t.Fatalf("Read welcome: %v", err)
	}
	if kind, _ := wire.Kind(frame); kind != wire.KindWelcome {
		t.Fatalf("first frame kind = %d, want welcome", kind)
	}
	return w
}

// rawQueryCounts runs a query over the raw connection and returns the row
// count and how many next frames the client had to send.
func rawQueryCounts(t *testing.T, w *wire.Conn, query string) (rows, nexts int) {
	t.Helper()
	payload, err := wire.Encode(wire.KindQuery, &wire.Query{
		Header: wire.Header{Type: wire.TypeQuery, ID: 1},
		Query:  query,
	}, nil)
	if err != nil {
		t.Fatalf("Encode query: %v", err)
	}
	if err := w.Write(payload); err != nil {
		t.Fatalf("Write query: %v", err)
	}
	for {
		frame, err := w.Read()
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		kind, err := wire.Kind(frame)
		if err != nil {
			t.Fatalf("Kind: %v", err)
		}
		switch kind {
		case wire.KindRows:
			var m wire.Rows
			if _, err := wire.Decode(frame, &m); err != nil {
				t.Fatalf("Decode rows: %v", err)
			}
			rows += m.Count
			if m.More {
				nexts++
				np, err := wire.Encode(wire.KindNext, &wire.Next{
					Header: wire.Header{Type: wire.TypeNext, ID: 1},
				}, nil)
				if err != nil {
					t.Fatalf("Encode next: %v", err)
				}
				if err := w.Write(np); err != nil {
					t.Fatalf("Write next: %v", err)
				}
			}
		case wire.KindDone:
			return rows, nexts
		case wire.KindError:
			var e wire.Error
			_, _ = wire.Decode(frame, &e)
			t.Fatalf("query error: %s", e.Message)
		default:
			t.Fatalf("unexpected frame kind %d", kind)
		}
	}
}

func execOn(t *testing.T, d *DB, query string, args ...any) {
	t.Helper()
	if _, err := d.sqlDB.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func TestFiveMegabyteBlobRoundTrips(t *testing.T) {
	d := openTestDB(t)
	execOn(t, d, `CREATE TABLE t (b BLOB)`)
	sock := startOwner(t, d)
	d2 := dialDB(t, sock)

	big := bytes.Repeat([]byte{0x5A}, 5<<20)
	if _, err := d2.sqlDB.Exec(`INSERT INTO t (b) VALUES (?)`, big); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var got []byte
	if err := d2.sqlDB.QueryRow(`SELECT b FROM t`).Scan(&got); err != nil {
		t.Fatalf("select: %v", err)
	}
	if !bytes.Equal(got, big) {
		t.Fatalf("blob changed: got %d bytes of %d", len(got), len(big))
	}
}

func TestFiftyMegabyteResultStreamsInBatches(t *testing.T) {
	d := openTestDB(t)
	execOn(t, d, `CREATE TABLE big (b BLOB)`)

	blob := bytes.Repeat([]byte{0x11}, 1<<20)
	tx, err := d.sqlDB.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i := 0; i < 50; i++ {
		if _, err := tx.Exec(`INSERT INTO big (b) VALUES (?)`, blob); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	sock := startOwner(t, d)
	w := rawHandshake(t, sock)
	rows, nexts := rawQueryCounts(t, w, `SELECT b FROM big`)
	if rows != 50 {
		t.Errorf("streamed %d rows, want 50", rows)
	}
	if nexts < 1 {
		t.Errorf("a 50 MB result arrived in a single batch (nexts=%d)", nexts)
	}
}

func TestClientDisconnectRollsBackAndKeepsSeq(t *testing.T) {
	d := openTestDB(t)
	execOn(t, d, `CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER NOT NULL)`)
	execOn(t, d, `INSERT INTO t (n) VALUES (1)`)
	sock := startOwner(t, d)

	d2 := dialDB(t, sock)
	conn, err := d2.sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO t (n) VALUES (2)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("conn close: %v", err)
	}
	// Dropping the whole handle closes the pooled connection, so the owner sees
	// the client go away mid-transaction.
	if err := d2.Close(); err != nil {
		t.Fatalf("handle close: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM t WHERE n = 2`).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the abandoned transaction was not rolled back")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The sequence continues without a gap at the value the rolled-back write
	// would have taken.
	if err := d.Tx(func(tx *Tx) error {
		var next int
		if err := tx.queryRow(`SELECT COALESCE(MAX(n), 0) + 1 FROM t`).Scan(&next); err != nil {
			return err
		}
		if next != 2 {
			t.Errorf("next seq = %d, want 2", next)
		}
		_, err := tx.exec(`INSERT INTO t (n) VALUES (?)`, next)
		return err
	}); err != nil {
		t.Fatalf("reinsert: %v", err)
	}
}

func TestPinnedTransactionDoesNotBlockOtherConnections(t *testing.T) {
	d := openTestDB(t)
	execOn(t, d, `CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER)`)
	sock := startOwner(t, d)
	d2 := dialDB(t, sock)

	ctx := context.Background()
	pinned, err := d2.sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer func() { _ = pinned.Close() }()
	if _, err := pinned.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := pinned.ExecContext(ctx, `INSERT INTO t (n) VALUES (1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	for i := 0; i < 2; i++ {
		other, err := d2.sqlDB.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn %d: %v", i, err)
		}
		var n int
		if err := other.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
			t.Fatalf("read %d while a transaction is pinned: %v", i, err)
		}
		if err := other.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}

	if _, err := pinned.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestBusyMapsToErrBusy(t *testing.T) {
	// A wire error carrying SQLITE_BUSY maps exactly as the driver's own does.
	if err := mapBusy(wire.NewError(1, 5, 5, "database is locked")); !errors.Is(err, ErrBusy) {
		t.Errorf("mapBusy(wire busy) = %v, want ErrBusy", err)
	}

	// End to end: a second BEGIN IMMEDIATE while one connection holds the write
	// lock comes back over the wire as ErrBusy.
	path := t.TempDir() + "/relevo.db"
	d, err := OpenWith(path, Options{BusyTimeout: 20 * time.Millisecond, BeginRetry: time.Millisecond})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	execOn(t, d, `CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER)`)
	sock := startOwner(t, d)
	d2 := dialWith(t, sock, Options{BeginRetry: time.Millisecond})

	if err := d2.Tx(func(tx *Tx) error {
		_, err := tx.exec(`INSERT INTO t (n) VALUES (1)`)
		return err
	}); err != nil {
		t.Fatalf("first tx: %v", err)
	}
	holder, err := d2.sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer func() { _ = holder.Close() }()
	if _, err := holder.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	err = d2.Tx(func(tx *Tx) error { return nil })
	if !errors.Is(err, ErrBusy) {
		t.Errorf("second Tx = %v, want ErrBusy", err)
	}
}

func TestConstraintMapsToErrInvalid(t *testing.T) {
	for _, code := range []int{19, 2067} {
		err := mapMasterMindKey(wire.NewError(1, code, code, "UNIQUE constraint failed: mastermind.session_id"))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("mapMasterMindKey(code %d) = %v, want ErrInvalid", code, err)
		}
	}
	fallback := mapMasterMindKey(errors.New("UNIQUE constraint failed: mastermind.session_id"))
	if !errors.Is(fallback, ErrInvalid) {
		t.Errorf("text fallback = %v, want ErrInvalid", fallback)
	}
}

func TestTestSocketPathFitsSunPath(t *testing.T) {
	_, sock := shortListener(t)
	if !strings.HasPrefix(sock, "/tmp/") {
		t.Errorf("test socket %q is not directly under /tmp", sock)
	}
	if len(sock) >= 104 {
		t.Errorf("test socket path is %d bytes, over the 104-byte sun_path", len(sock))
	}
}
