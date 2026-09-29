package chatlabel

import (
	"regexp"
	"strings"
)

// opencodeSessionID matches an opencode session id; duplicated from
// internal/delivery/deliver_opencode.go, which names the same rule.
var opencodeSessionID = regexp.MustCompile(`^ses_[A-Za-z0-9]+$`)

// OpencodeQuery reads a title from OpenCode 2.0.14's session_v2, falling back
// to the legacy session table for pre-2.0 rows. Single quotes in id are
// doubled, the SQL string-literal escape.
func OpencodeQuery(sessionID string) string {
	id := strings.ReplaceAll(sessionID, "'", "''")
	return "select title from session_v2 where id = '" + id + "'" +
		" union all" +
		" select title from session where id = '" + id + "'" +
		" and not exists (select 1 from session_v2 where id = '" + id + "')"
}

// OpencodeLegacyQuery is OpencodeQuery's fallback for a database that predates session_v2.
func OpencodeLegacyQuery(sessionID string) string {
	id := strings.ReplaceAll(sessionID, "'", "''")
	return "select title from session where id = '" + id + "'"
}

// Opencode builds the Label from OpencodeQuery's stdout: its first line,
// cleaned, is the title; empty output gives the empty Label.
func Opencode(out []byte) Label {
	first, _, _ := strings.Cut(string(out), "\n")
	text := clean(strings.TrimSpace(first))
	if text == "" {
		return Label{}
	}
	return Label{Text: text}
}
