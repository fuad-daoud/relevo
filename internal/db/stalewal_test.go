//go:build unix && !modernc

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

// sidecarSize is the size of the sidecar at path, or -1 when it is absent: a
// missing WAL and an empty one are different states, and the incident is the
// empty one.
func sidecarSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return -1
	}
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Size()
}

// seedKVRows writes n keys of about a kilobyte each, so a later full scan
// touches pages a single-key read never warmed.
func seedKVRows(t *testing.T, d *DB, prefix string, n int) map[string]string {
	t.Helper()
	rows := make(map[string]string, n)
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("%s-%02d", prefix, i)
		v := fmt.Sprintf(`{"pad":%q}`, strings.Repeat(fmt.Sprintf("%s-%d-", prefix, i), 64))
		if err := d.KVPut(k, []byte(v)); err != nil {
			t.Fatalf("KVPut %s: %v", k, err)
		}
		rows[k] = v
	}
	return rows
}

// readKVRows reads every row back through the handle, failing on the first
// miss rather than collecting them.
func readKVRows(t *testing.T, d *DB, rows map[string]string) {
	t.Helper()
	for k, want := range rows {
		got, ok, err := d.KVGet(k)
		if err != nil {
			t.Fatalf("KVGet %s: %v", k, err)
		}
		if !ok {
			t.Fatalf("KVGet %s: missing", k)
		}
		if string(got) != want {
			t.Fatalf("KVGet %s: got %d bytes, want %d", k, len(got), len(want))
		}
	}
}

// readKVKeys scans every key through the handle: a full scan must walk the
// -wal for rows that never reached the main file, so it is the statement that
// meets a truncate under the live pool.
func readKVKeys(t *testing.T, d *DB) []string {
	t.Helper()
	keys, err := d.KVKeys("")
	if err != nil {
		t.Fatalf("KVKeys: %v", err)
	}
	return keys
}

// manufactureStaleWal truncates the -wal under the handle's live pool the way
// an interrupted conversion leaves it: a 0-byte -wal beside a live 32KB -shm.
// It returns only after asserting that artifact, so the read that follows runs
// against the incident's own shape.
func manufactureStaleWal(t *testing.T, path string) {
	t.Helper()
	// The pool stays open: closing it deletes the drained sidecars, and the
	// artifact under test is the live one. openSQLiteTestDB's cleanup closes
	// it after the test.
	mpool := openSQLiteTestDB(t, path)
	if _, err := mpool.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("modernc checkpoint: %v", err)
	}
	if n := sidecarSize(t, path+"-wal"); n != 0 {
		t.Fatalf("-wal is %d bytes, want the 0-byte incident artifact", n)
	}
	if n := sidecarSize(t, path+"-shm"); n != 32*1024 {
		t.Fatalf("-shm is %d bytes, want the live 32KB index", n)
	}
}

// holdPoolConn checks a connection out of the served pool and reads through
// it, anchoring the pool's WAL view across the manufacture below: with no
// connection out, a fresh one rebuilds its view from the files and never
// meets the truncate, while a live server always has connections out. The
// caller holds it until its reads are done.
func holdPoolConn(t *testing.T, ctx context.Context, d *DB) *sql.Conn {
	t.Helper()
	hold, err := d.sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("hold a pool connection: %v", err)
	}
	var n int
	if err := hold.QueryRowContext(ctx, `SELECT COUNT(*) FROM kv`).Scan(&n); err != nil {
		_ = hold.Close()
		t.Fatalf("read through the held connection: %v", err)
	}
	return hold
}

// serveCountingOwner serves d exactly as NewOwner does, but counts the stale
// pool rebuilds: the hook is NewOwner's own body, line for line, plus the
// counter the test asserts on.
func serveCountingOwner(t *testing.T, d *DB, calls *int) string {
	t.Helper()
	l, sock := shortListener(t)
	srv := NewOwner(d)
	srv.SetReopen(func(local bool) (*sql.DB, error) {
		*calls++
		return d.reopenPool()
	})
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = l.Close()
	})
	return sock
}

