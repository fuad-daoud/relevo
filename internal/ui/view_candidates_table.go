package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/ui/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

type candRow struct {
	c     candidate.Candidate
	slots []relevo.ActorSlot
	on    bool
	gate  *availability.Gate
}

func actorOrder(acts map[string]roles.Actor) []string {
	var out []string
	for _, name := range []string{"builder", "reviewer", "researcher"} {
		if _, ok := acts[name]; ok {
			out = append(out, name)
		}
	}
	rest := make([]string, 0, len(acts))
	for name := range acts {
		switch name {
		case "builder", "reviewer", "researcher":
			continue
		}
		rest = append(rest, name)
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func candName(doc relevo.ConfigDoc, ref string) string {
	for _, c := range doc.Candidates {
		if c.Name == ref || c.Ref().String() == ref {
			return c.Name
		}
	}
	return ""
}

func candRows(doc relevo.ConfigDoc, gated []availability.Gate) []candRow {
	byName := make(map[string]candidate.Candidate, len(doc.Candidates))
	for _, c := range doc.Candidates {
		byName[c.Name] = c
	}
	seen := make(map[string]bool, len(doc.Candidates))
	var out []candRow
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		c, ok := byName[name]
		if !ok {
			return
		}
		seen[name] = true
		out = append(out, candRowWith(doc, c, gated))
	}
	for _, actor := range actorOrder(doc.Actors) {
		for _, e := range doc.Actors[actor].Candidates {
			add(candName(doc, e.Candidate))
		}
	}
	for _, c := range doc.Candidates {
		add(c.Name)
	}
	return out
}

func candRowWith(doc relevo.ConfigDoc, c candidate.Candidate, gated []availability.Gate) candRow {
	r := candRow{c: c}
	for _, s := range relevo.CandidateSlots(doc, c.Name) {
		if s.Off {
			continue
		}
		r.slots = append(r.slots, s)
		r.on = true
	}
	token := c.Ref().String()
	for i := range gated {
		if gated[i].Token == token {
			g := gated[i]
			r.gate = &g
			break
		}
	}
	return r
}

func servesText(r candRow) string {
	parts := make([]string, 0, len(r.slots))
	for _, s := range r.slots {
		parts = append(parts, fmt.Sprintf("%s %d", s.Actor, s.Position))
	}
	return strings.Join(parts, " · ")
}

func candStatus(r candRow, now time.Time) (string, lipgloss.Style) {
	switch {
	case !r.on:
		return "off", widget.FaintStyle
	case r.gate != nil:
		return stats.GateLeft(*r.gate, now), widget.RedStyle
	}
	return "ready", widget.GreenStyle
}

type candCol struct {
	key  string
	head string
	w    int
}

func candLayout(width int) (int, []candCol) {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	visible := []candCol{
		{"harness", "HARNESS", 8},
		{"provider", "PROVIDER", 10},
		{"model", "MODEL", 35},
		{"serves", "SERVES", 22},
		{"status", "STATUS", 9},
	}
	nameW := func(cols []candCol) int {
		fixed := 2 // the two spaces after CANDIDATE
		for i, c := range cols {
			fixed += c.w
			if i < len(cols)-1 {
				fixed += 2
			}
		}
		return cw - fixed
	}
	for _, drop := range []string{"model", "serves", "harness"} {
		if nameW(visible) >= 16 {
			break
		}
		for i, c := range visible {
			if c.key == drop {
				visible = append(append([]candCol(nil), visible[:i]...), visible[i+1:]...)
				break
			}
		}
	}
	return nameW(visible), visible
}

type candCell struct {
	text  string
	style lipgloss.Style
}

func candLine(cells []candCell, sel bool, cw int) string {
	var b strings.Builder
	for i, c := range cells {
		if i > 0 {
			if sel {
				b.WriteString(widget.SelBandStyle.Render("  "))
			} else {
				b.WriteString("  ")
			}
		}
		st := c.style
		if sel {
			st = st.Background(widget.SelBandStyle.GetBackground())
		}
		b.WriteString(st.Render(c.text))
	}
	row := b.String()
	if !sel {
		return "   " + widget.Fit(row, cw) + "   "
	}
	if lipgloss.Width(row) > cw {
		row = lipgloss.NewStyle().MaxWidth(cw).Render(row)
	}
	if w := lipgloss.Width(row); w < cw {
		row += widget.SelBandStyle.Render(strings.Repeat(" ", cw-w))
	}
	return "   " + row + "   "
}

func candHeaderLine(nameW int, cols []candCol, cw int) string {
	style := widget.FaintStyle.Bold(true)
	cells := []candCell{{text: widget.Pad("CANDIDATE", nameW), style: style}}
	for _, c := range cols {
		cells = append(cells, candCell{text: widget.Pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

func candDataLine(r candRow, sel bool, nameW int, cols []candCol, cw int, now time.Time) string {
	base, meta := widget.TextStyle, widget.MutedStyle
	if !r.on {
		base, meta = widget.FaintStyle, widget.FaintStyle
	}
	nameStyle := base
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	cells := []candCell{{text: widget.Pad(r.c.Name, nameW), style: nameStyle}}
	for _, col := range cols {
		var text string
		style := meta
		switch col.key {
		case "harness":
			text = r.c.Harness
		case "provider":
			text = r.c.Provider
		case "model":
			text = r.c.Model
		case "serves":
			text = servesText(r)
		case "status":
			text, style = candStatus(r, now)
		}
		cells = append(cells, candCell{text: widget.Pad(text, col.w), style: style})
	}
	return candLine(cells, sel, cw)
}

func candSince(t, now time.Time) string {
	l, n := t.Local(), now.Local()
	if l.Year() == n.Year() && l.Month() == n.Month() && l.Day() == n.Day() {
		return l.Format("15:04")
	}
	return l.Format("Jan 2 15:04")
}

func candUntilText(g availability.Gate, now time.Time) string {
	if g.Until.IsZero() {
		return "until cleared"
	}
	return "until " + stats.GateUntil(g, now)
}

func candOrdinal(n int) string {
	switch n {
	case 1:
		return "first"
	case 2:
		return "second"
	case 3:
		return "third"
	case 4:
		return "fourth"
	case 5:
		return "fifth"
	case 6:
		return "sixth"
	case 7:
		return "seventh"
	case 8:
		return "eighth"
	case 9:
		return "ninth"
	case 10:
		return "tenth"
	}
	return fmt.Sprintf("#%d", n)
}
