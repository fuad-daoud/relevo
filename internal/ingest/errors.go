package ingest

import "errors"

// ErrSource reports a Source with no valid binding behind it.
var ErrSource = errors.New("ingest: source")

// ErrCursor marks a detected rewrite of an append-only member.
var ErrCursor = errors.New("ingest: cursor")
