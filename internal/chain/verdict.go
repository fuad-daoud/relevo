package chain

import (
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/reporttail"
)

// ParseVerdict reads the verdict from a reviewer's output. The key is read
// from the last fenced relevo block that carries it, so a trailing status
// block after the verdict block does not hide it. A missing block, an
// unreadable tail, a missing key or a value other than pass or changes is no
// verdict, so a malformed output is never read as a pass.
func ParseVerdict(body []byte) Verdict {
	val, ok := blockValue(body, "verdict")
	if !ok {
		return ""
	}
	switch v := Verdict(val); v {
	case VerdictPass, VerdictChanges:
		return v
	default:
		return ""
	}
}

// ParseFindings reads the finding count from a security actor's output. The
// key is read from the last fenced relevo block that carries it, so a trailing
// status block after the findings block does not hide it. It accepts a
// non-negative integer and nothing else.
func ParseFindings(body []byte) (int, bool) {
	val, ok := blockValue(body, "findings")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(val)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// blockValue returns the value of key from the last fenced relevo block of the
// body that carries it. A missing block, an unreadable tail or a missing key
// returns ok == false, the same way the builder report tail treats them.
func blockValue(body []byte, key string) (string, bool) {
	lines := reporttail.SplitFenceLines(body)
	openIdx, _, reason := reporttail.FindRelevoBlock(lines)
	if openIdx < 0 || reason != "" {
		return "", false
	}

	val, found := "", false
	for _, b := range reporttail.RelevoBlocks(lines) {
		if v, ok := blockKey(lines, b.Open+1, b.Close, key); ok {
			val, found = v, true
		}
	}
	return val, found
}

// blockKey runs blockValue's per-line scan over lines[from:to]: for each
// non-blank, comment-stripped line, the key before its first ':' decides
// whether the line carries the key, and the value is unquoted. The last
// carrying line in the range wins.
func blockKey(lines []string, from, to int, key string) (string, bool) {
	val, found := "", false
	for i := from; i < to; i++ {
		line := strings.TrimSpace(reporttail.StripComment(lines[i]))
		if line == "" {
			continue
		}
		colon := strings.Index(line, ":")
		if colon == -1 || strings.TrimSpace(line[:colon]) != key {
			continue
		}
		val = strings.TrimSpace(reporttail.UnquoteScalar(strings.TrimSpace(line[colon+1:])))
		found = true
	}
	return val, found
}
