package spawn

import "strings"

// IsFrozenUnitError reports whether s names a systemd frozen-unit refusal: a
// slice frozen by suspend (or by hand) refuses a new scope, so systemd-run
// never runs the builder and exits with the manager's wording on stderr. It
// matches the manager's own sentences, not the runner's prefix, so both a
// Start error and a stream tail classify -- and only those sentences, because
// a stream tail also carries the builder's own words, and a builder working on
// this very code writes "frozen slice" in commits and test names. Pure.
func IsFrozenUnitError(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "cannot start frozen ") ||
		strings.Contains(lower, "cannot perform operation on frozen ") ||
		strings.Contains(lower, "'frozen'")
}
