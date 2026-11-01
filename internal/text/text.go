// Package text holds the display-text helpers the CLI, the cockpit views, the
// daemon and the listings share: first-line cuts, plurals, fallbacks and
// millisecond counts.
package text

import (
	"fmt"
	"strings"
)

// FirstLine is the first non-empty line of s; the whole input is trimmed before
// the cut, so a leading blank line never surfaces as an empty reason.
func FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// FirstRaw is s up to its first newline, untrimmed: a verbatim excerpt for
// notices keeping their own spacing.
func FirstRaw(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Plural names a count of units: "1 revision", "3 revisions".
func Plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// OrText returns s, or fallback when s is empty.
func OrText(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// MS renders a millisecond count the way a listing reads: whole milliseconds
// below a second, one decimal above.
func MS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}
