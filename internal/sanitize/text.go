// Package sanitize makes untrusted text safe to draw or store: one pass keeping
// newlines, turning tabs into spaces, and replacing every other control rune and
// invalid UTF-8 with U+FFFD. It owns one secret placeholder too. It imports
// nothing, so callers that must not risk a cycle share one rule.
package sanitize

import (
	"strings"
	"unicode/utf8"
)

// Redacted is the one spelling every secret rendering writes in its place, so a
// second rendering cannot invent a second one.
const Redacted = "<redacted>"

// Text returns s with every terminal control made inert, the rule the TUI applies
// before drawing a tab's body: \n is kept, \r is dropped (so CRLF becomes LF),
// \t becomes four spaces, and every other C0 control, DEL, C1 control
// (U+0080-U+009F) and invalid UTF-8 byte becomes U+FFFD. Everything else is
// unchanged.
func Text(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune('\n')
		case r == '\r': // \r\n becomes \n; a lone \r is dropped
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == utf8.RuneError:
			b.WriteRune('\uFFFD')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
