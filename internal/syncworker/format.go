package syncworker

// The remote's schema and the versions of it: what a log is, how a remote that
// holds an older one is brought forward, and how one holding something else is
// refused. The log's rows and reads live in turso.go; everything here is about
// the shape of the remote rather than what is in it.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// logFormatVersion is the version this build writes into the remote's meta. A
// remote whose meta carries another value defines the log differently, so this
// build refuses it rather than read columns it may have moved -- except the
// versions below, which this build knows how to bring forward in place.
const logFormatVersion = 4

// formatOneVersion is the previous format: the same log without the marks
// table. A remote at it is upgraded rather than refused.
const formatOneVersion = 1

// formatTwoVersion is the format before the mac column: every entry unsigned.
// It is upgraded in place, and its entries keep a NULL mac, which reads as
// unsigned rather than as a signature that failed.
const formatTwoVersion = 2

// formatThreeVersion is the format before the after_origin and after_seq columns.
// Older entries keep NULL in those columns, which reads as having no dependency.
const formatThreeVersion = 3

// metaFormatKey names the meta row that carries the format version.
const metaFormatKey = "format"

// addMACStatement is the DDL an upgrade from format two runs.
const addMACStatement = "ALTER TABLE log ADD COLUMN mac BLOB"

// addAfterOriginStatement and addAfterSeqStatement are the DDLs an upgrade to
// format 4 runs. Both columns are nullable so older entries read as having no
// dependency.
const (
	addAfterOriginStatement = "ALTER TABLE log ADD COLUMN after_origin TEXT"
	addAfterSeqStatement    = "ALTER TABLE log ADD COLUMN after_seq INTEGER"
)

// upgrade is what one older format needs to reach this build's own: the
// statements to run, and the tables the remote must hold for this build to
// believe it. The tables are named per format because only format one lacks the
// marks table; matching them as a pair refuses a remote whose format and tables
// disagree instead of trying to bring it forward.
type upgrade struct {
	tables     []string
	statements []string
}

// upgrades is every format this build brings forward rather than refuses. The
// statements run in one transaction with the format row, so a remote is never
// left holding half of a format.
var upgrades = map[int]upgrade{
	formatOneVersion: {
		tables:     []string{"head", "log", "meta"},
		statements: []string{createMarksStatement, addMACStatement, addAfterOriginStatement, addAfterSeqStatement},
	},
	formatTwoVersion: {
		tables:     []string{"head", "log", "marks", "meta"},
		statements: []string{addMACStatement, addAfterOriginStatement, addAfterSeqStatement},
	},
	formatThreeVersion: {
		tables:     []string{"head", "log", "marks", "meta"},
		statements: []string{addAfterOriginStatement, addAfterSeqStatement},
	},
}

// createStatements is the remote schema, in one place so the create and the
// classification cannot disagree about what a relevo log is. The marks table is
// last, so an upgrade from the format without it creates it on its own.
var createStatements = []string{
	"CREATE TABLE log (" +
		"origin TEXT NOT NULL, seq INTEGER NOT NULL, batch INTEGER NOT NULL, " +
		"tbl TEXT NOT NULL, pk TEXT NOT NULL, op TEXT NOT NULL, " +
		"schema_version INTEGER NOT NULL, body TEXT, at TEXT NOT NULL, mac BLOB, " +
		"after_origin TEXT, after_seq INTEGER, " +
		"PRIMARY KEY (origin, seq))",
	"CREATE TABLE head (" +
		"origin TEXT NOT NULL, tbl TEXT NOT NULL, pk TEXT NOT NULL, " +
		"seq INTEGER NOT NULL, hash TEXT NOT NULL, " +
		"PRIMARY KEY (origin, tbl, pk))",
	"CREATE TABLE meta (key TEXT NOT NULL PRIMARY KEY, value TEXT NOT NULL)",
	createMarksStatement,
}

// ensureLog makes sure the remote is this log: an empty remote gets the log's
// tables, one at an older format is brought forward, and anything else is
// refused. A remote classified as something other than a relevo log is never
// written into. The table set and the format row are matched together, so a
// remote whose tables and format disagree is refused rather than opened as
// something it is not.
func (b *TursoBackend) ensureLog(ctx context.Context, url string) error {
	tables, err := userTables(ctx, b.db)
	if err != nil {
		return fmt.Errorf("syncworker: read the remote's tables: %w", err)
	}
	if len(tables) == 0 {
		return b.createLog(ctx)
	}
	// The table set is checked before the format row is read, because a remote
	// this build would refuse is often one whose meta table does not exist to
	// read -- and that has to answer as a refusal naming the remote, not as a
	// SQL failure about a table.
	if !isLogTables(tables) && !isFormatOneTables(tables) {
		return errNotALog(url)
	}
	format, err := readFormat(ctx, b.db)
	if err != nil {
		return err
	}
	if format == strconv.Itoa(logFormatVersion) && isLogTables(tables) {
		return nil
	}
	if from, ok := upgrades[atoiOrZero(format)]; ok && slices.Equal(tables, from.tables) {
		return b.upgradeLog(ctx, from.statements)
	}
	return errNotALog(url)
}

