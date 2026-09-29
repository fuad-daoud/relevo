package ingest

import "errors"

// ErrSource reports a Source that could not produce a valid binding.
var ErrSource = errors.New("ingest: source")

// ErrCursor marks a detected rewrite of an append-only member.
var ErrCursor = errors.New("ingest: cursor")