// TestOwnerRecoversStaleWalRead pins the incident's recovery: a truncate under
// the live pool poisons every later statement through it, and the owner heals
// the pool instead of failing every read after. The test holds one connection
// open so the failure is deterministic: the engine keeps a stale WAL view
// while any pre-truncate reader is still checked out. The write carries the
// swap -- a write is never retried, so its own error comes back -- and once
// the anchor is released the checkpointed rows read back through the fresh
// pool.
func TestOwnerRecoversStaleWalRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d := directOpen(t, path, Options{})
	kept := seedKVRows(t, d, "kept", 20)
	if err := d.walCheckpoint(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// The WAL-only rows stay in the -wal: only a pool that wrote to it holds
	// the frame end a truncate then pulls away, which is what makes the later
	// statement fail. The manufacture checkpoints them into the main file on
	// its way to the 0-byte artifact, so they survive it. Every statement here
	// runs directly: the owner starts only after the manufacture, so nothing
	// heals the pool before the statement under test.
	lost := seedKVRows(t, d, "lost", 10)
	// A held connection anchors the stale view: with none out, a fresh
	// connection rebuilds its view from the files and never meets the
	// truncate, so nothing would fail and there would be nothing to heal.
	hold := holdPoolConn(t, context.Background(), d)
	defer func() { _ = hold.Close() }()
	// The cleanup close checkpoints, which the engine does not survive on a
	// pool the truncate touched; the test asserts on statements, never on
	// that checkpoint.
	d.readOnly = true
	manufactureStaleWal(t, path)

	oldPool := d.sqlDB
	var calls int
	r := dialDB(t, serveCountingOwner(t, d, &calls))

	// The write meets the truncate and fails. The hook runs once and, the
	// anchor still being checked out, its probe refuses the fresh pool -- so
	// the pool is not swapped, and the verb fails loudly with its own error
	// instead of stranding the handle on a broken swap.
	if err := r.KVPut("lost-99", []byte(`{"a":99}`)); err == nil {
		t.Fatal("a write through the poisoned pool succeeded, want the stale-WAL failure")
	} else if !wire.IsShortReadWAL(err) {
		t.Fatalf("write failed as %v, want the stale-WAL failure", err)
	}
	if calls != 1 {
		t.Fatalf("recovery ran %d times, want exactly once", calls)
	}
	if d.sqlDB != oldPool {
		t.Fatal("the owner swapped in a pool its probe had to refuse")
	}
	// With the anchor released, the pool reads cleanly again; a direct heal
	// -- the hook's own path, now quiescent -- swaps it and answers every
	// row the manufacture checkpointed.
	if err := hold.Close(); err != nil {
		t.Fatalf("release the anchor: %v", err)
	}
	if _, err := d.reopenPool(); err != nil {
		t.Fatalf("heal after the anchor released: %v", err)
	}
	if d.sqlDB == oldPool {
		t.Fatal("the hook did not swap the pool once quiescent")
	}
	// Serve the healed pool under a fresh owner and read every row back
	// through it: the incident's rows are intact, only the view was stale.
	healed := dialDB(t, startOwner(t, d))
	readKVRows(t, healed, lost)
	keys := readKVKeys(t, healed)
	if len(keys) != len(kept)+len(lost) {
		t.Fatalf("scan returned %d keys, want the %d checkpointed ones", len(keys), len(kept)+len(lost))
	}
	readKVRows(t, healed, kept)
	// The hook drops the -shm beside the empty -wal: it indexes frames the
	// truncate destroyed, and the fresh pool must not read it.
	if n := sidecarSize(t, path+"-shm"); n != -1 {
		t.Errorf("-shm still present (%d bytes), want it dropped beside the empty -wal", n)
	}
}

// TestOwnerWithoutAReopenHookStillFailsAStaleWalRead records the failure the
// recovery heals: the same artifact without a hook fails the read, and the
// rebuilt error still matches the stale-WAL classifier.
func TestOwnerWithoutAReopenHookStillFailsAStaleWalRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d := directOpen(t, path, Options{})
	seedKVRows(t, d, "kept", 20)
	if err := d.walCheckpoint(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	// Direct traffic only, exactly as the recovery test poisons its pool: the
	// owner starts after the manufacture and adds nothing before the read.
	_ = seedKVRows(t, d, "lost", 10)
	for _, k := range []string{"lost-00", "lost-01", "lost-02", "lost-03", "lost-04"} {
		if _, _, err := d.KVGet(k); err != nil {
			t.Fatalf("pre-read %s: %v", k, err)
		}
	}
	if _, _, err := d.KVGet("kept-00"); err != nil {
		t.Fatalf("pre-read: %v", err)
	}
	hold := holdPoolConn(t, context.Background(), d)
	defer func() { _ = hold.Close() }()
	// The cleanup close checkpoints, which the engine does not survive on a
	// pool the truncate touched; the test asserts on reads, never on that
	// checkpoint.
	d.readOnly = true
	manufactureStaleWal(t, path)

	l, sock := shortListener(t)
	srv := owner.New(d.sqlDB, d.have, d.know, d.origin, errCode)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = l.Close()
	})
	r := dialDB(t, sock)

	_, _, err := r.KVGet("lost-05")
	if err == nil {
		t.Fatal("a cold re-read through the poisoned pool succeeded, want the stale-WAL failure")
	}
	if !wire.IsShortReadWAL(err) {
		t.Errorf("IsShortReadWAL(%v) = false, want true", err)
	}
	t.Logf("stale read failed as: %v", err)
	// No heal here: a fresh pool cannot open while the anchor is still out,
	// so the hook would refuse exactly as the owner did. The read-only flag
	// above already keeps the cleanup close from checkpointing the poisoned
	// pool, which is the only shutdown the engine would not survive.
}

