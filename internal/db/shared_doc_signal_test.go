package db

import (
	"testing"
	"time"
)

func putConfig(t *testing.T, d *DB, body string) {
	t.Helper()
	if err := d.Tx(func(tx *Tx) error {
		return tx.ConfigPut("policy", []byte(body), time.Now())
	}); err != nil {
		t.Fatalf("ConfigPut: %v", err)
	}
}

// TestWriteDuringExportKeepsTheSignal pins that a config write landing between
// the export's signal read and its clear survives the clear. Both writes run in
// one transaction so they fall in the same millisecond.
func TestWriteDuringExportKeepsTheSignal(t *testing.T) {
	d := openTestDB(t)
	err := d.Tx(func(tx *Tx) error {
		if err := tx.ConfigPut("policy", []byte(`{"a":1}`), time.Now()); err != nil {
			return err
		}
		sig := DocSignal{Doc: "config"}
		if err := tx.queryRow(`SELECT dirtied_at FROM shared_outbox WHERE doc = 'config'`).Scan(&sig.DirtiedAt); err != nil {
			return err
		}
		if err := tx.ConfigPut("policy", []byte(`{"a":2}`), time.Now()); err != nil {
			return err
		}
		return tx.clearDocSignal(sig)
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	if after, err := d.DocSignals(); err != nil || len(after) != 1 {
		t.Fatalf("the concurrent write's signal was cleared: %v, %v", after, err)
	}
}

// TestExportWithoutConcurrentWriteClearsTheSignal pins that an export no write
// raced still empties the outbox.
func TestExportWithoutConcurrentWriteClearsTheSignal(t *testing.T) {
	d := openTestDB(t)
	putConfig(t, d, `{"a":1}`)
	sigs, err := d.DocSignals()
	if err != nil || len(sigs) != 1 {
		t.Fatalf("DocSignals = %v, %v", sigs, err)
	}
	if err := d.RecordDocExport(sigs[0], nil); err != nil {
		t.Fatalf("RecordDocExport: %v", err)
	}
	if after, err := d.DocSignals(); err != nil || len(after) != 0 {
		t.Fatalf("signals after a clean export = %v, %v; want none", after, err)
	}
}
