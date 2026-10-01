package store

import "regexp"

// reservedRoundFileRe matches a flat round-file name relevo authors only as a
// round_file row: a round's diff, plan-diff, chain-diff and drift patches, its
// builder segments, a consult's findings, and a verify consult's ask. A
// plan-diff and a chain-diff are row-only keys like the round's diff -- the row
// is the record -- so a file with that name under a binding directory is a
// plant. The pattern is anchored and carries no slash, so a nested name --
// NNN-<actor>/<rel>, a runner-written artifact -- is never reserved.
var reservedRoundFileRe = regexp.MustCompile(`^\d{3}-(diff\.patch|plan-diff\.patch|chain-diff\.patch|drift\.patch|builder-segments\.json|[0-9a-f]{8}-findings\.md|[0-9a-f]{8}-ask\.md)$`)

// reservedRoundFile reports whether name is one of those row-only names. relevo
// never writes one of them to disk, so a file with such a name under a binding
// directory is a plant or a stale copy from before the key became row-only; it
// is not the record. The read and stat policy answers these names from the row,
// and the round-file walk neither seals nor lists the file.
func reservedRoundFile(name string) bool { return reservedRoundFileRe.MatchString(name) }
