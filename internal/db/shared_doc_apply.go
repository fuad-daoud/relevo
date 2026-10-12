package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The importer's side of the machine-local doc bookkeeping. The methods on Tx
// run inside the local-file transaction that applies or parks a batch's doc
// entries, so what a doc entry changed and what is remembered about it commit
// together.

// DocApplied is the newest (origin, stamp) applied for one key.
type DocApplied struct {
	Origin string
	Stamp  string
}

// PendingDoc is one doc entry parked while this machine held no key.
type PendingDoc struct {
	Origin string
	Seq    int
	// Entry is the log entry exactly as it arrived, because the signature that
	// is checked later covers fields a stripped copy would not carry.
	Entry string
}

// DocApplied returns the newest applied state of one key; found is false when
// none has been applied.
func (t *Tx) DocApplied(doc, key string) (DocApplied, bool, error) {
	var got DocApplied
	err := t.queryRow(`SELECT origin, stamp FROM shared_applied WHERE doc = ? AND key = ?`, doc, key).
		Scan(&got.Origin, &got.Stamp)
	if errors.Is(err, sql.ErrNoRows) {
		return DocApplied{}, false, nil
	}
	if err != nil {
		return DocApplied{}, false, fmt.Errorf("db: doc applied %s %s: %w", doc, key, mapBusy(err))
	}
	return got, true, nil
}

// SetDocApplied records the newest applied state of one key.
func (t *Tx) SetDocApplied(doc, key, origin, stamp string) error {
	if _, err := t.exec(`INSERT OR REPLACE INTO shared_applied (doc, key, origin, stamp)
		VALUES (?, ?, ?, ?)`, doc, key, origin, stamp); err != nil {
		return fmt.Errorf("db: doc applied %s %s: %w", doc, key, mapBusy(err))
	}
	return nil
}

// SetDocExported records that the key's value on this machine is the one the
// log carries at (origin, stamp), so the exporter does not send it back.
func (t *Tx) SetDocExported(doc, key, origin, stamp, digest string) error {
	if _, err := t.exec(`INSERT OR REPLACE INTO shared_export_state (doc, key, origin, stamp, digest)
		VALUES (?, ?, ?, ?, ?)`, doc, key, origin, stamp, digest); err != nil {
		return fmt.Errorf("db: doc export state %s %s: %w", doc, key, mapBusy(err))
	}
	return nil
}

// ClearDocExported forgets the key's export state, for a key an applied
// tombstone removed.
func (t *Tx) ClearDocExported(doc, key string) error {
	if _, err := t.exec(`DELETE FROM shared_export_state WHERE doc = ? AND key = ?`, doc, key); err != nil {
		return fmt.Errorf("db: doc export state %s %s: %w", doc, key, mapBusy(err))
	}
	return nil
}

// ParkDoc holds a doc entry until a key can verify it. A second park of the
// same (origin, seq) replaces the first, so a redelivered batch parks nothing
// new.
func (t *Tx) ParkDoc(origin string, seq int, entry, at string) error {
	if _, err := t.exec(`INSERT OR REPLACE INTO shared_pending (origin, seq, entry, parked_at)
		VALUES (?, ?, ?, ?)`, origin, seq, entry, at); err != nil {
		return fmt.Errorf("db: park doc entry %s %d: %w", origin, seq, mapBusy(err))
	}
	return nil
}

// UnparkDoc removes a parked entry once it has been applied or refused.
func (t *Tx) UnparkDoc(origin string, seq int) error {
	if _, err := t.exec(`DELETE FROM shared_pending WHERE origin = ? AND seq = ?`, origin, seq); err != nil {
		return fmt.Errorf("db: unpark doc entry %s %d: %w", origin, seq, mapBusy(err))
	}
	return nil
}

// PendingDocs returns every parked doc entry in the order they arrived.
func (d *DB) PendingDocs() ([]PendingDoc, error) {
	rows, err := d.LocalOrSelf().sqlDB.QueryContext(context.Background(),
		`SELECT origin, seq, entry FROM shared_pending ORDER BY parked_at, origin, seq`)
	if err != nil {
		return nil, fmt.Errorf("db: pending docs: %w", mapBusy(err))
	}
	defer func() { _ = rows.Close() }()
	var out []PendingDoc
	for rows.Next() {
		var p PendingDoc
		if err := rows.Scan(&p.Origin, &p.Seq, &p.Entry); err != nil {
			return nil, fmt.Errorf("db: pending docs: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
