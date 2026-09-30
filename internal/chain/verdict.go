package chain

import (
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/reporttail"
)

// ParseVerdict reads the verdict from a reviewer's output. A missing block, a
// missing key or a value other than pass or changes is no verdict, so a
// malformed output is never read as a pass.
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

// ParseFindings reads the finding count from a security actor's output. It
// accepts a non-negative integer and nothing else.
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

// blockValue returns the value of the last key in the body's trailing relevo
// block. A missing block, an unreadable fence or a missing key returns
// ok == false, the same way the builder report tail treats them.
func blockValue(body []byte, key string) (string, bool) {
	lines := reporttail.SplitFenceLines(body)
	openIdx, closeIdx, reason := reporttail.FindRelevoBlock(lines)
	if openIdx < 0 || reason != "" {
		return "", false
	}

	val, found := "", false
	for i := openIdx + 1; i < closeIdx; i++ {
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
