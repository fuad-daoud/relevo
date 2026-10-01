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
