package sanitize

import "strings"

// StripControls drops the control bytes a terminal would act on rather than
// print: the C0 range, ESC among them, DEL, and C1. Ordinary printable runes,
// including non-ASCII text, pass through untouched.
func StripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}
