package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// renderCard draws a rounded card one column in from the left edge, cardW = width-2 wide:
// top "╭─ " + title + " " + dashes + (right != "" ? " " + right + " ─" : "") + "╮",
// then one "│" + row + pad + "│" line per row, then "╰─…─╯". Every line widget.Fit() to width.
// title and right are pre-styled; each row is pre-styled content, padded to cardW-2.
func renderCard(width int, title, right string, rows []string) []string {
	cardW := width - 2
	if cardW < 20 {
		cardW = 20
	}

	usedTopW := lipgloss.Width("╭─ ") + lipgloss.Width(title) + 1 + lipgloss.Width("╮")
	if right != "" {
		usedTopW += 1 + lipgloss.Width(right) + lipgloss.Width(" ─")
	}
	dashes := cardW - usedTopW
	if dashes < 0 {
		dashes = 0
	}

	var topLine string
	if right != "" {
		topLine = " " + widget.BorderStyle.Render("╭─ ") + title + " " + widget.BorderStyle.Render(strings.Repeat("─", dashes)) + " " + right + widget.BorderStyle.Render(" ─╮")
	} else {
		topLine = " " + widget.BorderStyle.Render("╭─ ") + title + " " + widget.BorderStyle.Render(strings.Repeat("─", dashes)) + widget.BorderStyle.Render("╮")
	}

	out := make([]string, 0, len(rows)+2)
	out = append(out, widget.Fit(topLine, width))

	for _, r := range rows {
		r = clipName(r, cardW-2)
		pad := (cardW - 2) - lipgloss.Width(r)
		if pad < 0 {
			pad = 0
		}
		line := " " + widget.BorderStyle.Render("│") + r + strings.Repeat(" ", pad) + widget.BorderStyle.Render("│")
		out = append(out, widget.Fit(line, width))
	}

	bottomLine := " " + widget.BorderStyle.Render("╰"+strings.Repeat("─", cardW-2)+"╯")
	out = append(out, widget.Fit(bottomLine, width))

	return out
}

// cardKeysRow formats keys for the card footer:
// "   " + widget.Chip(kbd,key)+" "+muted(label) joined by 6 spaces.
func cardKeysRow(keys []widget.KeyHelp) string {
	if len(keys) == 0 {
		return ""
	}
	keyChips := make([]string, len(keys))
	for i, k := range keys {
		keyChips[i] = widget.Chip(widget.KbdStyle, k.Key) + " " + widget.MutedStyle.Render(k.Help)
	}
	return "   " + strings.Join(keyChips, "      ")
}
