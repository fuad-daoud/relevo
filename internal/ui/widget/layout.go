package widget

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Fit pads or truncates s to width cells. Truncation is by cell, no
// ellipsis: at a narrow width an ellipsis costs more than it says.
func Fit(s string, width int) string {
	w := lipgloss.Width(s)
	if w == width {
		return s
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// Pad pads s to width cells, truncating with an ellipsis past it.
func Pad(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	if lipgloss.Width(s) == width {
		return s
	}
	if width <= 0 {
		return ""
	}
	return lipgloss.NewStyle().MaxWidth(width-1).Render(s) + "…"
}

func BlockLines(raw []string, width, height int) []string {
	out := make([]string, 0, height)
	for _, l := range raw {
		out = append(out, Fit(EmptyStyle.Render(l), width))
	}
	for len(out) < height {
		out = append(out, Fit("", width))
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}

func FitLines(lines []string, width, height int) []string {
	out := make([]string, 0, height)
	for _, l := range lines {
		out = append(out, Fit(l, width))
	}
	for len(out) < height {
		out = append(out, Fit("", width))
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}

func Clamp(v, lo, hi int) int {
	return min(max(v, lo), hi)
}

// BodyHeight is the shell's body height: height minus the header, blank row
// and the context row, the error block, and the rule and keys rows.
func BodyHeight(env Env) int {
	h := env.Height - 3 - env.ErrRows - 2
	if h < 0 {
		return 0
	}
	return h
}

func Centered(text string, width, height int) []string {
	out := FitLines(nil, width, height)
	if height <= 0 {
		return out
	}
	pad := (width - lipgloss.Width(text)) / 2
	if pad < 0 {
		pad = 0
	}
	out[height/2] = Fit(strings.Repeat(" ", pad)+EmptyStyle.Render(text), width)
	return out
}