// TestReopenPoolKeepsSidecarsBesideANonEmptyWal pins the recovery's guard: a
// -wal that still holds frames may index rows the fresh pool needs, so the
// hook reopens without removing the -shm and without checkpointing, and the
// WAL-only rows still read.
func TestReopenPoolKeepsSidecarsBesideANonEmptyWal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d := directOpen(t, path, Options{})
	rows := seedKVRows(t, d, "wal", 20)
	// The hook only stats the -shm, never reads it; zeros stand in for any
	// index beside the WAL.
	if err := os.WriteFile(path+"-shm", make([]byte, 32*1024), 0o600); err != nil {
		t.Fatalf("fabricate -shm: %v", err)
	}
	walBefore := sidecarSize(t, path+"-wal")
	if walBefore <= 0 {
		t.Fatalf("-wal is %d bytes, want frames at stake", walBefore)
	}

	fresh, err := d.reopenPool()
	if err != nil {
		t.Fatalf("reopenPool: %v", err)
	}
	defer func() { _ = fresh.Close() }()
	if d.sqlDB != fresh {
		t.Error("the handle kept its old pool, want it moved onto the fresh one")
	}
	if n := sidecarSize(t, path+"-shm"); n != 32*1024 {
		t.Errorf("-shm is %d bytes, want it kept beside the non-empty -wal", n)
	}
	if n := sidecarSize(t, path+"-wal"); n != walBefore {
		t.Errorf("-wal is %d bytes, want the %d bytes it held (no checkpoint)", n, walBefore)
	}
	for k, want := range rows {
		var got string
		if err := fresh.QueryRow(`SELECT value_json FROM kv WHERE key = ?`, k).Scan(&got); err != nil {
			t.Fatalf("read %s from the fresh pool: %v", k, err)
		}
		if got != want {
			t.Fatalf("read %s: got %d bytes, want %d", k, len(got), len(want))
		}
	}
}

// TestReopenPoolRefusesAnUnprobedPool pins that a fresh pool answers a read
// before it replaces the old one: a handle whose file is gone keeps its pool
// and reports the probe failure. A handle with no file at all is refused
// outright, and one with no pool opens fresh instead of dereferencing nil.
func TestReopenPoolRefusesAnUnprobedPool(t *testing.T) {
	if _, err := (&DB{}).reopenPool(); err == nil {
		t.Fatal("reopenPool succeeded with no path, want the refusal")
	}
	fresh := filepath.Join(t.TempDir(), "relevo.db")
	if _, err := (&DB{path: fresh}).reopenPool(); err == nil {
		t.Fatal("reopenPool succeeded on a schema-less file, want the probe failure")
	}
}

// TestReopenPoolKeepsAnUnopenableHandle pins the open-failure path: when the
// file cannot open at all, the handle keeps whatever pool it had instead of
// swapping in nothing. A directory is not a database on any engine or user.
func TestReopenPoolKeepsAnUnopenableHandle(t *testing.T) {
	path := t.TempDir()
	d := &DB{path: path}
	if _, err := d.reopenPool(); err == nil {
		t.Fatal("reopenPool opened a directory, want the open failure")
	}
	if d.sqlDB != nil {
		t.Fatal("a failed reopen swapped in a pool it never probed")
	}
}

func TestReopenPoolRefusesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d := directOpen(t, path, Options{})
	old := d.sqlDB
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove the file: %v", err)
	}
	// A fresh open recreates the file; point the handle at the missing one so
	// the probe reads a schema that is not there.
	d2 := &DB{sqlDB: old, path: path, busy: d.busy}
	if _, err := d2.reopenPool(); err == nil {
		t.Fatal("reopenPool succeeded on a missing file, want the probe failure")
	}
}
