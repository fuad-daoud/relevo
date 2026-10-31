package stats

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	datastats "github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// GateLeft is a gate's STATUS text: "gated" when it has no expiry,
// else the time left rounded down -- minutes (at least 1m) under an hour,
// hours under a day, then days.
func GateLeft(g availability.Gate, now time.Time) string {
	if g.Until.IsZero() {
		return "gated"
	}
	d := g.Until.Sub(now)
	switch {
	case d < time.Hour:
		m := int(d.Minutes())
		if m < 1 {
			m = 1
		}
		return fmt.Sprintf("gated %dm", m)
	case d < 24*time.Hour:
		return fmt.Sprintf("gated %dh", int(d.Hours()))
	default:
		return fmt.Sprintf("gated %dd", int(d.Hours()/24))
	}
}

// statsReliableGates is the gates the reliability tab counts and shows:
// the active gates that scope to the builder role. A gate scoped to another
// role is not a builder waiting on a limit.
func (v statsView) statsReliableGates() []availability.Gate {
	var out []availability.Gate
	for _, g := range v.rep.Reliability.Active {
		if g.Role == "" || g.Role == "builder" {
			out = append(out, g)
		}
	}
	return out
}

// GateUntil is a gate row's UNTIL text: the time of day when the
// expiry is today, the date and time otherwise, and "until cleared" for a gate
// with no expiry.
func GateUntil(g availability.Gate, now time.Time) string {
	if g.Until.IsZero() {
		return "until cleared"
	}
	u, n := g.Until.Local(), now.Local()
	if u.Year() == n.Year() && u.Month() == n.Month() && u.Day() == n.Day() {
		return u.Format("15:04")
	}
	return u.Format("Jan 2 15:04")
}

// statsShortErrorRe pulls the agy JSON error form's short_error value
var statsShortErrorRe = regexp.MustCompile(`"short_error":"([^"]*)"`)

// statsGateReasonParens matches one parenthesised aside and the space before
// it, for GateReason to strip.
var statsGateReasonParens = regexp.MustCompile(`\s?\([^()]*\)`)

// statsGateReasonStatus matches a leading HTTP status code prefix for
// GateReason to strip.
var statsGateReasonStatus = regexp.MustCompile(`(?i)^(error\s+)?\d{3}:\s*`)

// statsGateReasonPrefixes are the leading protocol prefixes GateReason
// strips, case-insensitively, in order.
var statsGateReasonPrefixes = []string{"AGY_ERROR:", "error:", "RESOURCE_EXHAUSTED (code 429):"}

