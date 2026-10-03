package reporttail

import "strings"

// BlockValue returns the value of key from the last fenced relevo block of the
// body that carries it. A missing block, an unreadable tail or a missing key
// returns ok == false, the same way the builder report tail treats them.
//
// A body that carries no fenced block falls back to a closing block whose
// backtick fences were lost, so a report that names its values in a bare
// `relevo` line is read the way the same values read fenced. The fallback never
// changes a body's validation: it extracts a value, and the caller decides
// whether the value is admissible.
func BlockValue(body []byte, key string) (string, bool) {
	lines := SplitFenceLines(body)
	openIdx, _, reason := FindRelevoBlock(lines)
	if openIdx < 0 || reason != "" {
		return fencelessBlockValue(lines, key)
	}

	val, found := "", false
	for _, b := range RelevoBlocks(lines) {
		if v, ok := blockKey(lines, b.Open+1, b.Close, key); ok {
			val, found = v, true
		}
	}
	return val, found
}

// HasFencelessBlock reports whether body opens a block with a bare `relevo`
// line and no backtick fence, which is the shape a closing block takes when its
// fences are lost. A body carrying any fenced block is not one: there the
// fences are present and nothing needs tolerating.
func HasFencelessBlock(body []byte) bool {
	lines := SplitFenceLines(body)
	if openIdx, _, reason := FindRelevoBlock(lines); openIdx >= 0 && reason == "" {
		return false
	}
	_, ok := fencelessOpen(lines)
	return ok
}

// fencelessOpen returns the index of the line that opens a fenceless block: the
// LAST line that is exactly the bare word `relevo`, matching the last fenced
// block winning. No such line returns ok == false.
func fencelessOpen(lines []string) (int, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "relevo" {
			return i, true
		}
	}
	return -1, false
}

// fencelessBlockValue reads key from the block a bare `relevo` line opens. The
// block runs to the end of the body: a closing block is the last thing a
// message carries, so the line the closing fence would have sat on is the end
// of the lines there are.
func fencelessBlockValue(lines []string, key string) (string, bool) {
	open, ok := fencelessOpen(lines)
	if !ok {
		return "", false
	}
	return blockKey(lines, open+1, len(lines), key)
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
