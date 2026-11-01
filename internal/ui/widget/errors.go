package widget

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// MaxErrorLines caps RenderError: a longer error ends with a trailing "…".
const MaxErrorLines = 8

// RenderError wraps err to width, keeping the newlines it already has, capped at
// MaxErrorLines with a trailing "…" when it overflows.
func RenderError(err error, width int) string {
	if err == nil {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	rawLines := strings.Split(err.Error(), "\n")
	var wrapped []string
	for _, l := range rawLines {
		wrapped = append(wrapped, wrapLine(l, width)...)
	}
	if len(wrapped) == 0 {
		return ""
	}
	if len(wrapped) > MaxErrorLines {
		wrapped = wrapped[:MaxErrorLines]
		last := wrapped[MaxErrorLines-1]
		ellipsis := "…"
		ew := lipgloss.Width(ellipsis)
		for lipgloss.Width(last)+ew > width && len(last) > 0 {
			runes := []rune(last)
			last = string(runes[:len(runes)-1])
		}
		wrapped[MaxErrorLines-1] = last + ellipsis
	}
	return strings.Join(wrapped, "\n")
}

func wrapLine(line string, width int) []string {
	if width <= 0 {
		width = 80
	}
	if lipgloss.Width(line) <= width {
		return []string{line}
	}
	var lines []string
	var cur strings.Builder
	curW := 0
	for _, w := range strings.Split(line, " ") {
		ww := lipgloss.Width(w)
		if cur.Len() > 0 {
			if curW+1+ww <= width {
				cur.WriteByte(' ')
				cur.WriteString(w)
				curW += 1 + ww
				continue
			}
			lines = append(lines, cur.String())
			cur.Reset()
			curW = 0
		}
		if ww > width {
			appendLongWord(&cur, &lines, &curW, w, width)
		} else {
			cur.WriteString(w)
			curW = ww
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// appendLongWord feeds a word wider than width rune by rune, so a long
// token without spaces still wraps instead of overflowing.
func appendLongWord(cur *strings.Builder, lines *[]string, curW *int, w string, width int) {
	for _, r := range w {
		appendRune(cur, lines, curW, r, width)
	}
}

// appendRune appends r to cur, flushing the full line first when r would
// overflow it, so display width accounting stays in one place.
func appendRune(cur *strings.Builder, lines *[]string, curW *int, r rune, width int) {
	rw := lipgloss.Width(string(r))
	if *curW+rw > width && cur.Len() > 0 {
		*lines = append(*lines, cur.String())
		cur.Reset()
		*curW = 0
	}
	cur.WriteRune(r)
	*curW += rw
}

// ErrorRows is the line count of RenderError(err, width) when err != nil,
// else 0.
func ErrorRows(err error, width int) int {
	if err == nil {
		return 0
	}
	return strings.Count(RenderError(err, width), "\n") + 1
}
