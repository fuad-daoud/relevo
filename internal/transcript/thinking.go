package transcript

import "strings"

// ThinkingMarker is the first character of every rendered thinking line. The
// transcript is shown to people, but the limit and denial scans also match on
// it, and they must be able to skip the model's own musings.
const ThinkingMarker = "∴"

// IsThinking reports whether line is one of the transcript's rendered thinking
// lines, so a scan can ignore the model reasoning about a limit rather than
// hitting one.
func IsThinking(line string) bool {
	return strings.HasPrefix(line, ThinkingMarker)
}

// thinkingLines renders text as thinking lines: each carries ThinkingMarker, a
// blank line inside the prose is exactly the marker (so no rendered line has
// trailing whitespace), and leading and trailing blank lines are dropped.
// Nothing left returns nil. Lines are not capped -- thinking is prose and is
// shown in full, like assistant text.
func thinkingLines(text string) []string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if start == end {
		return nil
	}
	out := make([]string, 0, end-start)
	for _, l := range lines[start:end] {
		if strings.TrimSpace(l) == "" {
			out = append(out, ThinkingMarker)
			continue
		}
		out = append(out, ThinkingMarker+" "+l)
	}
	return out
}
