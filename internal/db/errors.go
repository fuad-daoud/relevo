package db

import "errors"

var ErrOpen = errors.New("open failed")

var ErrBusy = errors.New("busy")

var ErrNotFound = errors.New("not found")

// ErrInvalid reports a value that failed validation; writers wrap it with the
// offending field or key name.
var ErrInvalid = errors.New("invalid")

// ErrNewerSchema reports a database whose schema is newer than this relevo's
// embedded migrations. Open leaves such a database untouched.
var ErrNewerSchema = errors.New("schema is newer than this relevo")

// ErrLocked reports that another process holds the database: relevo's own
// per-path open lock, or the engine's own file lock. The daemon is the only
// process that may open relevo.db, so a caller that sees it must reach the
// database through the owner instead of opening the file.
var ErrLocked = errors.New("database is locked by another process")

// ErrNotConverted reports a database whose header lacks the conversion marker:
// it was written by a pre-Turso build, or by a build that never finished the
// conversion. A read-only open cannot convert it (that is a write), so it
// refuses and leaves the file for a writable open -- the daemon's -- to convert
// and mark.
var ErrNotConverted = errors.New("relevo.db is not converted yet; start the daemon once")

// ErrPreflightRefused reports that an enable's preflight checks found a
// precondition this database does not meet. It is a refusal rather than a
// failure: nothing went wrong, the database is simply not ready to have its rows
// leave this machine, and every message the refusal carries names the fix.
//
// The sentinel is what lets a caller tell the two apart without reading prose. A
// refusal is answered by doing something -- running a named pass, re-running
// with a named flag -- while an internal failure is answered by reporting a
// defect, and pointing a refusal at a bug report sends the reader to neither.
var ErrPreflightRefused = errors.New("enable preflight refused")

// IsTransient reports whether err is a transient database failure that a caller
// may retry: SQLite busy (ErrBusy or code 5) or SQLite I/O error (code 10).
// Permanent failures (not found, constraint, schema mismatch, corrupt, misuse)
// and non-SQLite errors (dial failures, owner refusals) are not transient.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNewerSchema) {
		return false
	}
	if errors.Is(err, ErrBusy) {
		return true
	}
	if code, _, ok := errCode(err); ok {
		return code == sqliteBusy || code == sqliteIOErr
	}
	return false
}
