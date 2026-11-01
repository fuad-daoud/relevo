package store

import "regexp"

// reservedRoundFileRe matches a flat round-file name relevo authors only as a
// round_file row: a round's diff, plan-diff, chain-diff and drift patches, its
// builder segments, a consult's findings, a verify consult's ask, and a fork
// merge's conflict report.
var reservedRoundFileRe = regexp.MustCompile(`^\d{3}-(diff\.patch|plan-diff\.patch|chain-diff\.patch|drift\.patch|builder-segments\.json|fork-conflict\.txt|[0-9a-f]{8}-findings\.md|[0-9a-f]{8}-ask\.md)$`)

// reservedRoundFile reports whether name is one of those row-only names: the
// pattern is anchored and carries no slash, so a nested NNN-<actor>/<rel> is
// never reserved, and relevo never writes one to disk, so a file with such a
// name is a plant or a stale copy.
func reservedRoundFile(name string) bool { return reservedRoundFileRe.MatchString(name) }
