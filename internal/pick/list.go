package pick

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/view"
)

// The picker's own styles. internal/ui has near-identical ones; they are
// not shared because pick must not import ui (spec §8).
var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	hintStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	cursorStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	errorStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	needsYouStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	doneStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	activeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
)

// listChrome is what the list screen spends outside the rows: the title
// line, the blank under it, and the hint line.
const listChrome = 3

// listWindow returns where the first rendered row must be for cursor to be
// visible in a window of rows rows over n items, moving top as little as
// possible. rows <= 0 means no limit: the answer is 0 and every row renders.
// Copied from internal/ui/list.go, which pick must not import.
func listWindow(top, cursor, rows, n int) int {
	if rows <= 0 || n <= rows {
		return 0
	}
	if top > n-rows {
		top = n - rows
	}
	if top < 0 {
		top = 0
	}
	if cursor < top {
		top = cursor
	}
	if cursor >= top+rows {
		top = cursor - rows + 1
	}
	return top
}

func styleDisplay(display string) string {
	padded := fmt.Sprintf("%-9s", display)
	switch display {
	case "NEEDS YOU":
		return needsYouStyle.Render(padded)
	case "DONE":
		return doneStyle.Render(padded)
	case "ACTIVE":
		return activeStyle.Render(padded)
	default:
		return padded
	}
}

// renderRow is one binding line (spec §4):
//
//	> webshop        NEEDS YOU  r3  builder agy blocked
func renderRow(b view.BindingStatus, selected bool) string {
	cursor := " "
	if selected {
		cursor = cursorStyle.Render(">")
	}
	return strings.TrimRight(fmt.Sprintf("%s %-14s %s r%-2d builder %s %s",
		cursor, b.Name, styleDisplay(b.Display), b.Round, b.BuilderCandidate, b.BuilderStatus), " ")
}

// listRows is how many binding rows fit: the height less the chrome, never
// below one once a height is known, zero (no limit) before the first
// WindowSizeMsg.
func (m Model) listRows() int {
	if m.height <= 0 {
		return 0
	}
	n := m.height - listChrome
	if n < 1 {
		n = 1
	}
	return n
}

func (m Model) listView() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render(fmt.Sprintf("relevo %s -- pick a binding", m.opts.Verb)))
	sb.WriteString("\n\n")
	if !m.loaded {
		sb.WriteString(hintStyle.Render("loading…"))
		return sb.String()
	}
	rows := m.listRows()
	top := listWindow(m.top, m.cursor, rows, len(m.rows))
	end := len(m.rows)
	if rows > 0 && top+rows < end {
		end = top + rows
	}
	for i := top; i < end; i++ {
		sb.WriteString(renderRow(m.rows[i], i == m.cursor))
		sb.WriteString("\n")
	}
	sb.WriteString(hintStyle.Render("↑/↓ move  Enter pick  Esc cancel"))
	return sb.String()
}
