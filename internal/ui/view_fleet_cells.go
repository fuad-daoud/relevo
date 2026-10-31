package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

func ago(since, now time.Time) string {
	if since.IsZero() {
		return ""
	}
	d := now.Sub(since)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// Read, not claimed, so the payload is left for the route that pushes it.
func reportReady(b view.BindingStatus) bool {
	return b.MasterMindName == "you" && b.Pending != nil
}

func artifactWord(b view.BindingStatus) string {
	if b.Shape == store.ShapeReader {
		return "artifact"
	}
	return "report"
}

func whatAge(b view.BindingStatus, now time.Time) (what, age string) {
	if reportReady(b) {
		what = artifactWord(b) + " ready"
		if b.Last != nil {
			age = ago(b.Last.TS, now)
		}
		if age == "" && b.Waiting != nil {
			age = ago(b.Waiting.Since, now)
		}
		return what, age
	}
	switch b.Display {
	case "NEEDS YOU":
		if b.Waiting != nil {
			what = b.Waiting.Cause
			if what == "blocked" {
				what = "question"
			}
			age = ago(b.Waiting.Since, now)
		} else if b.Detail != "" {
			what = b.Detail
		} else {
			what = "needs you"
		}
	case "ACTIVE":
		what = b.BuilderStatus
		if b.QuietFor != "" {
			what += " · quiet " + b.QuietFor
		}
	case "PAUSED":
		what = "paused"
		if b.Last != nil && b.Last.Kind == "pause" {
			age = ago(b.Last.TS, now)
		}
	case "DONE":
		what = "done"
		if b.Last != nil {
			age = ago(b.Last.TS, now)
		}
	}
	if b.Display == "NEEDS YOU" && b.Stale != "" {
		what += " · " + b.Stale
	}
	return what, age
}

func clipName(name string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(name) <= width {
		return name
	}
	return lipgloss.NewStyle().MaxWidth(width-1).Render(name) + "…"
}

func candidateText(b view.BindingStatus) string {
	if b.BuilderName != "" {
		return b.BuilderName
	}
	s := b.BuilderCandidate
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return "-"
	}
	return s
}

func actorCell(b view.BindingStatus) string {
	if b.Role == "" {
		return "builder"
	}
	return b.Role
}

func secondSegment(s string) string {
	parts := strings.Split(s, "/")
	if len(parts) >= 2 {
		return parts[1]
	}
	return s
}

func nowCell(b view.BindingStatus, now time.Time) string {
	what, age := whatAge(b, now)
	if age == "" {
		return what
	}
	return what + " · " + age
}

func spendText(s usage.Spend) string {
	sum := s.Measured + s.Estimated
	if sum > 0 {
		text := fmt.Sprintf("$%.2f", sum)
		if sum < 0.01 {
			text = "<$0.01"
		}
		if s.Estimated > 0 {
			text = "~" + text
		}
		return text
	}
	if s.Plan > 0 {
		return "plan"
	}
	return ""
}

func spendCell(b view.BindingStatus) string {
	if b.Spend == nil {
		return ""
	}
	return spendText(*b.Spend)
}

func mastermindCell(b view.BindingStatus) string {
	if b.MasterMindName == "" {
		return "-"
	}
	return b.MasterMindName
}

func repoCell(b view.BindingStatus) string {
	s := b.CWD
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.Replace(s, home, "~", 1)
	}
	return s
}

func fleetRowPlan(width int) (candidate, spend, mastermind bool) {
	if width >= 94 {
		return true, true, true
	}
	if width >= 80 {
		return true, true, false
	}
	if width >= 70 {
		return true, false, false
	}
	return false, false, false
}

