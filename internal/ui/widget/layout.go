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

// BlockLines renders raw prose lines fitted to width and padded to height.
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

// FitLines pads lines to height, truncates the overflow and fits each to width.
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

// Clamp keeps v inside [lo, hi].
func Clamp(v, lo, hi int) int {
	return min(max(v, lo), hi)
}

// Centered centres one line in the body box.
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
