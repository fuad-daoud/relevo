package transcript

import "strings"

// ThinkingMarker is the first character of every rendered thinking line, so the
// limit and denial scans can skip the model's own musings.
const ThinkingMarker = "∴"

// IsThinking reports whether line is a rendered thinking line, so a scan ignores
// the model reasoning about a limit rather than hitting one. The marker is tested
// on the body after the stamp.
func IsThinking(line string) bool {
	_, body := SplitStamp(line)
	return strings.HasPrefix(body, ThinkingMarker)
}

// thinkingLines renders text as thinking lines, each carrying ThinkingMarker; a
// blank prose line is exactly the marker, so no rendered line has trailing
// whitespace, and leading and trailing blank lines are dropped, nil when nothing
// is left. Lines are not capped: thinking shows in full, like assistant text.
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
