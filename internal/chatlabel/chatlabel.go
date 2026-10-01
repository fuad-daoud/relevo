// Package chatlabel turns a mastermind record's harness kind and session id
// into the harness's own human-facing name for that session. Nothing here
// errors, writes relevo state, or is logged.
package chatlabel

import (
	"strings"
	"time"
)

// MaxTextRunes is the longest Text a Label carries, excluding surrounding quotes.
const MaxTextRunes = 50

// OpencodeTimeout bounds the sqlite3 read of an opencode title so a listing never hangs.
const OpencodeTimeout = 2 * time.Second

// Label is a harness's human-facing name for one session: Text is a chat
// title or a quoted last prompt, Link a claude.ai session URL; both may be empty.
type Label struct {
	Text string
	Link string
}

// String renders "-" for an empty Label, otherwise Text and Link joined by " · ".
func (l Label) String() string {
	switch {
	case l.Text == "" && l.Link == "":
		return "-"
	case l.Link == "":
		return l.Text
	case l.Text == "":
		return l.Link
	default:
		return l.Text + " · " + l.Link
	}
}

// clean collapses s to its first non-empty line, whitespace runs squashed to
// single spaces, cut to MaxTextRunes.
func clean(s string) string {
	s = strings.Join(strings.Fields(firstNonEmptyLine(s)), " ")
	if s == "" {
		return ""
	}
	return truncate(s, MaxTextRunes)
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// truncate cuts s to at most max runes, replacing the last rune with an ellipsis when it does.
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}
