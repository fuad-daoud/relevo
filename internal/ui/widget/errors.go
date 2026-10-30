package widget

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// MaxErrorLines caps RenderError: a longer error ends with a trailing "…".
const MaxErrorLines = 8

// RenderError wraps err across the terminal width, preserving the newlines
// the message already has, capped at MaxErrorLines with a trailing "…" when
// it overflows.
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

// wrapLine word-wraps one line to width.
func wrapLine(line string, width int) []string {
	if width <= 0 {
		width = 80
	}
	if lipgloss.Width(line) <= width {
		return []string{line}
	}
	words := strings.Split(line, " ")
	var lines []string
	var cur strings.Builder
	curW := 0

	for _, w := range words {
		ww := lipgloss.Width(w)
		if cur.Len() == 0 {
			if ww > width {
				for _, r := range w {
					rw := lipgloss.Width(string(r))
					if curW+rw > width && cur.Len() > 0 {
						lines = append(lines, cur.String())
						cur.Reset()
						curW = 0
					}
					cur.WriteRune(r)
					curW += rw
				}
			} else {
				cur.WriteString(w)
				curW = ww
			}
		} else {
			if curW+1+ww <= width {
				cur.WriteByte(' ')
				cur.WriteString(w)
				curW += 1 + ww
			} else {
				lines = append(lines, cur.String())
				cur.Reset()
				curW = 0
				if ww > width {
					for _, r := range w {
						rw := lipgloss.Width(string(r))
						if curW+rw > width && cur.Len() > 0 {
							lines = append(lines, cur.String())
							cur.Reset()
							curW = 0
						}
						cur.WriteRune(r)
						curW += rw
					}
				} else {
					cur.WriteString(w)
					curW = ww
				}
			}
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// ErrorRows is the line count of RenderError(err, width) when err != nil,
// else 0.
func ErrorRows(err error, width int) int {
	if err == nil {
		return 0
	}
	return strings.Count(RenderError(err, width), "\n") + 1
}
