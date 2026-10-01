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

// ErrPragmaNotReadOnly reports a PRAGMA the read-only seam will not run. The
// engine's query_only does not cover `PRAGMA x = ...`, so an assignment would
// reach the database; only the read form is allowed, and only for the names on
// readOnlyPragmas. It is wrapped with ErrInvalid, and a caller that must tell a
// malformed argument from a refused statement maps it to usage.
var ErrPragmaNotReadOnly = errors.New("pragma is not one of the read-only forms")

// readOnlyPragmas are the PRAGMA names the read-only seam accepts, in the order
// the refusal names them. A pragma outside this list can change the connection
// or the file -- writable_schema being the one that did -- and belongs on the
// writable handle, not behind a read-only verb.
var readOnlyPragmas = []string{
	"table_info",
	"table_list",
	"index_list",
	"index_info",
	"foreign_key_list",
	"journal_mode",
	"page_count",
	"page_size",
	"user_version",
	"schema_version",
	"integrity_check",
	"quick_check",
}

// checkReadOnlyPragma refuses a PRAGMA that is not one of the read forms the
// seam allows: `PRAGMA name` or `PRAGMA name(argument)`, for a name on
// readOnlyPragmas. The writing form `PRAGMA name = value`, a schema-qualified
// name and anything the lexer does not recognise are all refused, because none
// of them can be checked against the list.
func checkReadOnlyPragma(stmt string) error {
	if name, ok := pragmaName(stmt); ok && readOnlyPragma(name) {
		return nil
	}
	return fmt.Errorf("db: query read-only: PRAGMA is allowed only as `PRAGMA name` or `PRAGMA name(argument)`, and only for %s: %w: %w",
		strings.Join(readOnlyPragmas, ", "), ErrInvalid, ErrPragmaNotReadOnly)
}

// readOnlyPragma reports whether name is on the read-only list; the match is
// case-insensitive, the way SQLite reads pragma names.
func readOnlyPragma(name string) bool {
	for _, allowed := range readOnlyPragmas {
		if strings.EqualFold(allowed, name) {
			return true
		}
	}
	return false
}

// pragmaName returns the name a `PRAGMA name` or `PRAGMA name(argument)`
// statement reads, and whether the statement is in that read form at all.
func pragmaName(stmt string) (string, bool) {
	_, afterKeyword := word(stmt, skipSpace(stmt, 0))
	name, afterName := word(stmt, skipSpace(stmt, afterKeyword))
	if name == "" {
		return "", false
	}
	rest := strings.TrimSpace(stmt[skipSpace(stmt, afterName):])
	switch {
	case rest == "" || rest == ";":
		return name, true
	case strings.HasPrefix(rest, "(") && strings.HasSuffix(rest, ")"):
		return name, true
	}
	return "", false
}

// word returns the identifier that starts at i and the index just past it, or
// an empty word when none starts there.
func word(stmt string, i int) (string, int) {
	j := i
	for j < len(stmt) && isKeywordByte(stmt[j]) {
		j++
	}
	return stmt[i:j], j
}

// skipSpace returns the index of the first byte of stmt at or after i that is
// neither whitespace nor part of a comment.
func skipSpace(stmt string, i int) int {
	for i < len(stmt) {
		switch c := stmt[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < len(stmt) && stmt[i+1] == '-':
			i = skipLineComment(stmt, i)
		case c == '/' && i+1 < len(stmt) && stmt[i+1] == '*':
			i = skipBlockComment(stmt, i)
		default:
			return i
		}
	}
	return i
}

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
	switch keyword := strings.ToUpper(firstKeyword(one)); {
	case !readOnlyKeywords[keyword]:
		return fmt.Errorf("db: query read-only: the statement must start with SELECT, WITH, VALUES, EXPLAIN or PRAGMA: %w", ErrInvalid)
	case keyword == "PRAGMA":
		if err := checkReadOnlyPragma(one); err != nil {
			return err
		}
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
	// BEGIN is defence in depth: query_only does not block `PRAGMA x = ...`, so
	// a writing assignment that slipped past the read-form check would still be
	// rolled back here. Turso's own query_only refuses the writing pragmas that
	// matter, so the transaction is the second line, not the first.
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
