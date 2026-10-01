package db

import (
	"bytes"
	"context"
	"testing"
)

// TestInvalidUTF8TextIsRepairedOnWrite pins the write-side repair: a string
// holding invalid UTF-8 is rewritten to the replacement character before either
// engine sees it, because Turso refuses to write such a value and cannot read it
// back. strings.ToValidUTF8 replaces a run of invalid bytes with one
// replacement, so ff fe 41 becomes one replacement followed by A.
func TestInvalidUTF8TextIsRepairedOnWrite(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.sqlDB.Exec(`CREATE TABLE t (body TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	invalid := string([]byte{0xff, 0xfe, 0x41})
	if _, err := d.sqlDB.Exec(`INSERT INTO t (body) VALUES (?)`, invalid); err != nil {
		t.Fatalf("insert invalid text: %v", err)
	}

	var got string
	if err := d.sqlDB.QueryRow(`SELECT body FROM t`).Scan(&got); err != nil {
		t.Fatalf("select: %v", err)
	}
	if want := "\uFFFDA"; got != want {
		t.Errorf("stored text = %q, want %q", got, want)
	}
}

// TestRepairInvalidTextRewritesOnlyInvalidValues pins that the conversion repair
// touches no value it does not have to: a valid string, a string holding a NUL,
// and a BLOB are byte-identical after it runs, and no column reports a rewrite.
func TestRepairInvalidTextRewritesOnlyInvalidValues(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.sqlDB.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, txt TEXT, blob_col BLOB)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	blob := []byte{0xff, 0xfe, 0x41}
	nul := "A\x00B"
	if _, err := d.sqlDB.Exec(`INSERT INTO t (id, txt, blob_col) VALUES (?, ?, ?)`, 1, "hello", blob); err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	if _, err := d.sqlDB.Exec(`INSERT INTO t (id, txt, blob_col) VALUES (?, ?, ?)`, 2, nul, []byte{0x00, 0x41}); err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	conn, err := d.sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	repairs, err := repairInvalidText(conn)
	if err != nil {
		t.Fatalf("repairInvalidText: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("conn close: %v", err)
	}
	for _, r := range repairs {
		if r.Repaired != 0 {
			t.Errorf("repair rewrote %d values in %s.%s, want none", r.Repaired, r.Table, r.Column)
		}
	}

	var txt string
	if err := d.sqlDB.QueryRow(`SELECT txt FROM t WHERE id = 1`).Scan(&txt); err != nil {
		t.Fatalf("read txt: %v", err)
	}
	if txt != "hello" {
		t.Errorf("txt = %q, want %q", txt, "hello")
	}
	var nulBack string
	if err := d.sqlDB.QueryRow(`SELECT txt FROM t WHERE id = 2`).Scan(&nulBack); err != nil {
		t.Fatalf("read NUL text: %v", err)
	}
	if nulBack != nul {
		t.Errorf("NUL text = %q, want %q", nulBack, nul)
	}
	var blobBack []byte
	if err := d.sqlDB.QueryRow(`SELECT blob_col FROM t WHERE id = 1`).Scan(&blobBack); err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if !bytes.Equal(blobBack, blob) {
		t.Errorf("blob = %x, want %x", blobBack, blob)
	}
}
