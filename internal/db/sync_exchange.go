package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The exchange seam the sync log is built on: the local-only import marks, the
// outbox drain and the reads that turn a drained entry into the row's current
// state. Every read and write here is scoped to one *Tx, so a caller holds the
// outbox contents and the rows they name in a single consistent read.

// importMarkMigration is the schema version that creates sync_import_mark. It is
// named so a test can stand up a file as the build before it left one.
const importMarkMigration = 23

// ImportMark returns how far origin's entries have been applied: the sequence
// number of the last entry applied from that origin's log. found is false when
// origin has no mark, which differs from a mark of zero.
//
// A mark is absent rather than zero on a file whose schema lacks the table:
// nothing is applied either way, and the next write creates the row.
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
// applied from origin's log, replacing any earlier mark for the origin in place.
//
// The mark only ever moves forward: a sequence at or below the one stored is
// not written at all, so a replayed batch or an out-of-order pull cannot rewind
// what this file applied and have old entries offered again as new. The guard is
// in the statement rather than in the caller, so every path that persists a mark
// gets it.
//
// It is written in the same transaction as the batch that moved the mark, so a
// restore from a backup comes back with rows and marks in the same state, and
// entries that arrived after the backup are re-applied rather than skipped.
func (d *DB) SetImportMark(origin string, seq int) error {
	return d.Tx(func(t *Tx) error { return t.SetImportMark(origin, seq) })
}

func (t *Tx) SetImportMark(origin string, seq int) error {
	// An upsert rather than OR REPLACE: either form leaves one mark row per
	// origin, and only the upsert refuses to move backwards. Replacing a shared
	// row instead would delete it first and cascade to its children, which is why
	// the importer writes those with a conflict clause instead.
	if _, err := t.exec(`INSERT INTO sync_import_mark (origin, seq) VALUES (?, ?)
		ON CONFLICT(origin) DO UPDATE SET seq = excluded.seq
		WHERE excluded.seq > sync_import_mark.seq`, origin, seq); err != nil {
		return fmt.Errorf("db: set import mark %s: %w", origin, mapBusy(err))
	}
	return nil
}

// RewindImportMark sets an origin's mark down. It is explicitly named so the
// forward-only guard of SetImportMark stays intact for every other caller.
func (d *DB) RewindImportMark(origin string, seq int) error {
	return d.Tx(func(t *Tx) error { return t.RewindImportMark(origin, seq) })
}

func (t *Tx) RewindImportMark(origin string, seq int) error {
	if _, err := t.exec(`INSERT INTO sync_import_mark (origin, seq) VALUES (?, ?)
		ON CONFLICT(origin) DO UPDATE SET seq = excluded.seq`, origin, seq); err != nil {
		return fmt.Errorf("db: rewind import mark %s: %w", origin, mapBusy(err))
	}
	return nil
}

// ExchangeRow is one shared row read for export: the table it is in, the
// primary key it was found by, and every column the file holds, in schema order.
//
// Values come back typed rather than as text. A BLOB column returns its bytes
// and the compressed columns of migration 013 return the bytes on disk, so a
// caller sees exactly what is stored.
type ExchangeRow struct {
	// Table is the shared table the row is in.
	Table string
	// PK is the primary key the row was read by, as the outbox records it.
	PK string
	// RowID is the row's position in its own table, which is what a paged walk
	// carries between pages. It is local to one file, and it is the one ordering
	// an index serves, so paging by it reads a page rather than sorting the table.
	RowID int64
	// Columns are the row's columns in schema order, keyed by name.
	Columns []ExchangeColumn
}

func (r ExchangeRow) Value(name string) (any, bool) {
	for _, col := range r.Columns {
		if col.Name == name {
			return col.Value, true
		}
	}
	return nil, false
}

// ExchangeColumn is one column of an exported row: its name and the value the
// file holds for it, with NULL kept as a nil value rather than dropped.
type ExchangeColumn struct {
	// Name is the column name as this machine's schema spells it.
	Name string
	// Value is the stored value: nil, int64, float64, string or []byte.
	Value any
}

// ReadExchangeRow returns one row of a shared table by its primary key, with
// every column the table declares. ok is false when the table has no such row,
// which for a drained outbox entry is the row's absence rather than a failure.
//
// tbl must be a name SharedTables carries: it reaches SQL from a body a caller
// may not control, so only names this package classified as shared may reach it.
func (d *DB) ReadExchangeRow(tbl, pk string) (ExchangeRow, bool, error) {
	return readExchangeRow(context.Background(), d.sqlDB, tbl, pk)
}

func (t *Tx) ReadExchangeRow(tbl, pk string) (ExchangeRow, bool, error) {
	return readExchangeRow(t.ctx, t.conn, tbl, pk)
}

