package db

import (
	"path/filepath"
	"testing"
	"time"
)

// TestRecordDeleteLogsAnOwnedDeleteForEveryChild pins that a deleted binding's
// events and sealed files reach the outbox as deletes this machine owns. The
// exporter sends only owned entries, so a child delete logged without an
// origin would leave the child in the log after its parent is gone.
func TestRecordDeleteLogsAnOwnedDeleteForEveryChild(t *testing.T) {
	d := openOriginDB(t)
	id, err := d.RecordPut(testRecord("alpha"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	now := time.Date(2026, time.October, 17, 12, 0, 0, 0, time.UTC)
	if err := d.Tx(func(tx *Tx) error {
		for seq := 1; seq <= 2; seq++ {
			if err := tx.EventAppend(id, testEvent(seq)); err != nil {
				return err
			}
		}
		return tx.RoundFilePut(id, "001-report.md", 1, []byte("report\n"), now, now)
	}); err != nil {
		t.Fatalf("seed children: %v", err)
	}
	if err := d.TruncateOutbox(); err != nil {
		t.Fatalf("TruncateOutbox: %v", err)
	}

	if err := d.RecordDelete("", "alpha"); err != nil {
		t.Fatalf("RecordDelete: %v", err)
	}

	wantOwnedDeletes(t, d, map[string]int{"binding_record": 1, "binding_event": 2, "round_file": 1})
}

// TestChainDeleteLogsAnOwnedDeleteForItsTrace pins the same for a chain's
// trace, the one chain child that hangs off the chain by a foreign key.
func TestChainDeleteLogsAnOwnedDeleteForItsTrace(t *testing.T) {
	d := openOriginDB(t)
	c := testChain("x")
	putChain(t, d, c)
	if err := d.Tx(func(tx *Tx) error {
		for range 2 {
			if err := tx.ChainEventAppend(c.ID, ChainEventRow{TS: time.Now().UTC(), Phase: "build", Event: "sent"}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed trace: %v", err)
	}
	if err := d.TruncateOutbox(); err != nil {
		t.Fatalf("TruncateOutbox: %v", err)
	}

	if err := d.Tx(func(tx *Tx) error { return tx.ChainDelete("", "x") }); err != nil {
		t.Fatalf("ChainDelete: %v", err)
	}

	wantOwnedDeletes(t, d, map[string]int{"chains": 1, "chain_event": 2})
}

// wantOwnedDeletes drains the outbox and checks that it holds exactly want
// deletes per table, each attributed to this machine.
func wantOwnedDeletes(t *testing.T, d *DB, want map[string]int) {
	t.Helper()
	drained, err := d.DrainOutbox(1000)
	if err != nil {
		t.Fatalf("DrainOutbox: %v", err)
	}
	got := map[string]int{}
	for _, e := range drained {
		if e.Op != "delete" {
			continue
		}
		if !e.Origin.Valid || e.Origin.String != d.Origin() {
			t.Errorf("%s %s: delete logged with origin %v, want %q", e.Table, e.PK, e.Origin, d.Origin())
			continue
		}
		got[e.Table]++
	}
	for table, n := range want {
		if got[table] != n {
			t.Errorf("%s: %d owned deletes in the outbox, want %d", table, got[table], n)
		}
	}
}

// openOriginDB opens a split installation with an origin of its own, as every
// machine that syncs has, so an entry with no origin cannot pass for an owned
// one.
func openOriginDB(t *testing.T) *DB {
	t.Helper()
	d, err := OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
