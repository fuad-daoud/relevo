package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The machine-local bookkeeping of the exporter of a shared doc. Every method
// binds the machine-local file itself, so a caller holding the shared handle
// cannot read or write another file's copy of these tables.

// DocSignal is one doc whose items may have changed since the last export.
type DocSignal struct {
	Doc string
	// DirtiedAt is the signal's own text. It is compared on clear, so a write
	// that lands while an export runs survives it.
	DirtiedAt string
}

// DocExportRow is what the exporter last put on the log for one key.
type DocExportRow struct {
	Origin string
	Stamp  string
	Digest string
}

// DocExportWrite is one key the exporter just appended.
type DocExportWrite struct {
	Key    string
	Origin string
	Stamp  string
	Digest string
	// Tombstone marks a removal: the key leaves the export state, because
	// there is nothing left to diff, and stays in the applied state, so an
	// older entry still loses to it.
	Tombstone bool
}

// DocSignals returns every doc with a pending signal, sorted.
func (d *DB) DocSignals() ([]DocSignal, error) {
	rows, err := d.LocalOrSelf().sqlDB.QueryContext(context.Background(),
		`SELECT doc, dirtied_at FROM shared_outbox ORDER BY doc`)
	if err != nil {
		return nil, fmt.Errorf("db: doc signals: %w", mapBusy(err))
	}
	defer func() { _ = rows.Close() }()
	var out []DocSignal
	for rows.Next() {
		var s DocSignal
		if err := rows.Scan(&s.Doc, &s.DirtiedAt); err != nil {
			return nil, fmt.Errorf("db: doc signals: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DocExportState returns what was last exported for each key of doc.
func (d *DB) DocExportState(doc string) (map[string]DocExportRow, error) {
	rows, err := d.LocalOrSelf().sqlDB.QueryContext(context.Background(),
		`SELECT key, origin, stamp, digest FROM shared_export_state WHERE doc = ?`, doc)
	if err != nil {
		return nil, fmt.Errorf("db: doc export state %s: %w", doc, mapBusy(err))
	}
	defer func() { _ = rows.Close() }()
	out := map[string]DocExportRow{}
	for rows.Next() {
		var key string
		var row DocExportRow
		if err := rows.Scan(&key, &row.Origin, &row.Stamp, &row.Digest); err != nil {
			return nil, fmt.Errorf("db: doc export state %s: %w", doc, err)
		}
		out[key] = row
	}
	return out, rows.Err()
}

// RecordDocExport records an export of doc in one transaction: the state of
// every key written, the applied state they stand for, and the clearing of the
// signal the export answered. The signal is cleared only if it is still the one
// the export read.
func (d *DB) RecordDocExport(sig DocSignal, writes []DocExportWrite) error {
	return d.LocalOrSelf().Tx(func(t *Tx) error {
		for _, w := range writes {
			if err := t.recordDocWrite(sig.Doc, w); err != nil {
				return err
			}
		}
		return t.clearDocSignal(sig)
	})
}

func (t *Tx) clearDocSignal(sig DocSignal) error {
	if _, err := t.exec(`DELETE FROM shared_outbox WHERE doc = ? AND dirtied_at = ?`,
		sig.Doc, sig.DirtiedAt); err != nil {
		return fmt.Errorf("db: clear doc signal %s: %w", sig.Doc, mapBusy(err))
	}
	return nil
}

func (t *Tx) recordDocWrite(doc string, w DocExportWrite) error {
	if w.Tombstone {
		if _, err := t.exec(`DELETE FROM shared_export_state WHERE doc = ? AND key = ?`, doc, w.Key); err != nil {
			return fmt.Errorf("db: doc export state %s %s: %w", doc, w.Key, mapBusy(err))
		}
	} else if _, err := t.exec(`INSERT OR REPLACE INTO shared_export_state (doc, key, origin, stamp, digest)
		VALUES (?, ?, ?, ?, ?)`, doc, w.Key, w.Origin, w.Stamp, w.Digest); err != nil {
		return fmt.Errorf("db: doc export state %s %s: %w", doc, w.Key, mapBusy(err))
	}
	if _, err := t.exec(`INSERT OR REPLACE INTO shared_applied (doc, key, origin, stamp)
		VALUES (?, ?, ?, ?)`, doc, w.Key, w.Origin, w.Stamp); err != nil {
		return fmt.Errorf("db: doc applied %s %s: %w", doc, w.Key, mapBusy(err))
	}
	return nil
}

// ClearDocExportState forgets what was exported and every local write stamp:
// config that predates a join is the machine's starting point, not an edit
// made against the shared config. So the next export of each doc
// that had state re-emits every key. Each such doc is signalled in the same
// transaction, because an export only looks at a doc it has been told changed.
func (d *DB) ClearDocExportState() error {
	return d.LocalOrSelf().Tx(func(t *Tx) error {
		if _, err := t.exec(`INSERT OR REPLACE INTO shared_outbox (doc, dirtied_at)
			SELECT DISTINCT doc, ? FROM shared_export_state`, formatTime(time.Now())); err != nil {
			return fmt.Errorf("db: signal docs: %w", mapBusy(err))
		}
		if _, err := t.exec(`DELETE FROM shared_export_state`); err != nil {
			return fmt.Errorf("db: clear doc export state: %w", mapBusy(err))
		}
		if _, err := t.exec(`DELETE FROM config_write_stamp`); err != nil {
			return fmt.Errorf("db: clear config write stamps: %w", mapBusy(err))
		}
		return nil
	})
}

// ConfigUpdatedAt is when the config_doc section name was last written; ok is
// false when it is absent.
func (d *DB) ConfigUpdatedAt(name string) (time.Time, bool, error) {
	if !d.hasConfig() {
		return time.Time{}, false, nil
	}
	var text string
	err := d.sqlDB.QueryRowContext(context.Background(),
		`SELECT updated_at FROM config_doc WHERE name = ?`, name).Scan(&text)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("db: config updated_at %s: %w", name, mapBusy(err))
	}
	at, err := parseTime(text)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("db: config updated_at %s: %w", name, err)
	}
	return at, true, nil
}