func readExchangeRow(ctx context.Context, q queryer, tbl, pk string) (ExchangeRow, bool, error) {
	shared := sharedTable(tbl)
	if shared == nil {
		return ExchangeRow{}, false, fmt.Errorf("db: exchange row: %q is not a shared table: %w", tbl, ErrInvalid)
	}
	keys, err := exchangeKeyValues(pk, len(shared.PrimaryKey))
	if err != nil {
		return ExchangeRow{}, false, err
	}
	columns, err := exchangeColumns(ctx, q, tbl)
	if err != nil {
		return ExchangeRow{}, false, err
	}
	query, args := exchangeRowQuery(*shared, columns, keys)
	return scanExchangeRow(ctx, q, tbl, pk, query, args)
}

// exchangeRowQuery builds the one SELECT a row read runs, with the key values
// bound rather than spliced, so a pk of another shape cannot become SQL.
func exchangeRowQuery(shared SharedTable, columns []string, keys []any) (string, []any) {
	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = quoteIdent(col)
	}
	where := ""
	args := make([]any, 0, len(shared.PrimaryKey))
	for i, col := range shared.PrimaryKey {
		if i > 0 {
			where += " AND "
		}
		where += quoteIdent(col) + " = ?"
		args = append(args, keys[i])
	}
	return "SELECT " + strings.Join(quoted, ", ") + " FROM " + shared.Name + " WHERE " + where, args
}

// scanExchangeRow runs the row read and turns the result set into one row. A
// NULL stays a nil, so a column the writer set to nothing stays distinct from
// one that was never set.
func scanExchangeRow(ctx context.Context, q queryer, tbl, pk, query string, args []any) (ExchangeRow, bool, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return ExchangeRow{}, false, fmt.Errorf("db: exchange row %s %s: %w", tbl, pk, mapBusy(err))
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return ExchangeRow{}, false, fmt.Errorf("db: exchange row %s %s: %w", tbl, pk, mapBusy(err))
		}
		return ExchangeRow{}, false, nil
	}
	names, err := rows.Columns()
	if err != nil {
		return ExchangeRow{}, false, fmt.Errorf("db: exchange row %s %s: %w", tbl, pk, mapBusy(err))
	}
	values := make([]any, len(names))
	dest := make([]any, len(names))
	for i := range values {
		dest[i] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return ExchangeRow{}, false, fmt.Errorf("db: exchange row %s %s: %w", tbl, pk, mapBusy(err))
	}
	if err := rows.Close(); err != nil {
		return ExchangeRow{}, false, fmt.Errorf("db: exchange row %s %s: %w", tbl, pk, mapBusy(err))
	}

	row := ExchangeRow{Table: tbl, PK: pk, Columns: make([]ExchangeColumn, len(names))}
	for i, name := range names {
		row.Columns[i] = ExchangeColumn{Name: name, Value: values[i]}
	}
	return row, true, nil
}

// exchangeColumns returns the table's column names in schema order, from this
// machine's own schema, which is the only source a column name may come from.
func exchangeColumns(ctx context.Context, q queryer, tbl string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, tbl)
	if err != nil {
		return nil, fmt.Errorf("db: exchange columns %s: %w", tbl, mapBusy(err))
	}
	return collectRows(rows, func(s rowScanner) (string, error) { return scanString(s) })
}

// exchangeKeyValues parses the pk text an outbox entry carries into the key
// values it names. Nothing from the text is spliced into a statement, and a pk
// that is not the JSON array of exactly the table's key columns is refused: a
// key of the wrong shape would match nothing or a row the entry never named.
func exchangeKeyValues(pk string, want int) ([]any, error) {
	var raw []any
	if err := json.Unmarshal([]byte(pk), &raw); err != nil {
		return nil, fmt.Errorf("db: exchange key %s: not a JSON array: %w", pk, ErrInvalid)
	}
	if len(raw) != want {
		return nil, fmt.Errorf("db: exchange key %s: has %d values, want %d: %w", pk, len(raw), want, ErrInvalid)
	}
	out := make([]any, len(raw))
	for i, v := range raw {
		out[i] = exchangeKeyValue(v)
	}
	return out, nil
}

// exchangeKeyValue maps a decoded JSON key value onto the type the driver binds:
// a JSON number arrives as a float64, and binding that for an INTEGER column
// would compare as a real.
func exchangeKeyValue(v any) any {
	f, ok := v.(float64)
	if !ok {
		return v
	}
	if f == float64(int64(f)) {
		return int64(f)
	}
	return f
}

