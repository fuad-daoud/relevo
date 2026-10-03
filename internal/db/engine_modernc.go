//go:build modernc

package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"time"

	sqlite "modernc.org/sqlite"
)

// engineName is the driver this build opens databases with; the modernc build
// is the way back from Turso.
const engineName = "sqlite"

// journalSizeLimit caps the -wal file after a checkpoint resets it, in bytes;
// without it sqlite keeps a write burst's high-water size for the process's
// life. It is a modernc-only pragma: Turso does not support it.
const journalSizeLimit = 64 << 20

// sqliteIOErr is SQLite's primary result code for I/O errors (SQLITE_IOERR).
const sqliteIOErr = 10

// fileDSN builds a `file:` DSN for path with params after the `?`. The path is
// percent-encoded, so a `#`, `?` or `%` in it stays part of the filename
// instead of truncating the DSN to another file; the path must be absolute,
// which every call site satisfies.
func fileDSN(path, params string) string {
	return (&url.URL{Scheme: "file", Path: path}).String() + "?" + params
}

// openPool opens a pool on path with the pragmas modernc expects. A read-only
// pool opens mode=ro; a writable one switches to WAL, turns foreign keys on,
// and caps the -wal.
func openPool(path string, busy time.Duration, readOnly bool) (*sql.DB, error) {
	if readOnly {
		return openModernc(fileDSN(path, "mode=ro&_pragma=busy_timeout(5000)"))
	}
	dsn := fileDSN(path, fmt.Sprintf(
		"_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=journal_size_limit(%d)",
		busy.Milliseconds(), journalSizeLimit))
	return openModernc(dsn)
}

// openModernc builds the pool from this engine's connector rather than
// sql.Open, so the connection wrapper can repair a string argument on the way
// in: sql.Open offers no place to interpose, and the repair must hold under
// modernc too, so a file written before a downgrade stays readable by Turso.
func openModernc(dsn string) (*sql.DB, error) {
	connector, err := sqlite.NewConnector(dsn)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(repairConnector{Connector: connector}), nil
}

// engineCode reports no code: modernc's *sqlite.Error already carries one, so
// wire.CodeOf reads it directly.
func engineCode(error) (int, int, bool) { return 0, 0, false }

// engineLocked reports no engine-level lock: modernc's SQLITE_BUSY is a busy
// error, which mapBusy maps, not a separate locked sentinel.
func engineLocked(error) bool { return false }

// vacuumIntoStmt is the placeholder form modernc accepts, with the target
// passed as an argument.
func vacuumIntoStmt(path string) (string, []any, error) {
	return `VACUUM INTO ?`, []any{path}, nil
}

// prepareEngine is a no-op: modernc carries no external library to extract.
func prepareEngine(string) error { return nil }

// convertLegacy is a no-op under modernc: this engine reads the file an earlier
// modernc build wrote, so there is nothing to convert.
func convertLegacy(string) error { return nil }

// requireConverted is a no-op under modernc: this engine reads the files an
// earlier modernc build wrote, so a read-only open has nothing to refuse.
func requireConverted(string) error { return nil }

// engineStatus reports the modernc engine: it carries no external library, so
// there is nothing to be missing and nothing to mismatch.
func engineStatus(string) EngineState { return EngineState{Name: engineName} }
