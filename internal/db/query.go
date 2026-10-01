package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
)

// ErrNotOneStatement reports a query string that is not exactly one statement:
// it is empty, or a top-level semicolon separates two. singleStatement wraps it
// together with ErrInvalid, so a caller that knows only ErrInvalid still sees
// the rejection, while a caller that must tell a malformed argument (usage)
// from a statement the read-only seam refused can tell the two apart.
var ErrNotOneStatement = errors.New("not exactly one statement")

// readOnlyKeywords are the words a read-only statement may start with. The
// check is defence in depth, not the enforcement: the engine's query_only
// blocks DML, DDL and VACUUM, but not ATTACH (which creates a missing file),
// DETACH, REINDEX or transaction statements, so those are refused here and
// nowhere else.
var readOnlyKeywords = map[string]bool{
	"SELECT":  true,
	"WITH":    true,
	"VALUES":  true,
	"EXPLAIN": true,
	"PRAGMA":  true,
}

// QueryReadOnly runs exactly one read-only statement on a connection of its own
// and streams the result to onRow, which receives the statement's column names
// and one row's values. A statement that is not one statement, or that starts
// with a word the read-only allowlist does not carry, is refused with
// ErrInvalid before the database is touched. Everything else is refused by the
// engine or by the read-only transaction.
//
// The connection is never returned to the pool: query_only is a per-connection
// setting, so handing it back would make a later writer that happens to get it
// fail. On a dialled handle the discard also ends the owner's pinned
// connection, which holds the same setting.
func (d *DB) QueryReadOnly(ctx context.Context, stmt string, onRow func(columns []string, values []any) error) error {
	one, err := singleStatement(stmt)
	if err != nil {
		return err
	}
	if !readOnlyKeywords[strings.ToUpper(firstKeyword(one))] {
		return fmt.Errorf("db: query read-only: the statement must start with SELECT, WITH, VALUES, EXPLAIN or PRAGMA: %w", ErrInvalid)
	}

	conn, err := d.sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("db: query read-only: %w", err)
	}
	// The order matters: the transaction is rolled back, then the connection is
	// discarded with driver.ErrBadConn -- the same discard the owner makes of a
	// client's pinned connection -- so database/sql closes it instead of
	// pooling it.
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		// Close reports "connection is already closed" once Raw has discarded
		// the connection, which is the discard doing its job, not a failure.
		_ = conn.Close()
	}()

	// query_only takes 1, not ON: Turso's parser refuses the keyword spelling
	// that the engine's own openPragmas already avoids.
	if _, err := conn.ExecContext(ctx, "PRAGMA query_only = 1"); err != nil {
		return fmt.Errorf("db: query read-only: %w", err)
	}
	// BEGIN is what undoes a writing PRAGMA: query_only does not block
	// `PRAGMA x = ...`, so the transaction is the only thing that rolls it back.
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return fmt.Errorf("db: query read-only: %w", err)
	}

	rows, err := conn.QueryContext(ctx, one)
	if err != nil {
		return fmt.Errorf("db: query read-only: %w", err)
	}
	columns, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return fmt.Errorf("db: query read-only: %w", err)
	}
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			_ = rows.Close()
			return fmt.Errorf("db: query read-only: %w", err)
		}
		if err := onRow(columns, values); err != nil {
			_ = rows.Close()
			return err
		}
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if rowErr != nil {
		return fmt.Errorf("db: query read-only: %w", rowErr)
	}
	return nil
}

// singleStatement returns the one statement stmt holds. A semicolon inside a
// quoted region or a comment does not separate statements, and one trailing
// semicolon is allowed; anything else is ErrNotOneStatement.
func singleStatement(stmt string) (string, error) {
	parts := splitStatements(stmt)
	if n := len(parts); n > 0 && strings.TrimSpace(parts[n-1]) == "" {
		parts = parts[:n-1]
	}
	if len(parts) != 1 || strings.TrimSpace(parts[0]) == "" {
		return "", fmt.Errorf("db: query read-only: want exactly one statement: %w: %w", ErrInvalid, ErrNotOneStatement)
	}
	return strings.TrimSpace(parts[0]), nil
}

// splitStatements splits stmt at its top-level semicolons. A semicolon inside a
// quoted region -- '...', "...", [...] or `...` -- or inside a line or block
// comment is part of the statement it appears in, not a separator, so a string
// like ';' does not look like two statements.
func splitStatements(stmt string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(stmt); {
		switch c := stmt[i]; c {
		case '\'', '"', '`':
			i = skipQuoted(stmt, i, c)
		case '[':
			i = skipQuoted(stmt, i, ']')
		case '-':
			if i+1 < len(stmt) && stmt[i+1] == '-' {
				i = skipLineComment(stmt, i)
				continue
			}
			i++
		case '/':
			if i+1 < len(stmt) && stmt[i+1] == '*' {
				i = skipBlockComment(stmt, i)
				continue
			}
			i++
		case ';':
			parts = append(parts, stmt[start:i])
			i++
			start = i
		default:
			i++
		}
	}
	return append(parts, stmt[start:])
}

// skipQuoted returns the index just past the quoted region that opens at i and
// closes at close. A doubled closing byte is an escaped one, so a quote inside
// a string or an identifier does not end it.
func skipQuoted(s string, i int, close byte) int {
	for i++; i < len(s); {
		if s[i] != close {
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == close {
			i += 2
			continue
		}
		return i + 1
	}
	return i
}

// skipLineComment returns the index just past the `--` comment that starts at
// i, up to and including the newline that ends it.
func skipLineComment(s string, i int) int {
	if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(s)
}

// skipBlockComment returns the index just past the `/*` comment that starts at
// i, or the end of the string when it is never closed.
func skipBlockComment(s string, i int) int {
	if j := strings.Index(s[i+2:], "*/"); j >= 0 {
		return i + 2 + j + 2
	}
	return len(s)
}

// firstKeyword returns the word a statement starts with, skipping leading
// whitespace and comments, or "" when there is none.
func firstKeyword(stmt string) string {
	for i := 0; i < len(stmt); {
		c := stmt[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < len(stmt) && stmt[i+1] == '-':
			i = skipLineComment(stmt, i)
		case c == '/' && i+1 < len(stmt) && stmt[i+1] == '*':
			i = skipBlockComment(stmt, i)
		case isKeywordByte(c):
			j := i
			for j < len(stmt) && isKeywordByte(stmt[j]) {
				j++
			}
			return stmt[i:j]
		default:
			return ""
		}
	}
	return ""
}

// isKeywordByte reports whether c can appear in the word a statement starts
// with: an ASCII letter, digit or underscore.
func isKeywordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