// atoiOrZero reads a format row as a number, and a row that is not one as zero
// -- which names no upgrade, so a remote carrying a format this build cannot
// read is refused rather than guessed at.
func atoiOrZero(format string) int {
	n, err := strconv.Atoi(format)
	if err != nil {
		return 0
	}
	return n
}

// createLog writes the log's tables and its format row in one replica
// transaction, then pushes so the remote learns the schema. DDL reaches the
// remote only when it is made on a sync connection; the push is what carries
// the new tables and the format row to it.
func (b *TursoBackend) createLog(ctx context.Context) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("syncworker: create the log: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range createStatements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("syncworker: create the log: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO meta (key, value) VALUES (?, ?)",
		metaFormatKey, strconv.Itoa(logFormatVersion)); err != nil {
		return fmt.Errorf("syncworker: write the log format: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("syncworker: create the log: %w", err)
	}
	if err := b.driver.Push(ctx); err != nil {
		return fmt.Errorf("syncworker: push the new log: %w", markTransferRefusal(err))
	}
	return nil
}

// upgradeLog brings a remote this build knows how to open at an older format up
// to this one. The statements and the new format row are written in one replica
// transaction and pushed, the way createLog writes a whole log, so the remote
// never holds one without the other: a remote with the column but the old format
// row would be opened by a build that expects the column's absence to mean
// something.
func (b *TursoBackend) upgradeLog(ctx context.Context, statements []string) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("syncworker: upgrade the log: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			if isDuplicateColumnError(err) {
				continue
			}
			return fmt.Errorf("syncworker: upgrade the log: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE meta SET value = ? WHERE key = ?",
		strconv.Itoa(logFormatVersion), metaFormatKey); err != nil {
		return fmt.Errorf("syncworker: write the log format: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("syncworker: upgrade the log: %w", err)
	}
	if err := b.driver.Push(ctx); err != nil {
		return fmt.Errorf("syncworker: push the upgraded log: %w", markTransferRefusal(err))
	}
	return nil
}

// isDuplicateColumnError reports whether err was caused by an ALTER TABLE ADD
// COLUMN for a column that already exists.
func isDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

// userTables is the remote's own tables, which is everything sqlite_master
// lists that the engine did not create for itself.
func userTables(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if !engineTable(name) {
			names = append(names, name)
		}
	}
	return names, rows.Err()
}

// engineTable reports whether a table belongs to the database engine rather
// than to whatever the remote holds: SQLite's sqlite_ tables and the sync
// engine's turso_ and __turso_internal_ tables, so a replica of an empty remote
// is never empty. The match is on prefixes rather than names, and it is made in
// Go rather than in LIKE, whose underscore matches any one character.
func engineTable(name string) bool {
	for _, prefix := range []string{"sqlite_", "turso_", "__turso_internal_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// isLogTables reports whether the remote holds exactly the log's four tables.
// Anything else -- another database's tables, a partial create, a log missing
// its meta -- is a remote this build will not write into.
func isLogTables(tables []string) bool {
	return slices.Equal(tables, []string{"head", "log", "marks", "meta"})
}

// isFormatOneTables reports whether the remote holds exactly the previous
// format's tables: the same log without the marks table. It is a format to
// bring forward, not a partial create of the current set.
func isFormatOneTables(tables []string) bool {
	return slices.Equal(tables, []string{"head", "log", "meta"})
}

// readFormat is the format version the remote's meta carries, and empty when it
// carries none.
func readFormat(ctx context.Context, db *sql.DB) (string, error) {
	var value string
	err := db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", metaFormatKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("syncworker: read the log version: %w", err)
	}
	return value, nil
}

// errNotALog is the refusal a remote that is not this log gets. It names the URL
// because the operator's next move is to check which database the machine is
// pointed at, and a refusal that did not would send them elsewhere to find out.
// Every later attempt meets the same remote, so the class says so.
func errNotALog(url string) error {
	return MarkRefusal(CodeRemote,
		fmt.Errorf("sync: refusing remote %s: it is not a relevo sync log", url))
}
