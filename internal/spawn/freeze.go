package spawn

import "strings"

// IsFrozenUnitError reports whether s names a systemd frozen-unit refusal: a
// slice frozen by suspend (or by hand) refuses a new scope, so systemd-run
// never runs the builder and exits with the manager's wording on stderr. It
// matches the manager's own phrases, not the runner's prefix, so both a Start
// error and a stream tail classify. Pure.
func IsFrozenUnitError(s string) bool {
	lower := strings.ToLower(s)
	if strings.Contains(lower, "frozen unit") ||
		strings.Contains(lower, "frozen slice") ||
		strings.Contains(lower, "frozen scope") {
		return true
	}
	return strings.Contains(lower, "failed with") && strings.Contains(lower, "frozen")
}
