package db

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// openTermTestDB opens a handle whose origin is an installation id, because the
// term and fence queries scope to it: a row written by another installation is
// not this machine's fence.
func openTermTestDB(t *testing.T, origin string) *DB {
	t.Helper()
	d, err := OpenWith(filepath.Join(t.TempDir(), "relevo.db"), Options{Origin: origin})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func termRowCount(t *testing.T, d *DB) int {
	t.Helper()
	var n int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM sync_binding_term`).Scan(&n); err != nil {
		t.Fatalf("count terms: %v", err)
	}
	return n
}

// TestTxOrigin pins the installation id a transaction scopes to, which every
// other query in this file depends on.
func TestTxOrigin(t *testing.T) {
	d := openTermTestDB(t, "m1")
	err := d.Tx(func(tx *Tx) error {
		if got := tx.Origin(); got != "m1" {
			t.Errorf("Tx.Origin() = %q, want m1", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}

// A term records the sequence a writer's entries stay valid through. It is read
// through both the handle and a transaction, and an absent one is not an error:
// the importer asks the question before the hand-over that would answer it.
func TestTermRoundTrip(t *testing.T) {
	d := openTermTestDB(t, "m1")
	err := d.Tx(func(tx *Tx) error {
		if upto, ro, rs, found, err := tx.Term("b1", "m1"); err != nil || found || upto != 0 || ro != "" || rs != 0 {
			t.Errorf("Tx.Term absent = (%d, %q, %d, %t, %v), want empty", upto, ro, rs, found, err)
		}
		if err := tx.SetTerm("b1", "m1", 12); err != nil {
			return err
		}
		if err := tx.SetTerm("b2", "m1", 3); err != nil {
			return err
		}
		upto, ro, rs, found, err := tx.Term("b1", "m1")
		if err != nil || !found || upto != 12 || ro != "" || rs != 0 {
			t.Errorf("Tx.Term = (%d, %q, %d, %t, %v), want (12, empty, 0, true, nil)", upto, ro, rs, found, err)
		}
		// A second write moves the sequence rather than adding a row.
		if err := tx.SetTerm("b1", "m1", 20); err != nil {
			return err
		}
		upto, _, _, found, err = tx.Term("b1", "m1")
		if err != nil || !found || upto != 20 {
			t.Errorf("Tx.Term after upsert = (%d, %t, %v), want (20, true, nil)", upto, found, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	if n := termRowCount(t, d); n != 2 {
		t.Errorf("term rows = %d, want 2 (upsert must not add a row)", n)
	}
	upto, _, _, found, err := d.Term("b1", "m1")
	if err != nil || !found || upto != 20 {
		t.Errorf("DB.Term = (%d, %t, %v), want (20, true, nil)", upto, found, err)
	}
	if _, _, _, found, err := d.Term("b1", "m9"); err != nil || found {
		t.Errorf("DB.Term other writer = (found %t, %v), want not found", found, err)
	}
}

// A regrant is the entry that gave a writer its partition back, and the importer
// compares an entry against both halves of it.
func TestTermRegrant(t *testing.T) {
	d := openTermTestDB(t, "m1")
	err := d.Tx(func(tx *Tx) error {
		_, err := tx.exec(`INSERT INTO sync_binding_term (root_id, writer, upto, regrant_origin, regrant_seq)
			VALUES ('b1', 'm1', 20, 'm1', 30)`)
		return err
	})
	if err != nil {
		t.Fatalf("seed regrant: %v", err)
	}
	upto, ro, rs, found, err := d.Term("b1", "m1")
	if err != nil || !found || upto != 20 || ro != "m1" || rs != 30 {
		t.Errorf("DB.Term regrant = (%d, %q, %d, %t, %v), want (20, m1, 30, true, nil)", upto, ro, rs, found, err)
	}
}

// Deleting a term is how a grant to a writer clears the fence it once carried,
// so deleting an absent term is a no-op rather than a failure.
func TestDeleteTerm(t *testing.T) {
	d := openTermTestDB(t, "m1")
	err := d.Tx(func(tx *Tx) error {
		if err := tx.SetTerm("b1", "m1", 5); err != nil {
			return err
		}
		if err := tx.DeleteTerm("b1", "m1"); err != nil {
			return err
		}
		if _, _, _, found, err := tx.Term("b1", "m1"); err != nil || found {
			t.Errorf("Term after delete = (found %t, %v), want not found", found, err)
		}
		return tx.DeleteTerm("b1", "m1")
	})
	if err != nil {
		t.Fatalf("delete absent term: %v", err)
	}
}

// sync_writer_after carries the (origin, seq) a root's whole tree waits on, so
// it is read for the root of whatever table the entry names.
func TestWriterAfter(t *testing.T) {
	d := openTermTestDB(t, "m1")
	err := d.Tx(func(tx *Tx) error {
		for _, stmt := range []string{
			`INSERT INTO binding (id, origin, writer, name, cwd, created_at, builder_mode, ingest_source)
			 VALUES ('b1', 'm1', 'm1', 'b1', '/w', 't', 'headless', '')`,
			`INSERT INTO round (id, binding_id, number, started_at, outcome, switches)
			 VALUES ('rd1', 'b1', 1, 't', 'green', 0)`,
		} {
			if _, err := tx.exec(stmt); err != nil {
				return err
			}
		}
		if origin, seq, found, err := tx.WriterAfter("round", `["rd1"]`); err != nil || found || origin != "" || seq != 0 {
			t.Errorf("WriterAfter before write = (%q, %d, %t, %v), want empty", origin, seq, found, err)
		}
		if err := tx.SetWriterAfter("b1", "m0", 42); err != nil {
			return err
		}
		if err := tx.SetWriterAfter("b1", "m1", 44); err != nil {
			return err
		}
		origin, seq, found, err := tx.WriterAfter("round", `["rd1"]`)
		if err != nil || !found || origin != "m1" || seq != 44 {
			t.Errorf("Tx.WriterAfter child = (%q, %d, %t, %v), want (m1, 44, true, nil)", origin, seq, found, err)
		}
		if _, _, found, err := tx.WriterAfter("round", `["nope"]`); err != nil || found {
			t.Errorf("Tx.WriterAfter unknown root = (found %t, %v), want not found", found, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	origin, seq, found, err := d.WriterAfter("binding", `["b1"]`)
	if err != nil || !found || origin != "m1" || seq != 44 {
		t.Errorf("DB.WriterAfter = (%q, %d, %t, %v), want (m1, 44, true, nil)", origin, seq, found, err)
	}
	if _, _, found, err := d.WriterAfter("repo", `["r1"]`); err != nil || found {
		t.Errorf("DB.WriterAfter on a rootless table = (found %t, %v), want not found", found, err)
	}
}

// sync_fenced_change records what a takeover dropped from this machine's export,
// keyed by the row, so a second pass replaces the row rather than piling up.
func TestRecordFencedChange(t *testing.T) {
	d := openTermTestDB(t, "m1")
	at := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	if err := d.RecordFencedChange("b1", "round", `["rd1"]`, at); err != nil {
		t.Fatalf("RecordFencedChange: %v", err)
	}
	if err := d.RecordFencedChange("b1", "round", `["rd1"]`, at.Add(time.Hour)); err != nil {
		t.Fatalf("RecordFencedChange again: %v", err)
	}
	var n int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM sync_fenced_change WHERE root_id = 'b1'`).Scan(&n); err != nil {
		t.Fatalf("count fenced: %v", err)
	}
	if n != 1 {
		t.Errorf("fenced rows = %d, want 1 (the key replaces)", n)
	}

	err := d.Tx(func(tx *Tx) error {
		return tx.RecordFencedChange("b2", "round", `["rd2"]`, at)
	})
	if err != nil {
		t.Fatalf("Tx.RecordFencedChange: %v", err)
	}
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM sync_fenced_change WHERE root_id = 'b2'`).Scan(&n); err != nil {
		t.Fatalf("count fenced b2: %v", err)
	}
	if n != 1 {
		t.Errorf("fenced rows for b2 = %d, want 1", n)
	}
}

// A root is fenced against this machine when it carries a takeover naming it as
// the old writer, or when this machine's own term row survives: the term is what
// a hand-over this machine applied before it lost the binding leaves behind.
func TestIsTakeoverFenced(t *testing.T) {
	seed := func(stmts ...string) *DB {
		d := openTermTestDB(t, "m1")
		err := d.Tx(func(tx *Tx) error {
			for _, stmt := range stmts {
				if _, err := tx.exec(stmt); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		return d
	}
	left := `INSERT INTO sync_binding_left (root_id, owner, name, to_writer, kind, at)
		VALUES ('%s', 'm1', '%s', 'm2', '%s', 't')`

	cases := []struct {
		name  string
		stmts []string
		root  string
		want  bool
	}{
		{"takeover names this machine", []string{fmt.Sprintf(left, "b1", "b1", "takeover")}, "b1", true},
		{"a handover is not a takeover fence", []string{fmt.Sprintf(left, "b1", "b1", "handover")}, "b1", false},
		{"no row for the root", nil, "b1", false},
		{"another installation's term is not this one's fence",
			[]string{`INSERT INTO sync_binding_term (root_id, writer, upto) VALUES ('b1', 'm2', 9)`}, "b1", false},
		{"this machine's own term fences the root",
			[]string{`INSERT INTO sync_binding_term (root_id, writer, upto) VALUES ('b1', 'm1', 9)`}, "b1", true},
	}
	for _, c := range cases {
		d := seed(c.stmts...)
		got, err := d.IsTakeoverFenced(c.root)
		if err != nil || got != c.want {
			t.Errorf("%s: DB.IsTakeoverFenced = (%t, %v), want (%t, nil)", c.name, got, err, c.want)
		}
		err = d.Tx(func(tx *Tx) error {
			got, err := tx.IsTakeoverFenced(c.root)
			if err != nil || got != c.want {
				t.Errorf("%s: Tx.IsTakeoverFenced = (%t, %v), want (%t, nil)", c.name, got, err, c.want)
			}
			return nil
		})
		if err != nil {
			t.Errorf("%s: tx: %v", c.name, err)
		}
	}
}

// A hand-over onto a live local binding of the same (owner, name) is refused by
// the unique index, and the importer asks this question before it writes. The
// row the hand-over itself names is excluded, or every hand-over would collide
// with its own row.
func TestHasLiveBindingRecord(t *testing.T) {
	d := openTermTestDB(t, "m1")
	err := d.Tx(func(tx *Tx) error {
		if _, err := tx.exec(`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json, created_at, updated_at, origin, writer)
			VALUES ('rec1', 'o1', 'proj', 'open', 1, '/w', '{}', 't', 't', 'm1', 'm2')`); err != nil {
			return err
		}
		for _, c := range []struct {
			writer, owner, name, exclude string
			want                         bool
		}{
			{"m2", "o1", "proj", "other", true},
			{"m2", "o1", "proj", "rec1", false},
			{"m3", "o1", "proj", "other", false},
			{"m2", "o2", "proj", "other", false},
			{"m2", "o1", "other", "other", false},
		} {
			got, err := tx.HasLiveBindingRecord(c.writer, c.owner, c.name, c.exclude)
			if err != nil || got != c.want {
				t.Errorf("HasLiveBindingRecord(%q, %q, %q, %q) = (%t, %v), want (%t, nil)",
					c.writer, c.owner, c.name, c.exclude, got, err, c.want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}

// An archived record is not a live one, so it neither blocks a hand-over nor is
// found by the writer-scoped read.
func TestHasLiveBindingRecordIgnoresArchived(t *testing.T) {
	d := openTermTestDB(t, "m1")
	err := d.Tx(func(tx *Tx) error {
		if _, err := tx.exec(`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json, created_at, updated_at, origin, writer, archived_at)
			VALUES ('rec1', 'o1', 'proj', 'open', 1, '/w', '{}', 't', 't', 'm1', 'm2', 't')`); err != nil {
			return err
		}
		got, err := tx.HasLiveBindingRecord("m2", "o1", "proj", "other")
		if err != nil || got {
			t.Errorf("HasLiveBindingRecord on an archived row = (%t, %v), want (false, nil)", got, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}

// DiscardOutboxSince removes what one import statement queued for this machine,
// and only what it queued: a row the drain had already taken is below the
// sequence the statement read, so it stays.
func TestDiscardOutboxSinceSparesEarlierAndForeignRows(t *testing.T) {
	d := openTermTestDB(t, "m1")
	err := d.Tx(func(tx *Tx) error {
		for _, stmt := range []string{
			`INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round', '["rd1"]', 'update', 'm1')`,
			`INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round', '["rd2"]', 'update', 'm1')`,
			`INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round', '["rd3"]', 'update', 'm2')`,
			`INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round', '["rd4"]', 'update', '')`,
		} {
			if _, err := tx.exec(stmt); err != nil {
				return err
			}
		}
		max, err := tx.MaxOutboxSeq()
		if err != nil {
			return err
		}
		// The statement below queues one more row for this machine.
		if _, err := tx.exec(`INSERT INTO sync_outbox (tbl, pk, op, origin) VALUES ('round', '["rd5"]', 'insert', 'm1')`); err != nil {
			return err
		}
		if err := tx.DiscardOutboxSince("round", `["rd5"]`, max); err != nil {
			return err
		}
		var n int
		if err := tx.conn.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM sync_outbox WHERE pk = '["rd5"]'`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("rows for rd5 = %d, want 0", n)
		}
		if err := tx.conn.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM sync_outbox`).Scan(&n); err != nil {
			return err
		}
		if n != 4 {
			t.Errorf("outbox rows = %d, want 4 (only rd5 was discarded)", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}
