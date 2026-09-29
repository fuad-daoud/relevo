package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Config revisions: every config write appends one row in the same
// transaction. A database whose schema predates the table reads as absent.

// RevisionRow is one config_revision row: when a write happened, its label,
// the version it produced, the JSON-path diff and the document snapshot.
type RevisionRow struct {
	Rev      int64
	At       time.Time
	Source   string
	Message  string
	Version  int64
	Changes  []byte // raw JSON array
	Snapshot []byte // raw JSON object
}

// hasRevisions reports whether this database carries the config_revision
// table.
func (d *DB) hasRevisions() bool { return d.have >= 6 }

// RevisionsExist reports whether any revision has been recorded. A database
// without the table reads as empty, so a first write still has a baseline slot.
func (t *Tx) RevisionsExist() (bool, error) {
	var exists bool
	err := t.queryRow(`SELECT EXISTS(SELECT 1 FROM config_revision)`).Scan(&exists)
	if isMissingTable(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("db: revisions exist: %w", mapBusy(err))
	}
	return exists, nil
}

// RevisionInsert inserts one revision row and returns its rev, the table's
// rowid alias.
func (t *Tx) RevisionInsert(r RevisionRow) (int64, error) {
	res, err := t.exec(`INSERT INTO config_revision (at, source, message, version, changes, snapshot) VALUES (?, ?, ?, ?, ?, ?)`,
		formatTime(r.At), r.Source, r.Message, r.Version, string(r.Changes), string(r.Snapshot))
	if err != nil {
		return 0, fmt.Errorf("db: revision insert: %w", mapBusy(err))
	}
	rev, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("db: revision insert: %w", err)
	}
	return rev, nil
}

// Revisions returns revisions newest first; limit <= 0 means every row. The
// snapshot column is left out of the result: a snapshot can be large.
func (d *DB) Revisions(limit int) ([]RevisionRow, error) {
	if !d.hasRevisions() {
		return nil, nil
	}

	q := `SELECT rev, at, source, message, version, changes FROM config_revision ORDER BY rev DESC`
	var args []any
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := d.sqlDB.QueryContext(context.Background(), q, args...)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: revisions: %w", mapBusy(err))
	}
	out, err := collectRows(rows, scanRevision)
	if err != nil {
		return nil, fmt.Errorf("db: revisions: %w", mapBusy(err))
	}
	return out, nil
}

func scanRevision(s rowScanner) (RevisionRow, error) {
	var r RevisionRow
	var at, changes string
	if err := s.Scan(&r.Rev, &at, &r.Source, &r.Message, &r.Version, &changes); err != nil {
		return RevisionRow{}, err
	}
	parsed, err := parseTime(at)
	if err != nil {
		return RevisionRow{}, err
	}
	r.At = parsed
	r.Changes = []byte(changes)
	return r, nil
}

func (d *DB) Revision(rev int64) (RevisionRow, bool, error) {
	if !d.hasRevisions() {
		return RevisionRow{}, false, nil
	}

	var r RevisionRow
	var at, changes, snapshot string
	err := d.sqlDB.QueryRowContext(context.Background(),
		`SELECT rev, at, source, message, version, changes, snapshot FROM config_revision WHERE rev = ?`, rev).
		Scan(&r.Rev, &at, &r.Source, &r.Message, &r.Version, &changes, &snapshot)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return RevisionRow{}, false, nil
	}
	if err != nil {
		return RevisionRow{}, false, fmt.Errorf("db: revision %d: %w", rev, mapBusy(err))
	}
	parsed, err := parseTime(at)
	if err != nil {
		return RevisionRow{}, false, fmt.Errorf("db: revision %d: %w", rev, err)
	}
	r.At = parsed
	r.Changes = []byte(changes)
	r.Snapshot = []byte(snapshot)
	return r, true, nil
}
