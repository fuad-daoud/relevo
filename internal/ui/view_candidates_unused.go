package ui

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/ui/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/view"
)

// unusedCols is the unused-provider table's cell widths at width:
// PROVIDER keeps the CANDIDATE column's width, STATUS is fixed at 9 so it
// lines up with the candidates' STATUS, and SET BY is 33 unless that would
// leave REASON under 16 cells, in which case SET BY leaves.
func unusedCols(width, nameW int) (reasonW, setByW int) {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	setByW = 33
	reasonW = cw - nameW - 2 - setByW - 2 - 9 - 2
	if reasonW < 16 {
		setByW = 0
		reasonW = cw - nameW - 2 - 9 - 2
	}
	return reasonW, setByW
}

func unusedHeaderLine(nameW, reasonW, setByW, cw int) string {
	style := widget.FaintStyle.Bold(true)
	cells := []candCell{
		{text: widget.Pad("UNUSED PROVIDER", nameW), style: style},
		{text: widget.Pad("REASON", reasonW), style: style},
	}
	if setByW > 0 {
		cells = append(cells, candCell{text: widget.Pad("SET BY", setByW), style: style})
	}
	cells = append(cells, candCell{text: widget.Pad("STATUS", 9), style: style})
	return candLine(cells, false, cw)
}

func unusedDataLine(g view.ProviderGate, sel bool, nameW, reasonW, setByW, cw int, now time.Time) string {
	nameStyle := widget.TextStyle
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	setBy := g.Source
	if g.Binding != "" {
		setBy = g.Source + " · " + g.Binding
	}
	cells := []candCell{
		{text: widget.Pad(g.Provider, nameW), style: nameStyle},
		{text: widget.Pad(stats.GateReasonText(g.Note), reasonW), style: widget.MutedStyle},
	}
	if setByW > 0 {
		cells = append(cells, candCell{text: widget.Pad(setBy, setByW), style: widget.TextStyle})
	}
	cells = append(cells, candCell{text: widget.Pad(stats.GateLeft(asGate(g), now), 9), style: widget.RedStyle})
	return candLine(cells, sel, cw)
}

// asGate presents a ProviderGate as the ledger.Gate the candidate detail's
// gate helpers already take, so the two tables never word a gate differently.
func asGate(g view.ProviderGate) availability.Gate {
	return availability.Gate{
		Kind:    availability.RateLimited,
		Since:   g.Since,
		Until:   g.Until,
		Note:    g.Note,
		Source:  g.Source,
		Binding: g.Binding,
	}
}

func unusedDetailLines(g view.ProviderGate, now time.Time, width int) []string {
	p := g.Provider
	first := "   " + widget.FaintStyle.Bold(true).Render(p) + "   " + widget.MutedStyle.Render("a provider no candidate uses")

	line := "   " + widget.RedStyle.Render("gated") +
		widget.MutedStyle.Render(" since "+candSince(g.Since, now)) +
		widget.MutedStyle.Render(", "+candUntilText(asGate(g), now))
	if reason := stats.GateReasonText(g.Note); reason != "" {
		line += "   " + widget.TextStyle.Render(reason)
	}

	last := "   " + widget.MutedStyle.Render("no candidate uses "+p+"; ") +
		widget.TextStyle.Render("u") + widget.MutedStyle.Render(" clears it")

	return []string{widget.Fit(first, width), widget.Fit(line, width), widget.Fit(last, width)}
}
