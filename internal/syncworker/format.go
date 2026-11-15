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
// build refuses it rather than read columns it may have moved.
const logFormatVersion = 6

// metaFormatKey names the meta row that carries the format version.
const metaFormatKey = "format"

// olderLogTables is the table set a remote at each older format holds. A remote
// holding one of these sets at that format's number is refused with the migrate
// message rather than opened or treated as not a log.
var olderLogTables = map[int][]string{
	1: {"head", "log", "meta"},
	2: {"head", "log", "marks", "meta"},
	3: {"head", "log", "marks", "meta"},
	4: {"head", "log", "marks", "meta"},
	5: {"log", "marks", "meta"},
}

// createStatements is the remote schema, in one place so the create and the
// classification cannot disagree about what a relevo log is: the heads, meta
// and shared tables, in that order.
var createStatements = []string{
	"CREATE TABLE heads (" +
		"origin TEXT NOT NULL PRIMARY KEY, " +
		"seq INTEGER NOT NULL, " +
		"key TEXT NOT NULL, " +
		"entries INTEGER NOT NULL, " +
		"bytes INTEGER NOT NULL, " +
		"floor INTEGER NOT NULL DEFAULT 0, " +
		"legacy_upto INTEGER NOT NULL DEFAULT 0, " +
		"label TEXT NOT NULL DEFAULT '', " +
		"at TEXT NOT NULL)",
	"CREATE TABLE meta (key TEXT NOT NULL PRIMARY KEY, value TEXT NOT NULL)",
	"CREATE TABLE shared (" +
		"origin TEXT NOT NULL, seq INTEGER NOT NULL, batch INTEGER NOT NULL, " +
		"tbl TEXT NOT NULL, pk TEXT NOT NULL, op TEXT NOT NULL, " +
		"schema_version INTEGER NOT NULL, body TEXT, at TEXT NOT NULL, mac BLOB, " +
		"after_origin TEXT, after_seq INTEGER, " +
		"PRIMARY KEY (origin, seq))",
}

// ensureLog makes sure the remote is this log: an empty remote gets the log's
// tables, one whose meta names this format is used, one at an older format
// pulls once and is classified again before the migrate message, and anything
// else is refused as not a log. A remote classified as something other than a
// relevo log is never written into. The table set and the format row are
// matched together, so a remote whose tables and format disagree is refused
// rather than opened.
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
	if !isLogTables(tables) && !isOlderTables(tables) {
		return errNotALog(url)
	}
	format, err := readFormat(ctx, b.db)
	if err != nil {
		return err
	}
	if format == strconv.Itoa(logFormatVersion) && isLogTables(tables) {
		return nil
	}
	if older, ok := olderLogTables[atoiOrZero(format)]; ok && slices.Equal(tables, older) {
		return b.pullOlderFormat(ctx, url, format)
	}
	return errNotALog(url)
}

// unknownShape reports whether a table set matches no shape this build
// classifies: neither the current log's tables nor any older format's. An
// empty set is not unknown: it is the empty remote the log is created on.
func unknownShape(tables []string) bool {
	return len(tables) != 0 && !isLogTables(tables) && !isOlderTables(tables)
}

// resetUnknownShape throws away a pre-existing replica that matches no known
// shape and bootstraps it again from the remote, once per Open: a replica
// this open bootstrapped already is the remote, and a second mismatch falls
// through to ensureLog, which refuses it as not a log rather than discarding
// again.
func (b *TursoBackend) resetUnknownShape(ctx context.Context) error {
	tables, err := userTables(ctx, b.db)
	if err != nil {
		// An unreadable replica is ensureLog's to refuse: it reads the same
		// tables and answers with the same error.
		return nil
	}
	if !unknownShape(tables) || b.driver.Bootstrapped() != 0 {
		return nil
	}
	// The connection goes before the files; a close that fails does not stop
	// the discard, which is what the reset is for.
	_ = b.Close()
	if err := b.driver.Discard(ctx); err != nil {
		return fmt.Errorf("syncworker: discard the replica: %w", markOpenRefusal(err))
	}
	db, err := b.driver.Open(ctx, b.spec)
	if err != nil {
		return fmt.Errorf("syncworker: open replica: %w", markOpenRefusal(err))
	}
	b.db = db
	b.bytes.TursoReceived += b.driver.Bootstrapped()
	return nil
}

// pullOlderFormat gives an older-format replica one pull before the migrate
// refusal: a machine that was offline while another migrated the remote still
// holds the older tables until it pulls, and Open does not pull an existing
// replica. A pull of an unchanged remote costs a few hundred bytes, so the
// refusal stays cheap while the remote is unmigrated. Only this branch pulls:
// the current set and an empty remote never do.
func (b *TursoBackend) pullOlderFormat(ctx context.Context, url, format string) error {
	if err := b.transfer(ctx, b.driver.Pull); err != nil {
		return fmt.Errorf("syncworker: pull: %w", markTransferRefusal(err))
	}
	tables, err := userTables(ctx, b.db)
	if err != nil {
		return fmt.Errorf("syncworker: read the remote's tables: %w", err)
	}
	if !isLogTables(tables) {
		return errMigrate(url, format)
	}
	current, err := readFormat(ctx, b.db)
	if err != nil {
		return err
	}
	if current != strconv.Itoa(logFormatVersion) {
		return errMigrate(url, format)
	}
	return nil
}

// atoiOrZero reads a format row as a number, and a row that is not one as zero
// -- which names no older format, so a remote carrying a format this build
// cannot read is refused rather than guessed at.
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

// isLogTables reports whether the remote holds exactly this build's three tables.
// Anything else -- another database's tables, a partial create, an older format
// -- is a remote this build will not write into.
func isLogTables(tables []string) bool {
	return slices.Equal(tables, []string{"heads", "meta", "shared"})
}

// isOlderTables reports whether the remote holds the table set of any older
// format.
func isOlderTables(tables []string) bool {
	for _, older := range olderLogTables {
		if slices.Equal(tables, older) {
			return true
		}
	}
	return false
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

// errMigrate is the refusal an older-format remote gets until migrated. It names
// the URL and format, and repeats on every attempt.
func errMigrate(url, format string) error {
	return MarkRefusal(CodeMigrate,
		fmt.Errorf("sync: refusing remote %s at format %s: run relevo db sync migrate once every machine runs this release", url, format))
}
