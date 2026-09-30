//go:build unix

package db_test

import (
	"context"
	"database/sql"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// serveBeginWelcome reads one hello and answers a welcome.
func serveBeginWelcome(w *wire.Conn) bool {
	if _, err := w.Read(); err != nil {
		return false
	}
	payload, err := wire.Encode(wire.KindWelcome, &wire.Welcome{
		Header:     wire.Header{Type: wire.TypeWelcome},
		Proto:      wire.Proto,
		MinClient:  wire.Version,
		Version:    wire.Version,
		SchemaHave: 1,
		SchemaKnow: 1,
	}, nil)
	if err != nil {
		return false
	}
	return w.Write(payload) == nil
}

// decodeBeginArgs reads the request frame's value batch into the arguments
// database/sql would have passed.
func decodeBeginArgs(raw []byte) ([]any, error) {
	cur := wire.NewCursor(raw)
	var out []any
	for {
		v, ok, err := cur.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		out = append(out, v)
	}
}

// serveBeginRefusing runs one client connection against raw: the first
// BEGIN IMMEDIATE in the whole test is refused with restarting, and every other
// statement executes on a pinned connection so a transaction spans requests.
func serveBeginRefusing(nc net.Conn, raw *sql.DB, refused *int32) {
	defer func() { _ = nc.Close() }()
	w := wire.NewConn(nc)
	if !serveBeginWelcome(w) {
		return
	}
	conn, err := raw.Conn(context.Background())
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	for {
		frame, err := w.Read()
		if err != nil {
			return
		}
		kind, err := wire.Kind(frame)
		if err != nil || kind != wire.KindExec {
			return
		}
		var m wire.Exec
		rawArgs, err := wire.Decode(frame, &m)
		if err != nil {
			return
		}
		args, err := decodeBeginArgs(rawArgs)
		if err != nil {
			return
		}
		if m.Query == "BEGIN IMMEDIATE" && atomic.CompareAndSwapInt32(refused, 0, 1) {
			payload, _ := wire.Encode(wire.KindRefuse, &wire.Refusal{
				Header:  wire.Header{Type: wire.TypeRefuse},
				Code:    wire.RefuseRestarting,
				Message: "the owner is restarting",
			}, nil)
			_ = w.Write(payload)
			return
		}
		if _, err := conn.ExecContext(context.Background(), m.Query, args...); err != nil {
			payload, _ := wire.Encode(wire.KindError, wire.NewError(m.ID, 1, 1, err.Error()), nil)
			if w.Write(payload) != nil {
				return
			}
			continue
		}
		payload, _ := wire.Encode(wire.KindDone, &wire.Done{
			Header: wire.Header{Type: wire.TypeDone, ID: m.ID},
		}, nil)
		if w.Write(payload) != nil {
			return
		}
	}
}

// beginRefusingOwner serves raw over a socket that refuses the first BEGIN.
func beginRefusingOwner(t *testing.T, raw *sql.DB) string {
	t.Helper()
	l, sock := shortSock(t)
	var refused int32
	go func() {
		for {
			nc, err := l.Accept()
			if err != nil {
				return
			}
			go serveBeginRefusing(nc, raw, &refused)
		}
	}()
	return sock
}

// TestTxRetriesARestartingBegin pins the fresh-connection retry: a BEGIN refused
// restarting is retried on a new owner connection inside the busy window, and
// the transaction's write lands exactly once.
func TestTxRetriesARestartingBegin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	dialled, err := db.Dial(beginRefusingOwner(t, raw))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = dialled.Close() })

	if err := dialled.Tx(func(tx *db.Tx) error {
		return tx.KVPut("restart-pin", []byte(`"once"`))
	}); err != nil {
		t.Fatalf("Tx after a refused BEGIN: %v", err)
	}

	var count int
	if err := raw.QueryRow(`SELECT count(*) FROM kv WHERE key = 'restart-pin'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("rows = %d, want exactly 1", count)
	}
}