// sharedTable returns the shared table entry named tbl, or nil when no shared
// table carries that name.
func sharedTable(tbl string) *SharedTable {
	for i := range SharedTables {
		if SharedTables[i].Name == tbl {
			return &SharedTables[i]
		}
	}
	return nil
}

// DrainedEntry is one outbox entry with the state of the row it names at the
// moment the drain read it, absent when the row is gone.
type DrainedEntry struct {
	// Seq is the entry's position in the outbox, and the order entries come
	// back in.
	Seq int
	// Table and PK are the row the entry names, in the outbox's own spelling:
	// the table name and the json_array of its primary-key columns.
	Table string
	PK    string
	// Op is the write the trigger recorded: insert, update or delete.
	Op string
	// Origin is the installation the trigger attributed the row to. It is
	// absent for a child whose parent was already gone, which the exporter
	// skips rather than treating as a failure.
	Origin sql.NullString
	// Row is the row's current state, nil when the row is not in the file.
	Row *ExchangeRow
}

// DrainOutbox reads up to limit outbox entries in seq order, and with each entry
// the current state of the row it names, from this one transaction.
//
// Both reads are here together on purpose: an entry whose state is read after
// the drain would report a row as present or absent according to a write that
// arrived between the two reads. Holding the write lock for the whole read
// closes that window, so every entry in one drain is the state at one moment.
//
// The batch is closed over the parents its own rows reference. A child whose
// parent's outbox entries all lie past the window would otherwise travel alone,
// and an importer with the foreign keys on would refuse it for a parent it was
// never sent. Such a parent is pulled in here with its snapshot state. Its own
// later entries stay where they are and travel in their own batch, which is
// harmless because import is an idempotent upsert.
func (d *DB) DrainOutbox(limit int) ([]DrainedEntry, error) {
	var out []DrainedEntry
	err := d.Tx(func(t *Tx) error {
		var err error
		out, err = t.DrainOutbox(limit)
		return err
	})
	return out, err
}

func (t *Tx) DrainOutbox(limit int) ([]DrainedEntry, error) {
	window, err := t.readOutbox(limit)
	if err != nil {
		return nil, err
	}
	for i := range window {
		row, found, rerr := t.ReadExchangeRow(window[i].Table, window[i].PK)
		if rerr != nil {
			// An entry naming a table this file does not share is a defect in
			// the schema rather than a row to skip: reading it is what found
			// it, so the drain reports it instead of dropping a change.
			return nil, fmt.Errorf("db: drain outbox entry %d: %w", window[i].Seq, rerr)
		}
		if found {
			window[i].Row = &row
		}
	}
	if len(window) == 0 {
		return nil, nil
	}
	// The window's last sequence is the cut the exporter clears the outbox to.
	// Pulled parents are prepended so the window's own entries stay last and the
	// cut keeps naming the window's tail.
	pulled, err := t.pendingParents(window, window[len(window)-1].Seq)
	if err != nil {
		return nil, err
	}
	return append(pulled, window...), nil
}

// readOutbox returns up to limit entries from the head of the change log, in
// seq order. The order is the order the writes happened in, so a parent written
// before its child comes out before it.
func (t *Tx) readOutbox(limit int) ([]DrainedEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := t.conn.QueryContext(t.ctx,
		`SELECT seq, tbl, pk, op, origin FROM sync_outbox ORDER BY seq LIMIT ?`, limit)
	if err != nil {
		if isMissingTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("db: drain outbox: %w", mapBusy(err))
	}
	defer func() { _ = rows.Close() }()

	var entries []DrainedEntry
	for rows.Next() {
		var e DrainedEntry
		if err := rows.Scan(&e.Seq, &e.Table, &e.PK, &e.Op, &e.Origin); err != nil {
			return nil, fmt.Errorf("db: drain outbox: %w", mapBusy(err))
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: drain outbox: %w", mapBusy(err))
	}
	return entries, nil
}

// DeleteDrainedOutbox removes every outbox entry up to and including seq, and
// leaves the rest. It runs after the transport has accepted the batch, so a
// crash between the append and this delete re-exports rather than losing them:
// re-export is safe because import is an idempotent upsert.
//
// The cut is by seq and not by count, so a drain that returned fewer entries
// than asked deletes exactly what it drained and nothing that arrived after it.
func (d *DB) DeleteDrainedOutbox(seq int) error {
	return d.Tx(func(t *Tx) error { return t.DeleteDrainedOutbox(seq) })
}

func (t *Tx) DeleteDrainedOutbox(seq int) error {
	if _, err := t.exec(`DELETE FROM sync_outbox WHERE seq <= ?`, seq); err != nil {
		if isMissingTable(err) {
			return nil
		}
		return fmt.Errorf("db: delete drained outbox through %d: %w", seq, mapBusy(err))
	}
	return nil
}
