package view

import "strings"

// sanitizeText drops the control bytes a terminal would act on rather than
// print: the C0 range, ESC among them, DEL, and C1. Reason and Detail are
// model text and git's stderr, and neither is scanned before it reaches here,
// so this render boundary is the one place that stops a builder's output from
// repainting the terminal a human reads. Ordinary printable runes, including
// non-ASCII text, pass through untouched: a status line has no use for a
// control byte, and everything else it prints is the human's own.
func sanitizeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}

// SanitizeText is sanitizeText at the other end of the package, for a renderer
// this package does not own: internal/relevo's policy and history blocks draw
// provider, token and host labels out of the availability and latency history
// tables, and a row of one of those tables can carry a provider name a peer
// chose. Those strings are as untrusted as the model text above, they are drawn
// by the same terminal, and this is the same rule -- so the rule is exported
// rather than written twice, and rather than left unwritten on that path.
//
// It changes screen bytes only. The row in the file, and the body hash the log
// carries for it, are whatever was written and stay that way.
func SanitizeText(s string) string { return sanitizeText(s) }