// GateReason is the reliability tab's REASON cell: the agy JSON
// form's short_error value when present, then its readable lead clause --
// any leading protocol prefix and parenthesised aside stripped, cut at the
// first ". " or ": ", and a trailing "." dropped. "·" when nothing is left.
func GateReason(note string) string {
	if m := statsShortErrorRe.FindStringSubmatch(note); m != nil {
		note = m[1]
	}
	note = strings.TrimSpace(note)
	note = statsGateReasonStatus.ReplaceAllString(note, "")
	for {
		stripped := false
		for _, prefix := range statsGateReasonPrefixes {
			if len(note) >= len(prefix) && strings.EqualFold(note[:len(prefix)], prefix) {
				note = strings.TrimSpace(note[len(prefix):])
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
	}
	note = statsGateReasonParens.ReplaceAllString(note, "")
	cut := -1
	if i := strings.Index(note, ". "); i >= 0 {
		cut = i
	}
	if i := strings.Index(note, ": "); i >= 0 && (cut < 0 || i < cut) {
		cut = i
	}
	if cut >= 0 {
		note = note[:cut]
	}
	note = strings.TrimSpace(note)
	note = strings.TrimSuffix(note, ".")
	note = strings.TrimSpace(note)
	if note == "" {
		return "·"
	}
	return note
}

// GateReasonText is GateReason(note) when there is a reason to show,
// and "" when there is not: an empty note and the "·" placeholder both mean
// "no reason". A gate line joins it with " · " only when it is
// non-empty.
func GateReasonText(note string) string {
	r := GateReason(note)
	if r == "·" {
		return ""
	}
	return r
}

// statsGatesReasonW is the gates table's default REASON cell width,
// and statsGatesReasonMinW, statsGatesNameMinW its floors: reasonW shrinks
// first when nameW would fall under 16, down to reasonW's own floor of 12.
const (
	statsGatesReasonW    = 30
	statsGatesReasonMinW = 12
	statsGatesNameMinW   = 16
)

// statsGatesCols is the gates table's responsive columns: the table
// spans width-6 cells from column 3, with PROVIDER 14, UNTIL 16, LEFT 6 and a
// 3-cell gap fixed, CANDIDATE taking the rest and REASON the default 30. When
// nameW would fall under 16, reasonW shrinks first, to a floor of 12, before
// nameW is let fall to its own floor of 16.
func statsGatesCols(width int) (nameW, reasonW int) {
	reasonW = statsGatesReasonW
	nameW = width - 6 - 14 - 16 - 6 - 3 - reasonW
	if nameW < statsGatesNameMinW {
		reasonW -= statsGatesNameMinW - nameW
		if reasonW < statsGatesReasonMinW {
			reasonW = statsGatesReasonMinW
		}
		nameW = width - 6 - 14 - 16 - 6 - 3 - reasonW
		if nameW < statsGatesNameMinW {
			nameW = statsGatesNameMinW
		}
	}
	return nameW, reasonW
}

// statsGatesLines is the reliability tab's gates table: the
// header and one row per counted gate, in Active order, or one faint line
// when none is. The table fills [3, width-3): CANDIDATE takes the room left
// after PROVIDER, UNTIL, LEFT and the gap, and REASON is cut with statsCut
// and padded to reasonW so every row ends at exactly width-3.
func (v statsView) statsGatesLines(env widget.Env, width int) []string {
	gates := v.statsReliableGates()
	if len(gates) == 0 {
		return []string{"   " + widget.FaintStyle.Render("no candidate is gated")}
	}
	names := env.Src.Base().Candidates.NameOf
	nameW, reasonW := statsGatesCols(width)
	out := []string{"   " + widget.FaintStyle.Bold(true).Render(
		fmt.Sprintf("%-*s%-*s%-*s%*s   %-*s", nameW, "CANDIDATE", 14, "PROVIDER", 16, "UNTIL", 6, "LEFT", reasonW, "REASON"))}

	for _, g := range gates {
		provider := "·"
		if ref, err := candidate.ParseRef(g.Token); err == nil {
			provider = ref.Provider
		}
		note := GateReason(g.Note)
		out = append(out, "   "+
			widget.TextStyle.Render(datastats.FitKey(names(g.Token), nameW, false))+
			widget.MutedStyle.Render(datastats.FitKey(provider, 14, false))+
			widget.MutedStyle.Render(datastats.FitKey(GateUntil(g, env.Now), 16, false))+
			widget.RedStyle.Render(fmt.Sprintf("%6s", strings.TrimPrefix(GateLeft(g, env.Now), "gated ")))+
			"   "+widget.FaintStyle.Render(fmt.Sprintf("%-*s", reasonW, statsCut(note, reasonW))))
	}
	return out
}

// statsHourCell is one LIMITS BY HOUR cell: widget.GridStyle's "·" at
// zero, widget.TextStyle's count at one or two, and red bold at three or more, each
// right-aligned in cellW.
func statsHourCell(count, cellW int) string {
	text := strconv.Itoa(count)
	switch {
	case count >= 3:
		return strings.Repeat(" ", cellW-len(text)) + widget.RedStyle.Bold(true).Render(text)
	case count > 0:
		return strings.Repeat(" ", cellW-len(text)) + widget.TextStyle.Render(text)
	}
	return strings.Repeat(" ", cellW-1) + widget.GridStyle.Render("·")
}

// statsHourLines is the reliability tab's limits-by-hour table:
// the header names the hours 0-23, each right-aligned in cellW, then one row
// per provider, or one faint line when no provider recorded a limit in the
// window. cellW is (width-6-labelW)/24, at least 3, so the table fills
// [3, width-3) with at most (width-6-labelW)%24 spare cells at the right.
func (v statsView) statsHourLines(width int) []string {
	rows := v.rep.Reliability.ByHour
	if len(rows) == 0 {
		return []string{"   " + widget.FaintStyle.Render("no rate limits in this window")}
	}
	const labelW = 16
	cellW := max(3, (width-6-labelW)/24)
	var head strings.Builder
	fmt.Fprintf(&head, "%-*s", labelW, "LIMITS BY HOUR")
	for h := 0; h < 24; h++ {
		fmt.Fprintf(&head, "%*d", cellW, h)
	}
	out := []string{"   " + widget.FaintStyle.Bold(true).Render(head.String())}
	for _, row := range rows {
		var b strings.Builder
		b.WriteString("   " + widget.MutedStyle.Render(fmt.Sprintf("%-*s", labelW, row.Provider)))
		for h := 0; h < 24; h++ {
			b.WriteString(statsHourCell(row.Counts[h], cellW))
		}
		out = append(out, b.String())
	}
	return out
}

// reliabilityTabLines is the reliability tab: the four tiles, the gates
// table and the limits-by-hour table, each pair separated by two blank lines.
// The tab has no cursor and no detail block.
func (v statsView) reliabilityTabLines(env widget.Env, width int) []string {
	rel := v.rep.Reliability
	tiles := [][3]string{
		{"SWITCHES", strconv.Itoa(rel.Switches),
			fmt.Sprintf("in %d rounds · %.0f%% of rounds", rel.RoundsSwitched, rel.SwitchPct)},
		{"RATE LIMITS", strconv.Itoa(rel.RateLimits),
			fmt.Sprintf("across %d providers", len(rel.ByHour))},
		{"SPAWN FAILURES", strconv.Itoa(rel.SpawnFailures), "builders that failed to start"},
		{"GATED NOW", strconv.Itoa(len(v.statsReliableGates())), "candidates waiting on a limit"},
	}

	out := statsTilesLines(tiles, width)
	out = append(out, "", "")
	out = append(out, v.statsGatesLines(env, width)...)
	out = append(out, "", "")
	return append(out, v.statsHourLines(width)...)
}
