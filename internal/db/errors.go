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
