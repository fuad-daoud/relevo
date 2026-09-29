package db

import (
	"bytes"
	"testing"
	"time"
)

// TestRoundFilePutGetList pins the contract: a put is readable by (record,
// name) with its exact bytes and its own mtime, list is name-sorted, and a
// second put of the same key replaces the row rather than duplicating it.
func TestRoundFilePutGetList(t *testing.T) {
	d := openTestDB(t)

	id, err := d.RecordPut(testRecord("webshop"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}

	now := time.Now().UTC()
	mtime := time.Date(2026, 9, 24, 10, 11, 12, 123456789, time.UTC)
	body := []byte("003-report.md: the builder's report\n")

	if err := d.Tx(func(tx *Tx) error {
		if err := tx.RoundFilePut(id, "003-report.md", 3, body, mtime, now); err != nil {
			return err
		}
		return tx.RoundFilePut(id, "003-builder.log", 3, []byte("log\n"), mtime, now)
	}); err != nil {
		t.Fatalf("RoundFilePut: %v", err)
	}

	got, gotMTime, ok, err := d.RoundFileGet(id, "003-report.md")
	if err != nil {
		t.Fatalf("RoundFileGet: %v", err)
	}
	if !ok {
		t.Fatal("RoundFileGet(report) found = false, want true")
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body = %q, want %q", got, body)
	}
	if !gotMTime.Equal(mtime) {
		t.Errorf("mtime = %s, want %s", gotMTime, mtime)
	}

	if _, _, ok, err := d.RoundFileGet(id, "003-missing.md"); err != nil || ok {
		t.Errorf("RoundFileGet(missing) = (ok %v, err %v), want (false, nil)", ok, err)
	}

	names, err := d.RoundFileList(id)
	if err != nil {
		t.Fatalf("RoundFileList: %v", err)
	}
	if len(names) != 2 || names[0] != "003-builder.log" || names[1] != "003-report.md" {
		t.Fatalf("RoundFileList = %v, want [003-builder.log 003-report.md]", names)
	}

	next := []byte("rewritten\n")
	if err := d.Tx(func(tx *Tx) error {
		return tx.RoundFilePut(id, "003-report.md", 3, next, mtime, now)
	}); err != nil {
		t.Fatalf("RoundFilePut again: %v", err)
	}
	got, _, _, err = d.RoundFileGet(id, "003-report.md")
	if err != nil {
		t.Fatalf("RoundFileGet again: %v", err)
	}
	if !bytes.Equal(got, next) {
		t.Errorf("body after re-put = %q, want %q", got, next)
	}
	if names, err = d.RoundFileList(id); err != nil || len(names) != 2 {
		t.Fatalf("RoundFileList after re-put = %v (err %v), want 2 rows", names, err)
	}
}

// TestRoundFileListScopedToRecord pins that a sealed file is keyed by the
// binding record: another record's row is invisible, and deleting the record
// takes its sealed files with it.
func TestRoundFileListScopedToRecord(t *testing.T) {
	d := openTestDB(t)

	keepID, err := d.RecordPut(testRecord("keep"))
	if err != nil {
		t.Fatalf("RecordPut(keep): %v", err)
	}
	goneID, err := d.RecordPut(testRecord("gone"))
	if err != nil {
		t.Fatalf("RecordPut(gone): %v", err)
	}

	now := time.Now().UTC()
	if err := d.Tx(func(tx *Tx) error {
		if err := tx.RoundFilePut(keepID, "003-report.md", 3, []byte("keep\n"), now, now); err != nil {
			return err
		}
		return tx.RoundFilePut(goneID, "003-report.md", 3, []byte("gone\n"), now, now)
	}); err != nil {
		t.Fatalf("RoundFilePut: %v", err)
	}

	names, err := d.RoundFileList(keepID)
	if err != nil {
		t.Fatalf("RoundFileList(keep): %v", err)
	}
	if len(names) != 1 {
		t.Fatalf("RoundFileList(keep) = %v, want one row", names)
	}

	if err := d.RecordDelete("", "gone"); err != nil {
		t.Fatalf("RecordDelete(gone): %v", err)
	}
	if names, err = d.RoundFileList(goneID); err != nil || len(names) != 0 {
		t.Fatalf("RoundFileList(gone) after delete = %v (err %v), want none", names, err)
	}
	if _, _, ok, err := d.RoundFileGet(keepID, "003-report.md"); err != nil || !ok {
		t.Errorf("RoundFileGet(keep) = (ok %v, err %v), want (true, nil)", ok, err)
	}
}

// TestRoundFileEmptyBodyRoundTrips pins that an existing empty row reads back
// as a non-nil empty body, so a sealed empty file can be read and put again,
// and that a nil body is stored as an empty blob rather than NULL.
func TestRoundFileEmptyBodyRoundTrips(t *testing.T) {
	d := openTestDB(t)

	id, err := d.RecordPut(testRecord("webshop"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}

	now := time.Now().UTC()
	mtime := time.Date(2026, 9, 24, 10, 11, 12, 0, time.UTC)

	if err := d.Tx(func(tx *Tx) error {
		return tx.RoundFilePut(id, "001-done", 1, []byte{}, mtime, now)
	}); err != nil {
		t.Fatalf("RoundFilePut(empty): %v", err)
	}
	got, _, ok, err := d.RoundFileGet(id, "001-done")
	if err != nil {
		t.Fatalf("RoundFileGet(empty): %v", err)
	}
	if !ok {
		t.Fatal("RoundFileGet(empty) found = false, want true")
	}
	if got == nil {
		t.Fatal("RoundFileGet(empty) body = nil, want a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("RoundFileGet(empty) len = %d, want 0", len(got))
	}

	// Putting the Get's result under another name is the fork's copy loop.
	if err := d.Tx(func(tx *Tx) error {
		return tx.RoundFilePut(id, "002-done", 2, got, mtime, now)
	}); err != nil {
		t.Fatalf("RoundFilePut(round-tripped): %v", err)
	}

	if err := d.Tx(func(tx *Tx) error {
		return tx.RoundFilePut(id, "003-done", 3, nil, mtime, now)
	}); err != nil {
		t.Fatalf("RoundFilePut(nil): %v", err)
	}
	got, _, ok, err = d.RoundFileGet(id, "003-done")
	if err != nil {
		t.Fatalf("RoundFileGet(nil): %v", err)
	}
	if !ok {
		t.Fatal("RoundFileGet(nil) found = false, want true")
	}
	if got == nil {
		t.Fatal("RoundFileGet(nil) body = nil, want a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("RoundFileGet(nil) len = %d, want 0", len(got))
	}
}
