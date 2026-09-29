package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderCard draws a rounded card one column in from the left edge, cardW = width-2 wide:
// top "╭─ " + title + " " + dashes + (right != "" ? " " + right + " ─" : "") + "╮",
// then one "│" + row + pad + "│" line per row, then "╰─…─╯". Every line fit() to width.
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
		topLine = " " + borderStyle.Render("╭─ ") + title + " " + borderStyle.Render(strings.Repeat("─", dashes)) + " " + right + borderStyle.Render(" ─╮")
	} else {
		topLine = " " + borderStyle.Render("╭─ ") + title + " " + borderStyle.Render(strings.Repeat("─", dashes)) + borderStyle.Render("╮")
	}

	out := make([]string, 0, len(rows)+2)
	out = append(out, fit(topLine, width))

	for _, r := range rows {
		r = clipName(r, cardW-2)
		pad := (cardW - 2) - lipgloss.Width(r)
		if pad < 0 {
			pad = 0
		}
		line := " " + borderStyle.Render("│") + r + strings.Repeat(" ", pad) + borderStyle.Render("│")
		out = append(out, fit(line, width))
	}

	bottomLine := " " + borderStyle.Render("╰"+strings.Repeat("─", cardW-2)+"╯")
	out = append(out, fit(bottomLine, width))

	return out
}

// cardKeysRow formats keys for the card footer:
// "   " + chip(kbd,key)+" "+muted(label) joined by 6 spaces.
func cardKeysRow(keys []KeyHelp) string {
	if len(keys) == 0 {
		return ""
	}
	keyChips := make([]string, len(keys))
	for i, k := range keys {
		keyChips[i] = chip(kbdStyle, k.Key) + " " + mutedStyle.Render(k.Help)
	}
	return "   " + strings.Join(keyChips, "      ")
}
