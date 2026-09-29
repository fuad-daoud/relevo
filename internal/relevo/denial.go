package relevo

import (
	"regexp"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// matchDenial scans text line by line from the last line backwards, like
// matchLimit, and returns the first (i.e. latest) line matching any
// pattern. Thinking lines are skipped, because a model reasoning about a
// denial is not one. Pure.
func matchDenial(text string, patterns []*regexp.Regexp) (line string, ok bool) {
	if len(patterns) == 0 {
		return "", false
	}
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if transcript.IsThinking(l) {
			continue
		}
		for _, p := range patterns {
			if p.MatchString(l) {
				trimmed := strings.TrimSpace(l)
				runes := []rune(trimmed)
				if len(runes) > 200 {
					runes = runes[:200]
				}
				return string(runes), true
			}
		}
	}
	return "", false
}

// denialPatterns compiles the candidate's DenialPatterns when set, else the
// harness's defaults; patterns that fail to compile are skipped (Load
// validated them; this is defence).
func denialPatterns(c candidate.Candidate, h harness.Harness) []*regexp.Regexp {
	var raw []string
	if len(c.DenialPatterns) > 0 {
		raw = c.DenialPatterns
	} else {
		raw = h.DenialPatterns
	}
	var res []*regexp.Regexp
	for _, p := range raw {
		if re, err := regexp.Compile(p); err == nil {
			res = append(res, re)
		}
	}
	return res
}
