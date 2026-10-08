package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// The enumerate-a-table seam reconcile walks: the rows of one shared table that
// one installation owns, in key order, each row carrying every column the table
// declares. It is the read that turns "this origin's rows" into something a
// walk can step over, and it resolves ownership in SQL through the same
// expression the outbox triggers write, so the rows a reconcile walks are the
// rows an export of the same origin would have drained.

// SharedOwnedRows returns every row of tbl that origin owns, in primary-key
// order. A child resolves through the parent it names, the way its trigger
// does, so a row whose parent is already gone resolves to no owner and is not
// returned: the parent's own delete carries that removal to every importer.
func (d *DB) SharedOwnedRows(tbl, origin string) ([]ExchangeRow, error) {
	return sharedOwnedRows(context.Background(), d.sqlDB, tbl, origin)
}

func (t *Tx) SharedOwnedRows(tbl, origin string) ([]ExchangeRow, error) {
	return sharedOwnedRows(t.ctx, t.conn, tbl, origin)
}

func sharedOwnedRows(ctx context.Context, q queryer, tbl, origin string) ([]ExchangeRow, error) {
	shared := sharedTable(tbl)
	if shared == nil {
		return nil, fmt.Errorf("db: shared owned rows: %q is not a shared table: %w", tbl, ErrInvalid)
	}
	rule, ok := OwnerRules[tbl]
	if !ok {
		return nil, fmt.Errorf("db: shared owned rows: %s has no owner rule: %w", tbl, ErrInvalid)
	}
	columns, err := exchangeColumns(ctx, q, tbl)
	if err != nil {
		return nil, err
	}
	query, args := ownedRowsQuery(*shared, rule, columns, origin)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: shared owned rows %s: %w", tbl, mapBusy(err))
	}
	defer func() { _ = rows.Close() }()

	var out []ExchangeRow
	for rows.Next() {
		row, err := scanOwnedRow(rows, tbl)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: shared owned rows %s: %w", tbl, mapBusy(err))
	}
	return out, nil
}

// ownedRowsQuery builds the one SELECT a walk runs: the key as the outbox spells
// it, every column by name, and the table restricted to the rows whose owner
// resolves to origin. The table and column names come from SharedTables and
// OwnerRules; the owner is bound.
func ownedRowsQuery(shared SharedTable, rule ownerRule, columns []string, origin string) (string, []any) {
	keys := joinQuoted(shared.PrimaryKey)
	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = quoteIdent(col)
	}
	// The key is json_array of the key columns because that is the spelling the
	// outbox records and the log's head holds, so a key this returns compares to
	// a head row as the same string rather than as two shapes of one key.
	query := "SELECT json_array(" + keys + "), " + strings.Join(quoted, ", ") +
		" FROM " + quoteIdent(shared.Name) +
		" WHERE " + ownerExpression(rule, quoteIdent(shared.Name)) + " = ?" +
		" ORDER BY " + keys
	return query, []any{origin}
}

// scanOwnedRow turns one result row into an exchange row, reading the leading
// json_array as the key and every remaining column by the name the statement
// named it under. A key that is not text is refused: it would be a key nothing
// downstream could compare.
func scanOwnedRow(rows *sql.Rows, tbl string) (ExchangeRow, error) {
	names, err := rows.Columns()
	if err != nil {
		return ExchangeRow{}, fmt.Errorf("db: shared owned rows %s: %w", tbl, mapBusy(err))
	}
	values := make([]any, len(names))
	dest := make([]any, len(names))
	for i := range values {
		dest[i] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return ExchangeRow{}, fmt.Errorf("db: shared owned rows %s: %w", tbl, mapBusy(err))
	}
	pk, ok := values[0].(string)
	if !ok {
		return ExchangeRow{}, fmt.Errorf("db: shared owned rows %s: key is %T, want text: %w",
			tbl, values[0], ErrInvalid)
	}
	row := ExchangeRow{Table: tbl, PK: pk, Columns: make([]ExchangeColumn, len(names)-1)}
	for i, name := range names[1:] {
		row.Columns[i] = ExchangeColumn{Name: name, Value: values[i+1]}
	}
	return row, nil
}
