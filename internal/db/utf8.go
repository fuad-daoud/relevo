package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"unicode/utf8"
)

// replacementText stands in for a byte sequence that is not valid UTF-8. relevo
// stores harness output, which can carry any bytes; Turso refuses to write
// invalid UTF-8 and cannot read a database that holds it, so every string
// argument is repaired before either engine sees the value.
const replacementText = "\uFFFD"

// repairConn wraps a driver connection so a string argument bound to a
// statement is repaired to valid UTF-8 first. A []byte argument is untouched:
// compressed columns are BLOBs and may hold arbitrary bytes.
type repairConn struct{ driver.Conn }

// CheckNamedValue implements driver.NamedValueChecker. Only a string is repaired
// here; every other value returns driver.ErrSkip so database/sql's own
// conversion still runs.
func (c repairConn) CheckNamedValue(nv *driver.NamedValue) error {
	s, ok := nv.Value.(string)
	if !ok {
		return driver.ErrSkip
	}
	nv.Value = strings.ToValidUTF8(s, replacementText)
	return nil
}

// repairConnector wraps a driver.Connector so every connection it opens repairs
// string arguments. database/sql asks the connection for its NamedValueChecker,
// so wrapping Connect is enough to cover Exec, Query and prepared statements.
type repairConnector struct{ driver.Connector }

// Connect opens one connection and interposes the repair on the way back.
func (c repairConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return repairConn{Conn: conn}, nil
}

// ColumnRepair is one column's share of a conversion repair: how many values
// held invalid UTF-8 and were rewritten.
type ColumnRepair struct {
	Table    string
	Column   string
	Repaired int
}

// repairInvalidText rewrites every TEXT value that is not valid UTF-8 with the
// replacement character, in one transaction, and returns the per-column count.
// It runs under modernc, the only engine that can still read such a value: Turso
// refuses to read a database that holds one. Round 2's one-time conversion must
// call this after its checkpoint and before Turso's first open, and log the
// counts.
func repairInvalidText(conn *sql.Conn) ([]ColumnRepair, error) {
	ctx := context.Background()
	targets, err := textColumns(ctx, conn)
	if err != nil {
		return nil, err
	}
	repairs := make([]ColumnRepair, 0, len(targets))
	if len(targets) == 0 {
		return repairs, nil
	}

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("db: repair text: begin: %w", mapBusy(err))
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	for _, t := range targets {
		repaired, err := repairColumn(ctx, conn, t.table, t.column)
		if err != nil {
			return nil, err
		}
		repairs = append(repairs, ColumnRepair{Table: t.table, Column: t.column, Repaired: repaired})
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("db: repair text: commit: %w", mapBusy(err))
	}
	committed = true
	return repairs, nil
}

// textColumn names one column the repair visits.
type textColumn struct{ table, column string }

// textColumns lists every TEXT column of every ordinary table, ordered by table
// then schema position, so the returned counts are deterministic.
func textColumns(ctx context.Context, conn *sql.Conn) ([]textColumn, error) {
	tables, err := tableNames(ctx, conn)
	if err != nil {
		return nil, err
	}
	var out []textColumn
	for _, table := range tables {
		columns, err := textColumnsOf(ctx, conn, table)
		if err != nil {
			return nil, err
		}
		for _, column := range columns {
			out = append(out, textColumn{table: table, column: column})
		}
	}
	return out, nil
}

// tableNames reads the ordinary tables from sqlite_master; sqlite_'s own tables
// carry no application text.
func tableNames(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rows, err := conn.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("db: repair text: tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("db: repair text: tables: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: repair text: tables: %w", err)
	}
	return names, nil
}

// textColumnsOf returns the columns one table declares TEXT, read from
// PRAGMA table_info so a name is taken from the schema rather than guessed.
func textColumnsOf(ctx context.Context, conn *sql.Conn, table string) ([]string, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA table_info("+quoteIdent(table)+")")
	if err != nil {
		return nil, fmt.Errorf("db: repair text: %s columns: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var columns []string
	for rows.Next() {
		var (
			cid        int
			name       string
			declType   string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &declType, &notNull, &defaultVal, &pk); err != nil {
			return nil, fmt.Errorf("db: repair text: %s columns: %w", table, err)
		}
		if strings.EqualFold(strings.TrimSpace(declType), "TEXT") {
			columns = append(columns, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: repair text: %s columns: %w", table, err)
	}
	return columns, nil
}

// repairColumn rewrites one column's invalid text and reports how many rows it
// touched. Only a value whose storage class is text is considered, so a BLOB or
// a number that happens to live in a TEXT-declared column is left as it is.
func repairColumn(ctx context.Context, conn *sql.Conn, table, column string) (int, error) {
	query := fmt.Sprintf(`SELECT rowid, typeof(%s), %s FROM %s`,
		quoteIdent(column), quoteIdent(column), quoteIdent(table))
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("db: repair text: %s.%s: %w", table, column, err)
	}

	type invalidRow struct {
		rowid int64
		value string
	}
	var invalid []invalidRow
	for rows.Next() {
		var (
			rowid int64
			kind  string
			value sql.NullString
		)
		if err := rows.Scan(&rowid, &kind, &value); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("db: repair text: %s.%s: %w", table, column, err)
		}
		if !value.Valid || kind != "text" || utf8.ValidString(value.String) {
			continue
		}
		invalid = append(invalid, invalidRow{rowid: rowid, value: value.String})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("db: repair text: %s.%s: %w", table, column, err)
	}
	_ = rows.Close()

	update := fmt.Sprintf(`UPDATE %s SET %s = ? WHERE rowid = ?`, quoteIdent(table), quoteIdent(column))
	for _, bad := range invalid {
		repaired := strings.ToValidUTF8(bad.value, replacementText)
		if _, err := conn.ExecContext(ctx, update, repaired, bad.rowid); err != nil {
			return 0, fmt.Errorf("db: repair text: %s.%s rowid %d: %w", table, column, bad.rowid, err)
		}
	}
	return len(invalid), nil
}

// quoteIdent wraps a schema name in double quotes so a table or column name that
// holds a quote or a keyword cannot change the statement's shape.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
