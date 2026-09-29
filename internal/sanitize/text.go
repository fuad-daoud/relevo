// Package sanitize makes untrusted text safe to draw or store: one pass that
// keeps newlines, turns tabs into spaces, and replaces every other control
// rune and invalid UTF-8 with U+FFFD. It owns one placeholder too, the one
// spelling a secret is rewritten as. It imports nothing, so a caller with no
// cycle risk -- presentation, storage or the server -- shares one rule.
package sanitize

import (
	"strings"
	"unicode/utf8"
)

// Redacted is the one spelling every rendering of a secret writes in its place,
// so a second rendering cannot invent a second one.
const Redacted = "<redacted>"

// Text returns s with every terminal control made inert, the rule the TUI has
// always applied before drawing a tab's body: \n is kept, \r is dropped (so
// CRLF becomes LF), \t becomes four spaces, and every other C0 control, DEL,
// C1 control (U+0080-U+009F) and invalid UTF-8 byte becomes U+FFFD. Everything
// else is unchanged.
func Text(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune('\n')
		case r == '\r':
			// \r\n becomes \n. A lone \r is dropped.
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