func fleetRowLine(b view.BindingStatus, g fleetGroup, selected bool, now time.Time, width int) string {
	gutter := "   "
	if selected {
		gutter = " " + widget.AccentStyle.Render("▍") + " "
	}
	meta := groupMetas[g]
	dot := meta.dot.Render(meta.glyph)

	nameText := widget.Pad(clipName(b.Key(), 20), 20)
	nameStyled := widget.TextStyle.Render(nameText)
	if selected {
		nameStyled = widget.TextStyle.Bold(true).Render(nameText)
	}

	nowVal := rowNow(b, now)
	nowText := widget.Pad(clipName(nowVal, 24), 24)
	var nowStyled string
	switch {
	case strings.HasPrefix(b.BuilderStatus, "exited") || strings.HasPrefix(b.BuilderStatus, "stalled"):
		nowStyled = widget.RedStyle.Render(nowText)
	case b.Unread && g == groupIdle:
		nowStyled = widget.AccentStyle.Render(nowText)
	case selected:
		nowStyled = widget.TextStyle.Render(nowText)
	default:
		nowStyled = widget.MutedStyle.Render(nowText)
	}

	line := gutter + dot + "  " + nameStyled + nowStyled

	cand, spend, plan := fleetRowPlan(width)
	if cand {
		candText := widget.Pad(clipName(candidateText(b), 24), 24)
		line += widget.MutedStyle.Render(candText)
	}
	if plan {
		planText := widget.Pad(clipName(mastermindCell(b), 14), 14)
		line += widget.FaintStyle.Render(planText)
	}
	if spend {
		s := spendCell(b)
		if len([]rune(s)) < 6 {
			s = strings.Repeat(" ", 6-len([]rune(s))) + s
		}
		line += widget.FaintStyle.Render(s)
	}
	if b.Role != "" {
		line += "  " + widget.FaintStyle.Render(b.Role)
	}

	if selected {
		return widget.SelBandStyle.Render(widget.Fit(line, width))
	}
	return widget.Fit(line, width)
}

func doneFoldLine(rows []view.BindingStatus, now time.Time, width int) string {
	line := "   " + widget.FaintStyle.Render("✓") + "  " + widget.MutedStyle.Render(fmt.Sprintf("%d done", len(rows)))

	todayCount := 0
	var newestRow *view.BindingStatus
	var newestTS time.Time
	yNow, mNow, dNow := now.Local().Date()

	for i := range rows {
		b := &rows[i]
		if b.Last != nil && !b.Last.TS.IsZero() {
			yB, mB, dB := b.Last.TS.Local().Date()
			if yB == yNow && mB == mNow && dB == dNow {
				todayCount++
			}
			if newestRow == nil || b.Last.TS.After(newestTS) {
				newestRow = b
				newestTS = b.Last.TS
			}
		}
	}

	if newestRow != nil {
		line += widget.FaintStyle.Render(fmt.Sprintf("  ·  %d today  ·  last %s %s ago", todayCount, newestRow.Key(), ago(newestTS, now)))
	}

	line += "      " + widget.Chip(widget.KbdStyle, ".") + widget.FaintStyle.Render(" show")
	return widget.Fit(line, width)
}

func sectionLine(g fleetGroup, count int) string {
	meta := groupMetas[g]
	hint := meta.hint
	if g == groupDone {
		hint = ". hide"
	}
	sec := "    " + widget.Chip(meta.pill, meta.label) + "  " + widget.FaintStyle.Render(fmt.Sprintf("%d", count))
	if hint != "" {
		sec += "    " + widget.FaintStyle.Italic(true).Render(hint)
	}
	return sec
}

func quoteLine(quote string, selected bool, width int) string {
	line := "        " + widget.FaintStyle.Render("╰ ") + widget.MutedStyle.Italic(true).Render(quote)
	if selected {
		return widget.SelBandStyle.Render(widget.Fit(line, width))
	}
	return widget.Fit(line, width)
}

func doneRows(bindings []view.BindingStatus) []view.BindingStatus {
	var out []view.BindingStatus
	for i := range bindings {
		if bindings[i].Display == "DONE" {
			out = append(out, bindings[i])
		}
	}
	return out
}

func anyDone(bindings []view.BindingStatus) bool {
	for i := range bindings {
		if bindings[i].Display == "DONE" {
			return true
		}
	}
	return false
}
