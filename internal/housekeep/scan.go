package housekeep

import (
	"bytes"
	"regexp"

	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/policy"
)

// Built-in instruction-shaped scan patterns.
var builtInPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)<system-reminder`),
	regexp.MustCompile(`(?i)</?(human|assistant)>`),
	regexp.MustCompile(`^\s*(Human|Assistant|User):`),
	regexp.MustCompile(`(?i)ignore (all )?(previous|prior) instructions`),
	regexp.MustCompile(`(?i)^\s*IMPORTANT:.*you must`),
}

// ScanLines returns the 1-based line numbers ScanInstructionShaped counts,
// in order.
func ScanLines(text []byte, extra []*regexp.Regexp) []int {
	if len(text) == 0 {
		return nil
	}

	rawLines := bytes.Split(text, []byte("\n"))
	var matchedLines []int
	insideFence := false

	for i, rawLine := range rawLines {
		lineNo := i + 1
		line := string(bytes.TrimRight(rawLine, "\r"))
		if classify.IsFence(line) {
			insideFence = !insideFence
			continue
		}
		if insideFence {
			continue
		}

		matched := false
		for _, re := range builtInPatterns {
			if re.MatchString(line) {
				matched = true
				break
			}
		}
		if !matched {
			for _, re := range extra {
				if re.MatchString(line) {
					matched = true
					break
				}
			}
		}
		if matched {
			matchedLines = append(matchedLines, lineNo)
		}
	}

	return matchedLines
}

// ScanInstructionShaped counts lines in text that match any built-in pattern or extra patterns outside of fences.
// Pure; no I/O; returns 0 for empty input.
func ScanInstructionShaped(text []byte, extra []*regexp.Regexp) int {
	return len(ScanLines(text, extra))
}

// CompileScanPatterns compiles policy extra scan patterns.
func CompileScanPatterns(p policy.Policy) []*regexp.Regexp {
	if len(p.ScanPatterns) == 0 {
		return nil
	}
	var res []*regexp.Regexp
	for _, pat := range p.ScanPatterns {
		re, err := regexp.Compile(pat)
		if err == nil {
			res = append(res, re)
		}
	}
	return res
}
