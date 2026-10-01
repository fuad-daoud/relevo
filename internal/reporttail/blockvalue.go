package reporttail

import "strings"

// BlockValue returns the value of key from the last fenced relevo block of the
// body that carries it. A missing block, an unreadable tail or a missing key
// returns ok == false, the same way the builder report tail treats them.
func BlockValue(body []byte, key string) (string, bool) {
	lines := SplitFenceLines(body)
	openIdx, _, reason := FindRelevoBlock(lines)
	if openIdx < 0 || reason != "" {
		return "", false
	}

	val, found := "", false
	for _, b := range RelevoBlocks(lines) {
		if v, ok := blockKey(lines, b.Open+1, b.Close, key); ok {
			val, found = v, true
		}
	}
	return val, found
}

// blockKey runs BlockValue's per-line scan over lines[from:to]: for each
// non-blank, comment-stripped line, the key before its first ':' decides
// whether the line carries the key, and the value is unquoted. The last
// carrying line in the range wins.
func blockKey(lines []string, from, to int, key string) (string, bool) {
	val, found := "", false
	for i := from; i < to; i++ {
		line := strings.TrimSpace(StripComment(lines[i]))
		if line == "" {
			continue
		}
		colon := strings.Index(line, ":")
		if colon == -1 || strings.TrimSpace(line[:colon]) != key {
			continue
		}
		val = strings.TrimSpace(UnquoteScalar(strings.TrimSpace(line[colon+1:])))
		found = true
	}
	return val, found
}
