package ui

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
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

// unusedHeaderLine is the unused table's header row, in faint bold.
func unusedHeaderLine(nameW, reasonW, setByW, cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{
		{text: pad("UNUSED PROVIDER", nameW), style: style},
		{text: pad("REASON", reasonW), style: style},
	}
	if setByW > 0 {
		cells = append(cells, candCell{text: pad("SET BY", setByW), style: style})
	}
	cells = append(cells, candCell{text: pad("STATUS", 9), style: style})
	return candLine(cells, false, cw)
}

// unusedDataLine is one unused provider's table row. The name is bold on
// the cursor row, the reason is the gate note's readable lead, SET BY names
// the recorder, and STATUS is the time left in red.
func unusedDataLine(g view.ProviderGate, sel bool, nameW, reasonW, setByW, cw int, now time.Time) string {
	nameStyle := textStyle
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	setBy := g.Source
	if g.Binding != "" {
		setBy = g.Source + " · " + g.Binding
	}
	cells := []candCell{
		{text: pad(g.Provider, nameW), style: nameStyle},
		{text: pad(statsGateReasonText(g.Note), reasonW), style: mutedStyle},
	}
	if setByW > 0 {
		cells = append(cells, candCell{text: pad(setBy, setByW), style: textStyle})
	}
	cells = append(cells, candCell{text: pad(statsGateLeft(asGate(g), now), 9), style: redStyle})
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

// unusedDetailLines is the unused row's detail block: whose gate it is,
// the gate itself, and how to clear it.
func unusedDetailLines(g view.ProviderGate, now time.Time, width int) []string {
	p := g.Provider
	first := "   " + faintStyle.Bold(true).Render(p) + "   " + mutedStyle.Render("a provider no candidate uses")

	line := "   " + redStyle.Render("gated") +
		mutedStyle.Render(" since "+candSince(g.Since, now)) +
		mutedStyle.Render(", "+candUntilText(asGate(g), now))
	if reason := statsGateReasonText(g.Note); reason != "" {
		line += "   " + textStyle.Render(reason)
	}

	last := "   " + mutedStyle.Render("no candidate uses "+p+"; ") +
		textStyle.Render("u") + mutedStyle.Render(" clears it")

	return []string{fit(first, width), fit(line, width), fit(last, width)}
}
