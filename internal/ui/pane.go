package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/fuad-daoud/relevo/internal/transcript"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

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
			lines[i] = widget.DiffFileStyle.Render(l)
		case strings.HasPrefix(l, "@@"):
			lines[i] = widget.DiffHunkStyle.Render(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = widget.DiffAddStyle.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = widget.DiffDelStyle.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// colourTranscript styles a headless builder's log for the terminal tab,
// by the transcript's own markers and nothing else: the plain-text stamp is
// faint, a call is a green bullet, a bold tool name and its argument in
// parentheses, dim; an ok result is dim; an error result is red; a thinking
// line is dim italic; every other line -- assistant prose, [unknown] events,
// the relevo-exit trailer -- is left alone.
func colourTranscript(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		stamp, text := transcript.SplitStamp(l)
		faint := ""
		if stamp != "" {
			faint = widget.FaintStyle.Render(stamp)
		}
		switch {
		case transcript.IsThinking(l):
			lines[i] = faint + widget.MutedStyle.Italic(true).Render(text)
		case strings.HasPrefix(text, "● "):
			rest := strings.TrimPrefix(text, "● ")
			name, arg, _ := strings.Cut(rest, " ")
			out := widget.StateActiveStyle.Render("●") + " " + lipgloss.NewStyle().Bold(true).Render(name)
			if arg != "" {
				out += widget.MutedStyle.Render("(" + arg + ")")
			}
			lines[i] = faint + out
		case strings.HasPrefix(text, "  ⎿ error"):
			lines[i] = faint + widget.ErrorStyle.Render(text)
		case strings.HasPrefix(text, "  ⎿ "):
			lines[i] = faint + widget.MutedStyle.Render(text)
		default:
			lines[i] = l
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
			lines = append(lines, widget.Fit("", width))
		} else {
			lines = append(lines, widget.Fit(widget.EmptyStyle.Render(l), width))
		}
	}
	for len(lines) < height {
		lines = append(lines, widget.Fit("", width))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}
