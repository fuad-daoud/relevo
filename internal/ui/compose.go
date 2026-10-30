package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// dimLines strips every style from each line and re-renders it in widget.ShadeStyle, so
// the screen behind a modal reads as one flat dark tone.
func dimLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = widget.ShadeStyle.Render(ansi.Strip(l))
	}
	return out
}

// composeBox draws box over base at column x, row y. For each box row i with
// y+i in range: left := ansi.Truncate(base[y+i], x, ""); right :=
// ansi.TruncateLeft(base[y+i], x+boxW, ""); line = left + box[i] + right, then
// widget.Fit(line, width). Rows outside base are dropped; x is clamped to [0, width-boxW]
// (0 when the box is wider than width). base is not modified.
func composeBox(base, box []string, x, y, width int) []string {
	out := make([]string, len(base))
	copy(out, base)

	boxW := 0
	for _, r := range box {
		if w := lipgloss.Width(r); w > boxW {
			boxW = w
		}
	}

	maxX := width - boxW
	if maxX < 0 {
		maxX = 0
	}
	if x < 0 {
		x = 0
	} else if x > maxX {
		x = maxX
	}

	for i, bRow := range box {
		rowIdx := y + i
		if rowIdx < 0 || rowIdx >= len(base) {
			continue
		}
		left := ansi.Truncate(base[rowIdx], x, "")
		right := ansi.TruncateLeft(base[rowIdx], x+boxW, "")
		line := left + bRow + right
		out[rowIdx] = widget.Fit(line, width)
	}

	return out
}

// modalInnerW is a modal's inner width: min(want, width-6), floored at 20. It
// is shared by renderModal and the overlays that compose rows to innerW.
func modalInnerW(want, width int) int {
	innerW := want
	if width-6 < innerW {
		innerW = width - 6
	}
	if innerW < 20 {
		innerW = 20
	}
	return innerW
}

// renderModal draws the D2 box: innerW = min(want, width-6) (floor 20); a top
// border "╭─ " + title + " " + "─"… + "╮"; one line per row, "│ " + row padded
// to innerW + " │"; "╰─…─╯". No background is painted: the box interior is the
// terminal background (§2.1). The border and title use widget.DangerStyle when
// danger, else widget.AccentStyle; the title is bold.
func renderModal(title string, rows []string, want, width int, danger bool) []string {
	innerW := modalInnerW(want, width)

	borderSt := widget.AccentStyle
	if danger {
		borderSt = widget.DangerStyle
	}
	titleSt := borderSt.Bold(true)

	topBorder := ""
	if title != "" {
		titleW := lipgloss.Width(title)
		dashes := innerW - 1 - titleW
		if dashes < 0 {
			dashes = 0
		}
		topBorder = borderSt.Render("╭─ ") + titleSt.Render(title) + borderSt.Render(" "+strings.Repeat("─", dashes)+"╮")
	} else {
		topBorder = borderSt.Render("╭" + strings.Repeat("─", innerW+2) + "╮")
	}

	out := make([]string, 0, len(rows)+2)
	out = append(out, topBorder)
	for _, row := range rows {
		fitted := widget.Fit(row, innerW)
		line := borderSt.Render("│ ") + fitted + borderSt.Render(" │")
		out = append(out, line)
	}
	bottomBorder := borderSt.Render("╰" + strings.Repeat("─", innerW+2) + "╯")
	out = append(out, bottomBorder)

	return out
}

// modalRow renders one list row inside a modal: marker "▸ " (accent) when
// selected else "  ", name, desc, and an optional right-aligned note. A
// selected row is rebuilt as plain text and rendered once with widget.SelBandStyle,
// so the band covers the whole row (§2.2). Unselected rows are unchanged.
func modalRow(selected bool, name, desc, note string, innerW int, nameStyle, descStyle lipgloss.Style) string {
	if selected {
		line := "▸ " + name
		if desc != "" {
			line += "  " + desc
		}
		if note != "" {
			gap := innerW - lipgloss.Width(line) - lipgloss.Width(note)
			if gap < 1 {
				gap = 1
			}
			line += strings.Repeat(" ", gap) + note
		}
		return widget.SelBandStyle.Foreground(widget.TextStyle.GetForeground()).Bold(true).Render(widget.Fit(line, innerW))
	}
	marker := "  "
	left := marker + nameStyle.Render(name)
	if desc != "" {
		left += "  " + descStyle.Render(desc)
	}
	line := left
	if note != "" {
		gap := innerW - lipgloss.Width(left) - lipgloss.Width(note)
		if gap < 1 {
			gap = 1
		}
		line = left + strings.Repeat(" ", gap) + widget.FaintStyle.Render(note)
	}
	return widget.Fit(line, innerW)
}

// modalSection renders a faint bold section label ("VIEWS").
func modalSection(label string) string {
	return widget.FaintStyle.Bold(true).Render(label)
}
