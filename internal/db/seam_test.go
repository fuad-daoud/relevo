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
)

// TestForeignKeysAreOnForEveryPooledConnection pins the per-connection pragma:
// every connection the pool hands out enforces foreign keys, not only the first,
// so a delete cascades from binding_record to its round_file rows.
func TestForeignKeysAreOnForEveryPooledConnection(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	conns := make([]*sql.Conn, 0, 3)
	for i := 0; i < 3; i++ {
		c, err := d.sqlDB.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn %d: %v", i, err)
		}
		conns = append(conns, c)
		t.Cleanup(func() { _ = c.Close() })
	}
	for i, c := range conns {
		var fk int
		if err := c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatalf("connection %d: PRAGMA foreign_keys: %v", i, err)
		}
		if fk != 1 {
			t.Errorf("connection %d: foreign_keys = %d, want 1", i, fk)
		}
	}

	recordID, err := d.RecordPut(testRecord("cascade"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	insertPlainRoundFile(t, d, recordID, "cascade.jsonl", "body\n")

	if _, err := d.sqlDB.ExecContext(ctx, `DELETE FROM binding_record WHERE id = ?`, recordID); err != nil {
		t.Fatalf("delete binding_record: %v", err)
	}
	var n int
	if err := d.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM round_file WHERE record_id = ?`, recordID).Scan(&n); err != nil {
		t.Fatalf("count round_file: %v", err)
	}
	if n != 0 {
		t.Errorf("round_file rows = %d, want 0: the foreign-key cascade is off", n)
	}
}

// TestReadOnlyHandleRefusesWritesOnEveryConnection pins that a read-only handle
// refuses a write on each pooled connection, not only the first.
func TestReadOnlyHandleRefusesWritesOnEveryConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close the writable handle: %v", err)
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = ro.Close() })

	ctx := context.Background()
	conns := make([]*sql.Conn, 0, 3)
	for i := 0; i < 3; i++ {
		c, err := ro.sqlDB.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn %d: %v", i, err)
		}
		conns = append(conns, c)
		t.Cleanup(func() { _ = c.Close() })
	}
	for i, c := range conns {
		if _, err := c.ExecContext(ctx, `INSERT INTO schema_version (version, applied_at) VALUES (99999, '2026-01-01T00:00:00.000Z')`); err == nil {
			t.Errorf("connection %d accepted a write, want a refusal", i)
		}
	}
}

// TestTwoDirectHandlesOnOnePathAreCounted pins the per-path handle count.
func TestTwoDirectHandlesOnOnePathAreCounted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d1, err := Open(path)
	if err != nil {
		t.Fatalf("Open d1: %v", err)
	}
	t.Cleanup(func() { _ = d1.Close() })
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("Open d2: %v", err)
	}
	t.Cleanup(func() { _ = d2.Close() })

	if got := handleCount(path); got != 2 {
		t.Errorf("handleCount with two handles = %d, want 2", got)
	}
	if err := d1.Close(); err != nil {
		t.Fatalf("close d1: %v", err)
	}
	if got := handleCount(path); got != 1 {
		t.Errorf("handleCount after one close = %d, want 1", got)
	}
	if err := d2.Close(); err != nil {
		t.Fatalf("close d2: %v", err)
	}
	if got := handleCount(path); got != 0 {
		t.Errorf("handleCount after both close = %d, want 0", got)
	}
}

// TestVacuumRefusesAServedHandle pins the served guard: an owner's live handle
// must not have its pool swapped out from under its clients.
func TestVacuumRefusesAServedHandle(t *testing.T) {
	d := openTestDB(t)
	_ = NewOwner(d)

	if err := d.Vacuum(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Vacuum on a served handle = %v, want ErrInvalid", err)
	}
}

// TestVacuumRefusesWhileAnotherHandleIsOpen pins the count guard: a second
// direct handle on the same file must not be stranded by the swap.
func TestVacuumRefusesWhileAnotherHandleIsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d1, err := Open(path)
	if err != nil {
		t.Fatalf("Open d1: %v", err)
	}
	t.Cleanup(func() { _ = d1.Close() })
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("Open d2: %v", err)
	}
	t.Cleanup(func() { _ = d2.Close() })

	if err := d1.Vacuum(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Vacuum with two handles = %v, want ErrInvalid", err)
	}
}

// TestVacuumShrinksTheFileAndKeepsTheHandleUsable pins the swap: the file gets
// smaller, and the handle keeps serving the reopened pool.
func TestVacuumShrinksTheFileAndKeepsTheHandleUsable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	recordID, err := d.RecordPut(testRecord("vacuum"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	for i := 0; i < 20; i++ {
		insertPlainRoundFile(t, d, recordID, fmt.Sprintf("f-%02d", i), strings.Repeat("x", 64<<10))
	}
	if _, err := d.sqlDB.Exec(`DELETE FROM round_file`); err != nil {
		t.Fatalf("delete round_file: %v", err)
	}
	if err := d.walCheckpoint(); err != nil {
		t.Fatalf("walCheckpoint: %v", err)
	}

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	if err := d.Vacuum(); err != nil {
		t.Fatalf("Vacuum: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if after.Size() >= before.Size() {
		t.Errorf("Vacuum left %d bytes, want under the %d before it", after.Size(), before.Size())
	}

	if _, err := d.UpsertRepo(Repo{OriginURL: ptr("https://example.test/after.git")}); err != nil {
		t.Fatalf("UpsertRepo after Vacuum: %v", err)
	}
	var n int
	if err := d.sqlDB.QueryRow(`SELECT count(*) FROM repo`).Scan(&n); err != nil {
		t.Fatalf("count repo: %v", err)
	}
	if n != 1 {
		t.Errorf("repo rows = %d, want 1: the handle must keep working after the swap", n)
	}
}
