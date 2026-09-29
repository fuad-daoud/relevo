package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/fuad-daoud/relevo/internal/transcript"
)

// sep joins a pane head's parts with a faint middle dot (X2). Its only user
// is roundPane.paneHead.
var sep = faintStyle.Render(" · ")

// spread puts right at the right edge of a width-wide line, after left,
// dropping the gap when they would overlap (left wins; the caller decides
// what is important enough for the right).
func spread(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

// diffStat counts a patch the way `git diff --stat` would summarise it:
// files by `diff --git` headers, added and removed by leading +/- that
// are not the ---/+++ file markers.
func diffStat(patch string) (files, added, removed int) {
	for _, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			files++
		case strings.HasPrefix(l, "+++ "), strings.HasPrefix(l, "--- "):
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return
}

// colourDiff styles a patch line by line: file headers bold, hunks blue,
// additions green, removals red, everything else untouched. It never
// parses further than the first characters of a line.
func colourDiff(patch string) string {
	lines := strings.Split(patch, "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "diff --git "), strings.HasPrefix(l, "+++ "), strings.HasPrefix(l, "--- "):
			lines[i] = diffFileStyle.Render(l)
		case strings.HasPrefix(l, "@@"):
			lines[i] = diffHunkStyle.Render(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = diffAddStyle.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = diffDelStyle.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// colourTranscript styles a headless builder's log for the terminal tab,
// by the transcript's own markers and nothing else: a call is a green
// bullet, a bold tool name and its argument in parentheses, dim; an ok
// result is dim; an error result is red; a thinking line is dim italic;
// every other line -- assistant prose, [unknown] events, the relevo-exit
// trailer -- is left alone.
func colourTranscript(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		switch {
		case transcript.IsThinking(l):
			lines[i] = dimStyle.Italic(true).Render(l)
		case strings.HasPrefix(l, "● "):
			rest := strings.TrimPrefix(l, "● ")
			name, arg, _ := strings.Cut(rest, " ")
			out := stateActiveStyle.Render("●") + " " + lipgloss.NewStyle().Bold(true).Render(name)
			if arg != "" {
				out += dimStyle.Render("(" + arg + ")")
			}
			lines[i] = out
		case strings.HasPrefix(l, "  ⎿ error"):
			lines[i] = errorStyle.Render(l)
		case strings.HasPrefix(l, "  ⎿ "):
			lines[i] = dimStyle.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// emptyPaneBlock is the prose the pane shows at zero rows, already styled
// and fitted to width and height (padded with blank rows to height).
func emptyPaneBlock(width, height int) []string {
	if height <= 0 {
		return nil
	}
	raw := []string{
		"no bindings",
		"",
		"  relevo bind                   put a builder on this tree",
		"  relevo bind --worktree        put a builder on its own worktree",
		"  relevo config                 list what can be bound",
	}
	lines := make([]string, 0, height)
	for _, l := range raw {
		if l == "" {
			lines = append(lines, fit("", width))
		} else {
			lines = append(lines, fit(emptyStyle.Render(l), width))
		}
	}
	for len(lines) < height {
		lines = append(lines, fit("", width))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}
