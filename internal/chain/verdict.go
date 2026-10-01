package chain

import (
	"strconv"

	"github.com/fuad-daoud/relevo/internal/reporttail"
)

// ParseVerdict reads the verdict from a reviewer's output. The key is read
// from the last fenced relevo block that carries it, so a trailing status
// block after the verdict block does not hide it. A missing block, an
// unreadable tail, a missing key or a value other than pass or changes is no
// verdict, so a malformed output is never read as a pass.
func ParseVerdict(body []byte) Verdict {
	val, ok := reporttail.BlockValue(body, "verdict")
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
	val, ok := reporttail.BlockValue(body, "findings")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(val)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
