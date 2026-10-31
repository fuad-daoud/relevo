package pick

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// resultModel is the last screen: what the verb said (or the error), held
// until a key so a popup pane does not vanish before it is read.
type resultModel struct {
	pending bool   // the verb is running; keys other than ctrl+c are ignored
	text    string // success text, or the empty-list line
	err     error  // shown wrapped, in place of text
	// outcome is what Run returns once the key is pressed: nil after a
	// successful verb, else one of the sentinels in verb.go.
	outcome error
}

const maxErrorLines = 8

func (m Model) resultView() string {
	var sb strings.Builder
	switch {
	case m.result.pending:
		sb.WriteString(hintStyle.Render(fmt.Sprintf("running relevo %s…", m.opts.Verb)))
		return sb.String()
	case m.result.err != nil:
		sb.WriteString(errorStyle.Render("relevo: ") + renderError(m.result.err, m.width))
	default:
		sb.WriteString(m.result.text)
	}
	sb.WriteString("\n\n")
	sb.WriteString(hintStyle.Render("press any key"))
	return sb.String()
}

// renderError wraps err across width, preserving the newlines the message
// already has, capped at maxErrorLines with a trailing "…". Copied from
// internal/ui/list.go, which pick must not import.
func renderError(err error, width int) string {
	if err == nil {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	var wrapped []string
	for _, l := range strings.Split(err.Error(), "\n") {
		wrapped = append(wrapped, wrapLine(l, width)...)
	}
	if len(wrapped) == 0 {
		return ""
	}
	if len(wrapped) > maxErrorLines {
		wrapped = wrapped[:maxErrorLines]
		last := wrapped[maxErrorLines-1]
		ellipsis := "…"
		ew := lipgloss.Width(ellipsis)
		for lipgloss.Width(last)+ew > width && len(last) > 0 {
			runes := []rune(last)
			last = string(runes[:len(runes)-1])
		}
		wrapped[maxErrorLines-1] = last + ellipsis
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
	flush := func() {
		lines = append(lines, cur.String())
		cur.Reset()
		curW = 0
	}
	writeLong := func(w string) {
		for _, r := range w {
			rw := lipgloss.Width(string(r))
			if curW+rw > width && cur.Len() > 0 {
				flush()
			}
			cur.WriteRune(r)
			curW += rw
		}
	}
	for _, w := range strings.Split(line, " ") {
		ww := lipgloss.Width(w)
		switch {
		case cur.Len() == 0 && ww > width:
			writeLong(w)
		case cur.Len() == 0:
			cur.WriteString(w)
			curW = ww
		case curW+1+ww <= width:
			cur.WriteByte(' ')
			cur.WriteString(w)
			curW += 1 + ww
		default:
			flush()
			if ww > width {
				writeLong(w)
			} else {
				cur.WriteString(w)
				curW = ww
			}
		}
	}
	if cur.Len() > 0 {
		flush()
	}
	return lines
}
