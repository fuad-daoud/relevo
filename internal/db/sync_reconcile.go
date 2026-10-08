package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// The enumerate-a-table seam reconcile walks: one bounded, key-ordered page of
// the rows of one shared table that one installation owns, each row carrying
// every column the table declares. It is the read that turns "this origin's rows"
// into something a walk can step over in pages rather than load whole, and it
// resolves ownership in SQL through the same expression the outbox triggers
// write, so the rows a reconcile walks are the rows an export of the same origin
// would have drained.

// SharedOwnedRowPage returns up to limit rows of tbl that origin owns, in rowid
// order, starting just after the rowid after names. The cursor is the rowid the
// previous page ended on, so a walk continues where it stopped instead of
// rescanning the table: no call reads a table whole.
//
// An empty after names the first page.
//
// The rowid is the paging order rather than the key because the key is
// json_array(...) over the primary-key columns, and no index serves an order on
// that expression: ordering by it makes every page sort the whole table, bodies
// included. Paging by rowid walks the table's own index and stops after limit
// rows, which is what keeps a page's read proportional to the page. The key
// still comes back on each row, spelled the way the outbox spells it, so the
// entries a walk emits are unchanged by the order it found its rows in.
func (d *DB) SharedOwnedRowPage(tbl, origin string, after int64, limit int) ([]ExchangeRow, error) {
	return sharedOwnedRowPage(context.Background(), d.sqlDB, tbl, origin, after, limit)
}

func (t *Tx) SharedOwnedRowPage(tbl, origin string, after int64, limit int) ([]ExchangeRow, error) {
	return sharedOwnedRowPage(t.ctx, t.conn, tbl, origin, after, limit)
}

func sharedOwnedRowPage(ctx context.Context, q queryer, tbl, origin string, after int64, limit int) ([]ExchangeRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	shared := sharedTable(tbl)
	if shared == nil {
		return nil, fmt.Errorf("db: shared owned row page: %q is not a shared table: %w", tbl, ErrInvalid)
	}
	rule, ok := OwnerRules[tbl]
	if !ok {
		return nil, fmt.Errorf("db: shared owned row page: %s has no owner rule: %w", tbl, ErrInvalid)
	}
	columns, err := exchangeColumns(ctx, q, tbl)
	if err != nil {
		return nil, err
	}
	query, args := ownedRowPageQuery(*shared, rule, columns, origin, after, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: shared owned row page %s: %w", tbl, mapBusy(err))
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
		return nil, fmt.Errorf("db: shared owned row page %s: %w", tbl, mapBusy(err))
	}
	return out, nil
}

// ownedRowPageQuery builds the one SELECT a page runs: the rowid the cursor
// resumes from, the key as the outbox spells it, every column by name, and the
// table restricted to the rows whose owner resolves to origin and whose rowid
// sits past the cursor. The table and column names come from SharedTables and
// OwnerRules; the owner, the cursor and the page size are bound.
//
// Ordering by rowid is what keeps the read bounded: the rowid order is the
// table's own index order, so the statement walks it from the cursor and stops
// at limit rows, while an order on the key expression would sort every row of
// the table to return a page of them.
func ownedRowPageQuery(shared SharedTable, rule ownerRule, columns []string, origin string, after int64, limit int) (string, []any) {
	key := "json_array(" + joinQuoted(shared.PrimaryKey) + ")"
	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = quoteIdent(col)
	}
	query := "SELECT rowid, " + key + ", " + strings.Join(quoted, ", ") +
		" FROM " + quoteIdent(shared.Name) +
		" WHERE " + ownerExpression(rule, quoteIdent(shared.Name)) + " = ?" +
		" AND rowid > ?" +
		" ORDER BY rowid" +
		" LIMIT ?"
	return query, []any{origin, after, limit}
}

// scanOwnedRow turns one result row into an exchange row, reading the leading
// rowid and the json_array beside it as the key, and every remaining column by
// the name the statement named it under. A key that is not text is refused: it
// would be a key nothing downstream could compare.
func scanOwnedRow(rows *sql.Rows, tbl string) (ExchangeRow, error) {
	names, err := rows.Columns()
	if err != nil {
		return ExchangeRow{}, fmt.Errorf("db: shared owned row page %s: %w", tbl, mapBusy(err))
	}
	var rowid int64
	values := make([]any, len(names)-1)
	dest := make([]any, len(names))
	dest[0] = &rowid
	for i := range values {
		dest[i+1] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return ExchangeRow{}, fmt.Errorf("db: shared owned row page %s: %w", tbl, mapBusy(err))
	}
	pk, ok := values[0].(string)
	if !ok {
		return ExchangeRow{}, fmt.Errorf("db: shared owned row page %s: key is %T, want text: %w",
			tbl, values[0], ErrInvalid)
	}
	row := ExchangeRow{Table: tbl, PK: pk, RowID: rowid, Columns: make([]ExchangeColumn, len(names)-2)}
	for i, name := range names[2:] {
		row.Columns[i] = ExchangeColumn{Name: name, Value: values[i+1]}
	}
	return row, nil
}
