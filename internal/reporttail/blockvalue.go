package reporttail

import "strings"

// BlockValue returns the value of key from the last fenced relevo block carrying
// it; prose after the closing fence does not hide the keys. A missing block, an
// unreadable tail or a missing key returns ok == false, the same way the
// builder report tail treats them. A body with no fenced
// block falls back to a closing block whose fences were lost. The fallback never
// changes validation: it extracts a value, the caller decides admissibility.
func BlockValue(body []byte, key string) (string, bool) {
	lines := SplitFenceLines(body)
	openIdx, _ := FindRelevoBlock(lines)
	if openIdx < 0 {
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

// HasFencelessBlock reports whether body opens a block with a bare `relevo` line
// and no fence: the shape a closing block takes when its fences are lost. A body
// with any fenced block is not one.
func HasFencelessBlock(body []byte) bool {
	lines := SplitFenceLines(body)
	if openIdx, _ := FindRelevoBlock(lines); openIdx >= 0 {
		return false
	}
	_, ok := fencelessOpen(lines)
	return ok
}

// fencelessOpen returns the line opening a fenceless block: the LAST bare
// `relevo` line, matching the last fenced block winning. None returns false.
func fencelessOpen(lines []string) (int, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "relevo" {
			return i, true
		}
	}
	return -1, false
}

// fencelessBlockValue reads key from the block a bare `relevo` line opens,
// running to end of body: a closing block is the last thing a message carries.
func fencelessBlockValue(lines []string, key string) (string, bool) {
	open, ok := fencelessOpen(lines)
	if !ok {
		return "", false
	}
	return blockKey(lines, open+1, len(lines), key)
}

// blockKey runs BlockValue's per-line scan over lines[from:to]: the key before
// each non-blank line's first ':' decides, the value is unquoted, and the last
// carrying line wins.
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
