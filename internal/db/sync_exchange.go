package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The exchange seam the sync log is built on: the local-only import marks, the
// outbox drain and the reads that turn a drained entry into the row's current
// state. Every read and write here is scoped to one *Tx so a caller can hold the
// outbox contents and the rows they name in a single consistent read, and every
// *DB form opens that transaction itself.

// ImportMark returns how far origin's entries have been applied: the sequence
// number of the last entry applied from that origin's log. found is false when
// origin has no mark, which is what a machine that has never imported from it
// sees and is different from a mark of zero.
//
// A mark is absent rather than zero on a file whose schema predates the table:
// there is nothing applied either way, and the next write creates the row.
func (d *DB) ImportMark(origin string) (int, bool, error) {
	return importMark(context.Background(), d.sqlDB, origin)
}

func (t *Tx) ImportMark(origin string) (int, bool, error) { return importMark(t.ctx, t.conn, origin) }

func importMark(ctx context.Context, q queryer, origin string) (int, bool, error) {
	var seq int
	err := q.QueryRowContext(ctx, `SELECT seq FROM sync_import_mark WHERE origin = ?`, origin).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("db: import mark %s: %w", origin, mapBusy(err))
	}
	return seq, true, nil
}

// SetImportMark records that every entry up to and including seq has been
// applied from origin's log. It replaces any earlier mark for the origin in
// place, so one origin keeps one mark however many rounds have written it.
//
// It is written in the same transaction as the batch that moved the mark, which
// is the property that makes a restore from a backup consistent: the file comes
// back with the rows and the marks in the same state, so the entries that
// arrived after the backup are re-applied rather than skipped.
func (d *DB) SetImportMark(origin string, seq int) error {
	return d.Tx(func(t *Tx) error { return t.SetImportMark(origin, seq) })
}

func (t *Tx) SetImportMark(origin string, seq int) error {
	// OR REPLACE is right here and wrong for a shared row. The mark table has
	// no trigger, no foreign key and nothing that references it, so replacing
	// the row deletes one mark row and writes another; replacing a shared row
	// instead deletes that row first and cascades to its children, which is why
	// the importer writes those with a conflict clause instead.
	if _, err := t.exec(`INSERT OR REPLACE INTO sync_import_mark (origin, seq) VALUES (?, ?)`, origin, seq); err != nil {
		return fmt.Errorf("db: set import mark %s: %w", origin, mapBusy(err))
	}
	return nil
}