package stats

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
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

// statsBuckets sums consecutive values into at most plotW columns, as few
// values per column as needed.
func statsBuckets(vals []float64, plotW int) []float64 {
	if len(vals) == 0 {
		return nil
	}
	chunk := (len(vals) + plotW - 1) / plotW
	if chunk < 1 {
		chunk = 1
	}
	var out []float64
	for i := 0; i < len(vals); i += chunk {
		sum := 0.0
		for j := i; j < i+chunk && j < len(vals); j++ {
			sum += vals[j]
		}
		out = append(out, sum)
	}
	return out
}

// statsBarRunes are the four-row bars' levels; 0 is empty.
var statsBarRunes = []rune(" ▁▂▃▄▅▆▇█")

// statsSplitNames are the tokens tab's splits, in `s` cycle order: 0 total, 1
// candidate, 2 provider, 3 kind.
var statsSplitNames = []string{"total", "candidate", "provider", "kind"}

// statsSplitChips is the tokens tab's chip row: the four split names, the
// selected one a filled chip and the rest muted, with no label.
func statsSplitChips(split int) string {
	chips := make([]string, len(statsSplitNames))
	for i, name := range statsSplitNames {
		if i == split {
			chips[i] = widget.Chip(widget.KbdStyle.Bold(true), name)
			continue
		}
		chips[i] = widget.MutedStyle.Render(widget.Chip(widget.NormalStyle, name))
	}
	// widget.Chip() pads one cell each side, so one space here leaves three cells
	// between two names, as the sketch shows.
	return "   " + strings.Join(chips, " ")
}

// statsSeries is one series of the tokens tab: a label, one value per day
// and the window's total. Idle marks a configured candidate or provider with
// no tokens in the window, which the stacked charts leave to the idle line
type statsSeries struct {
	Name  string
	Vals  []float64
	Total int64
	Idle  bool
}

// statsSeriesOrder is the series' shared order: by total desc, ties by name.
func statsSeriesOrder(series []statsSeries) []statsSeries {
	sort.SliceStable(series, func(i, j int) bool {
		if series[i].Total != series[j].Total {
			return series[i].Total > series[j].Total
		}
		return series[i].Name < series[j].Name
	})
	return series
}

// statsSeriesFor is the active split's series. shared reports whether
// they compare on one scale: candidate and provider do, kind does not.
func (v statsView) statsSeriesFor(env widget.Env, split int) (series []statsSeries, shared bool) {
	switch split {
	case 2:
		return v.statsProviderSeries(v.rep.Spend.Days), true
	case 3:
		return v.statsKindSeries(v.rep.Spend.Days), false
	case 1:
		return v.statsCandidateSeries(env, v.rep.Spend.Days), true
	}
	return nil, true
}

// statsCandidateSeries is the candidate split: one chart per candidate
// with tokens in the window, then every configured candidate without any. A
// round with no candidate carries no tokens now, so "(none)" is skipped.
func (v statsView) statsCandidateSeries(env widget.Env, days []datastats.DayCost) []statsSeries {
	names := env.Src.Base().Candidates.NameOf
	totals := map[string]int64{}
	for _, d := range days {
		for token, n := range d.ByCandidate {
			if token == "(none)" {
				continue
			}
			totals[token] += n
		}
	}
	series := make([]statsSeries, 0, len(totals)+len(v.configured))
	for token, total := range totals {
		vals := make([]float64, len(days))
		for i, d := range days {
			vals[i] = float64(d.ByCandidate[token])
		}
		series = append(series, statsSeries{Name: names(token), Vals: vals, Total: total})
	}
	seen := map[string]bool{}
	for token := range totals {
		seen[token] = true
	}
	var idle []statsSeries
	for _, token := range v.configured {
		if seen[token] {
			continue
		}
		seen[token] = true
		idle = append(idle, statsSeries{Name: names(token), Vals: make([]float64, len(days)), Idle: true})
	}
	sort.SliceStable(idle, func(i, j int) bool { return idle[i].Name < idle[j].Name })
	return append(statsSeriesOrder(series), idle...)
}

// statsProviderSeries is the provider split: the same charts, from the
// days' tokens per provider. The idle ones are the configured candidates'
// providers, which the token's reference names.
func (v statsView) statsProviderSeries(days []datastats.DayCost) []statsSeries {
	totals := map[string]int64{}
	for _, d := range days {
		for prov, n := range d.TokensByProvider {
			if prov == "(none)" {
				continue
			}
			totals[prov] += n
		}
	}
	series := make([]statsSeries, 0, len(totals)+len(v.configured))
	for prov, total := range totals {
		vals := make([]float64, len(days))
		for i, d := range days {
			vals[i] = float64(d.TokensByProvider[prov])
		}
		series = append(series, statsSeries{Name: prov, Vals: vals, Total: total})
	}
	seen := map[string]bool{}
	for prov := range totals {
		seen[prov] = true
	}
	var idle []statsSeries
	for _, token := range v.configured {
		ref, err := candidate.ParseRef(token)
		if err != nil || seen[ref.Provider] {
			continue
		}
		seen[ref.Provider] = true
		idle = append(idle, statsSeries{Name: ref.Provider, Vals: make([]float64, len(days)), Idle: true})
	}
	sort.SliceStable(idle, func(i, j int) bool { return idle[i].Name < idle[j].Name })
	return append(statsSeriesOrder(series), idle...)
}

// statsKindSeries is the kind split: four charts, always in this order
// and never idle, each on its own scale because cache reads dwarf the rest.
func (v statsView) statsKindSeries(days []datastats.DayCost) []statsSeries {
	kinds := []struct {
		name string
		val  func(datastats.TokenCounts) int64
	}{
		{"cache read", func(k datastats.TokenCounts) int64 { return k.Cache }},
		{"fresh input", func(k datastats.TokenCounts) int64 { return k.In }},
		{"output", func(k datastats.TokenCounts) int64 { return k.Out }},
		{"cache write", func(k datastats.TokenCounts) int64 { return k.Write }},
	}
	series := make([]statsSeries, 0, len(kinds))
	for _, kind := range kinds {
		vals := make([]float64, len(days))
		var total int64
		for i, d := range days {
			n := kind.val(d.Kinds)
			vals[i] = float64(n)
			total += n
		}
		series = append(series, statsSeries{Name: kind.name, Vals: vals, Total: total})
	}
	return series
}

// statsStackedCharts is the candidate, provider and kind splits' body:
// one full timeline chart per non-idle series, stacked in the order the series
// arrive, then one faint line naming the idle ones. shared draws every chart on
// one scale; windowTotal is the window's tokens, which each chart's share is
// of.
func statsStackedCharts(series []statsSeries, days []datastats.DayCost, shared bool, width int, windowTotal int64) []string {
	var active []statsSeries
	var idle []string
	for _, s := range series {
		if s.Idle {
			idle = append(idle, s.Name)
			continue
		}
		active = append(active, s)
	}

	// The y-label width: the widest label any chart in the stack carries, so
	// every plot starts on the same column. The passes settle it against the
	// geometry the renderer will use.
	axisW := 4
	for pass := 0; pass < 3; pass++ {
		w := statsStackTicks(active, shared, width, axisW)
		if w == axisW {
			break
		}
		axisW = w
	}

	// The shared scale: the largest column over every non-idle series, over
	// the renderer's own avail. When shared is false each chart scales itself.
	fixedMax := 0.0
	if shared {
		avail := statsStackAvail(width, axisW)
		for _, s := range active {
			if m := statsStackColMax(s.Vals, avail); m > fixedMax {
				fixedMax = m
			}
		}
	}

	out := make([]string, 0, len(active)*11)
	for i, s := range active {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, statsChartTitle(s.Name, s.Total, windowTotal, width))
		out = append(out, statsTimelineVals(s.Vals, days, width, statsChartOpts{Rows: 8, FixedMax: fixedMax, AxisW: axisW})...)
	}
	if len(idle) > 0 {
		out = append(out, "")
		out = append(out, "   "+widget.FaintStyle.Render(statsCut("no tokens in this window: "+strings.Join(idle, " · "), width-6)))
	}
	return out
}

// statsStackTicks is the y-label width the stack needs at axisW: the widest
// label any of its charts carries, on the shared scale or its own.
func statsStackTicks(active []statsSeries, shared bool, width, axisW int) int {
	avail := statsStackAvail(width, axisW)
	sharedMax := 0.0
	if shared {
		for _, s := range active {
			if m := statsStackColMax(s.Vals, avail); m > sharedMax {
				sharedMax = m
			}
		}
	}
	widest := 0
	for _, s := range active {
		max := statsStackColMax(s.Vals, avail)
		if shared {
			max = sharedMax
		}
		if w := statsStackLabelW(max); w > widest {
			widest = w
		}
	}
	return widest
}

// statsStackAvail is a stacked chart's plot room: the width less the gutter,
// the y-label column and the right margin.
func statsStackAvail(width, axisW int) int {
	avail := width - axisW - 8
	if avail < 1 {
		return 1
	}
	return avail
}

// statsStackColMax is the largest column a series' values draw over avail plot
// cells, with the same statsGeom bucketing the renderer uses.
func statsStackColMax(vals []float64, avail int) float64 {
	var max float64
	for _, c := range statsGeom(len(vals), avail).colVals(vals) {
		if c > max {
			max = c
		}
	}
	return max
}

// statsStackLabelW is the widest y label a chart whose scale maximum is max
// carries: statsNiceStep's step, its tick count and datastats.ShortTokens, exactly
// as the renderer's axis computes them.
func statsStackLabelW(max float64) int {
	step := statsNiceStep(max)
	m := widget.Clamp(int(math.Ceil(max/step)), 1, 4)
	w := lipgloss.Width("0")
	for j := 1; j <= m; j++ {
		if lw := lipgloss.Width(datastats.ShortTokens(int64(math.Round(float64(j) * step)))); lw > w {
			w = lw
		}
	}
	return w
}

// statsChartTitle is one stacked chart's title: the series name in the gutter,
// then its total in bold and its share of the window muted, right aligned to
// end at width-3.
func statsChartTitle(name string, total, windowTotal int64, width int) string {
	left := "   " + widget.TextStyle.Bold(true).Render(name)
	right := widget.TextStyle.Bold(true).Render(datastats.ShortTokens(total))
	if windowTotal != 0 {
		right += widget.MutedStyle.Render(fmt.Sprintf(" · %.0f%%", 100*float64(total)/float64(windowTotal)))
	}
	pad := width - 3 - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

// statsWeekLine is the tokens tab's footer: the window's last seven days
// against the seven before, then the busiest day. The busiest-day clause goes
// when every day is zero, and last week reads 0 under eight days.
func statsWeekLine(days []datastats.DayCost) string {
	var thisWeek, lastWeek int64
	start := len(days) - 7
	if start < 0 {
		start = 0
	}
	for _, d := range days[start:] {
		thisWeek += d.Tokens
	}
	if len(days) >= 8 {
		from := len(days) - 14
		if from < 0 {
			from = 0
		}
		for _, d := range days[from : len(days)-7] {
			lastWeek += d.Tokens
		}
	}
	line := "   " + widget.MutedStyle.Render("this week ") + widget.TextStyle.Bold(true).Render(datastats.ShortTokens(thisWeek)) +
		widget.MutedStyle.Render("   last week ") + widget.TextStyle.Bold(true).Render(datastats.ShortTokens(lastWeek))
	busiest, max := -1, int64(0)
	for i, d := range days {
		if d.Tokens > max {
			busiest, max = i, d.Tokens
		}
	}
	if busiest < 0 {
		return line
	}
	return line + widget.MutedStyle.Render("   busiest day ") + widget.TextStyle.Bold(true).Render(datastats.MonthDay(days[busiest].Day)) +
		widget.MutedStyle.Render(" "+datastats.ShortTokens(max))
}

// tokensTabLines is the tokens tab: the split chips, the total split's
// timeline or the selected split's stacked charts, then the week line. With no
// tokens in the window one faint line replaces the chart.
func (v statsView) tokensTabLines(env widget.Env, width, height int) []string {
	days := v.rep.Spend.Days
	out := []string{statsSplitChips(v.split), ""}

	var hasTokens bool
	for _, d := range days {
		if d.Tokens > 0 {
			hasTokens = true
			break
		}
	}
	if !hasTokens {
		return append(out, "   "+widget.FaintStyle.Render("no token usage recorded in this window"), "", statsWeekLine(days))
	}
	if v.split == 0 {
		out = append(out, "   "+widget.FaintStyle.Bold(true).Render("TOKENS"))
		out = append(out, statsTimelineLines(days, width, widget.Clamp(height-8, 8, 20))...)
	} else {
		series, shared := v.statsSeriesFor(env, v.split)
		out = append(out, statsStackedCharts(series, days, shared, width, v.rep.Totals.TokenKinds.Total())...)
	}
	out = append(out, "")
	return append(out, statsWeekLine(days))
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

// statsRepoShareW is the repos table's share cell: statsShareCell's fixed 17
// cells.
const statsRepoShareW = 17

// statsRepoCol is one repos-tab column after the name column.
type statsRepoCol struct {
	Head string // the header label
	W    int    // the cell width; numbers are right-aligned in it
	// Drop is the drop rank: 0 never drops. The ranks leave in order 1, 2, 3
	// while the name column would fall under 16 cells.
	Drop int
}

// statsRepoCols is the repos table's column set, left to right.
var statsRepoCols = []statsRepoCol{
	{Head: "RNDS", W: 6},
	{Head: "BINDINGS", W: 10},
	{Head: "DONE", W: 6},
	{Head: "HALTED", W: 8},
	{Head: "COMMITS", W: 9, Drop: 1},
	{Head: "TOKENS", W: 9},
}

// statsRepoVisible is the columns the repos table shows at width and the name
// column's room: the table is width-6 wide from column 3, and the
// droppable columns leave in their rank order -- COMMITS, then BINDINGS, then
// DONE -- while the name column would fall under 16 cells.
func statsRepoVisible(width int) ([]statsRepoCol, int) {
	visible := append([]statsRepoCol(nil), statsRepoCols...)
	for _, drop := range []int{1, 2, 3} {
		if statsRepoColNameW(width, visible) >= 16 {
			break
		}
		for i, c := range visible {
			if c.Drop == drop {
				visible = append(append([]statsRepoCol(nil), visible[:i]...), visible[i+1:]...)
				break
			}
		}
	}
	return visible, statsRepoColNameW(width, visible)
}

// statsRepoColNameW is the repos table's name column: width - 6 less
// the visible columns and the share cell.
func statsRepoColNameW(width int, visible []statsRepoCol) int {
	sum := 0
	for _, c := range visible {
		sum += c.W
	}
	return statsMinWidth(width - 6 - sum - statsRepoShareW)
}

// repoTabRows is the repos tab's visible rows: every repo row, plus the
// children of the repos the view has expanded. The cursor walks only these.
func (v statsView) repoTabRows() []repoTabRow {
	repos := v.overviewRepoRows()
	out := make([]repoTabRow, 0, len(repos))
	for _, r := range repos {
		out = append(out, repoTabRow{kind: kindRepo, group: r.GroupRow})
		if v.expanded[r.Key] {
			out = append(out, repoChildren(r)...)
		}
	}
	return out
}

// repoTabRow is one visible repos-tab row: a repo, or one of an expanded repo's
// children. parent is the owning repo's key for a child and "" for a repo row.
// feature is a ticket row's owning feature key, "(none)" under the (no feature)
// bucket and "" for every other row.
type repoTabRow struct {
	kind    repoKind
	group   datastats.GroupRow
	parent  string
	feature string
}

// repoChildren is an expanded repo's child rows in display order: each
// labelled feature by tokens desc with its labelled tickets nested under it by
// tokens desc, then the (no feature) row with its tickets under the same rule.
// The (no feature) row shows when the repo has featureless rounds and either a
// labelled feature or a labelled ticket, so a featureless round's ticket stays
// reachable; a repo with no labels at all has no children.
func repoChildren(r datastats.RepoRow) []repoTabRow {
	if len(r.Scratch) > 0 {
		return scratchRepoChildren(r)
	}
	var out []repoTabRow
	for _, f := range reposByTokens(r.Features, func(f datastats.FeatureRow) int64 { return f.Tokens }) {
		out = append(out, repoTabRow{kind: kindFeature, group: f.GroupRow, parent: r.Key})
		for _, t := range reposByTokens(f.Tickets, func(g datastats.GroupRow) int64 { return g.Tokens }) {
			out = append(out, repoTabRow{kind: kindTicket, group: t, parent: r.Key, feature: f.Key})
		}
	}
	if !repoNoFeatureShown(r) {
		return out
	}
	out = append(out, repoTabRow{kind: kindFeature, group: r.NoFeature.GroupRow, parent: r.Key})
	for _, t := range reposByTokens(r.NoFeature.Tickets, func(g datastats.GroupRow) int64 { return g.Tokens }) {
		out = append(out, repoTabRow{kind: kindTicket, group: t, parent: r.Key, feature: "(none)"})
	}
	return out
}

// scratchRepoChildren is the fold row's child rows: one row per scratch repo by
// tokens desc, each parented to the fold. They are leaf rows -- a repo has no
// children of its own -- so a child expands to nothing.
func scratchRepoChildren(r datastats.RepoRow) []repoTabRow {
	rows := reposByTokens(r.Scratch, func(s datastats.RepoRow) int64 { return s.Tokens })
	out := make([]repoTabRow, 0, len(rows))
	for _, s := range rows {
		out = append(out, repoTabRow{kind: kindRepo, group: s.GroupRow, parent: r.Key})
	}
	return out
}

// repoNoFeatureShown is the (no feature) row's visibility rule: the repo
// has featureless rounds and either a labelled feature or a ticket to keep
// reachable. A repo with no labels at all expands to nothing.
func repoNoFeatureShown(r datastats.RepoRow) bool {
	return r.NoFeature.Rounds > 0 && (len(r.Features) > 0 || len(r.NoFeature.Tickets) > 0)
}

// reposByTokens is the repos tab's per-section ordering: a copy of rows,
// stable-sorted by tokens desc. A feature section and each feature's
// ticket section both use it.
func reposByTokens[Row any](rows []Row, tokens func(Row) int64) []Row {
	out := append([]Row(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool { return tokens(out[i]) > tokens(out[j]) })
	return out
}

// statsRepoHead is a repos-table header row: the name label clipped to
// the name column, the visible column heads right-aligned, and % TOKENS
// right-aligned over its cell, as the overview's repos table does.
func statsRepoHead(label string, nameW int, visible []statsRepoCol) string {
	head := datastats.FitKey(label, nameW, false)
	for _, col := range visible {
		head += fmt.Sprintf("%*s", col.W, col.Head)
	}
	// The share cell is two cells of gutter, a ten-cell bar and a four-cell
	// percent, 17 cells total; the header is right-aligned over the same 17
	// cells so its final "S" sits over the percent's "%".
	head += fmt.Sprintf("%17s", "% TOKENS")
	return "   " + widget.FaintStyle.Bold(true).Render(head)
}

// statsRepoCells is one repos-table row's numbers: each visible
// column's value right-aligned in its cell.
func statsRepoCells(g datastats.GroupRow, visible []statsRepoCol) string {
	var b strings.Builder
	for _, col := range visible {
		fmt.Fprintf(&b, "%*s", col.W, statsRepoCell(g, col.Head))
	}
	return b.String()
}

// statsRepoCell is one repos-table cell's value.
func statsRepoCell(g datastats.GroupRow, head string) string {
	switch head {
	case "RNDS":
		return strconv.Itoa(g.Rounds)
	case "BINDINGS":
		return strconv.Itoa(g.Bindings)
	case "DONE":
		return strconv.Itoa(g.Done)
	case "HALTED":
		return strconv.Itoa(g.ReportHalted)
	case "COMMITS":
		return strconv.Itoa(g.Commits)
	case "TOKENS":
		return datastats.ShortTokens(g.Tokens)
	}
	return "·"
}

// repoKind names which of the repos tab's three tables a row belongs to, so
// its display name and its detail tag come from the right vocabulary.
type repoKind int

const (
	kindRepo repoKind = iota
	kindFeature
	kindTicket
)

// statsRepoRow is one repos-table row: the fitted name, the numbers in
// muted and the share cell. depth is the row's level in the nested table: the
// name is indented 2*depth cells inside the name column -- two for a feature,
// four for a ticket -- the dash grid's convention; its numbers and share stay
// on the repo rows' columns. The selected row is rebuilt as plain text, share
// included, and rendered once with the band, fitted to the row width so it
// ends at width-3.
func statsRepoRow(g datastats.GroupRow, kind repoKind, windowTotal int64, nameW int, visible []statsRepoCol, depth int, selected bool) string {
	name := shortRepo(g.Key)
	switch kind {
	case kindFeature:
		name = shortFeature(g.Key)
	case kindTicket:
		name = shortTicket(g.Key)
	}
	cellW := statsRepoCellW(nameW, visible)
	pad := 2 * depth
	key := strings.Repeat(" ", pad)
	nameW = statsMinWidth(nameW - pad)
	key += datastats.FitKey(name, nameW, false)
	cells := statsRepoCells(g, visible)
	if selected {
		text := key + cells + statsSharePlain(g.Tokens, windowTotal)
		return "   " + widget.SelBandStyle.Foreground(widget.TextStyle.GetForeground()).Bold(true).Render(widget.Fit(text, cellW))
	}
	return "   " + widget.TextStyle.Render(key) + widget.MutedStyle.Render(cells) +
		statsShareCell(g.Tokens, windowTotal)
}

// reposTabLines is the repos tab: one REPO table over the visible rows --
// every repo, plus the children of the expanded ones, each nesting level
// indented two cells further inside the name column -- then two blank lines and
// the selected row's detail block. sel is the selected row's line, so the page
// follows the cursor.
func (v statsView) reposTabLines(env widget.Env, width int) ([]string, int) {
	rows := v.repoTabRows()
	n := len(rows)
	visible, nameW := statsRepoVisible(width)
	windowTotal := v.rep.Totals.TokenKinds.Total()
	if n == 0 {
		return []string{statsRepoHead("REPO", nameW, visible)}, -1
	}
	cur := widget.Clamp(v.cursor[1], 0, n-1)

	out := []string{statsRepoHead("REPO", nameW, visible)}
	sel := -1
	var detail repoTabRow
	for i, r := range rows {
		if i == cur {
			sel = len(out)
			detail = r
		}
		depth := 0
		if r.parent != "" {
			depth = 1
			if r.feature != "" {
				depth = 2
			}
		}
		out = append(out, statsRepoRow(r.group, r.kind, windowTotal, nameW, visible, depth, i == cur))
	}
	out = append(out, "", "")
	out = append(out, v.statsGroupDetail(env, detail.group, detail.kind, windowTotal)...)
	return out, sel
}

// statsGroupDetail is the selected repos-tab row's detail block: the
// name and its kind, the totals, the outcomes and the top candidates. Each
// line starts with the three-cell gutter and is cut to width-6.
func (v statsView) statsGroupDetail(env widget.Env, g datastats.GroupRow, kind repoKind, windowTotal int64) []string {
	w := env.Width - 6
	if w < 1 {
		w = 1
	}
	name := shortRepo(g.Key)
	switch kind {
	case kindFeature:
		name = shortFeature(g.Key)
	case kindTicket:
		name = shortTicket(g.Key)
	}
	first := "   " + widget.FaintStyle.Bold(true).Render(name)
	switch {
	case kind == kindFeature && g.Key != "(none)":
		first += "   " + widget.MutedStyle.Render("feature")
	case kind == kindTicket && g.Key != "(none)":
		first += "   " + widget.MutedStyle.Render("ticket")
	// The fold's key is a placeholder, not a repo, so it gets no echo; a
	// child's is its own path and does.
	case g.Key != "(none)" && g.Key != datastats.ScratchKey:
		first += "   " + widget.MutedStyle.Render(g.Key)
	}
	out := []string{widget.Fit(first, w)}

	roundWord, bindingWord := "rounds", "bindings"
	if g.Rounds == 1 {
		roundWord = "round"
	}
	if g.Bindings == 1 {
		bindingWord = "binding"
	}
	totals := "   " + widget.TextStyle.Bold(true).Render(strconv.Itoa(g.Rounds)) +
		widget.MutedStyle.Render(" "+roundWord+" on ") +
		widget.TextStyle.Bold(true).Render(strconv.Itoa(g.Bindings)) +
		widget.MutedStyle.Render(" "+bindingWord+"   ") +
		widget.TextStyle.Bold(true).Render(datastats.ShortTokens(g.Tokens))
	if windowTotal > 0 {
		pct := int(math.Round(100 * float64(g.Tokens) / float64(windowTotal)))
		totals += widget.MutedStyle.Render(fmt.Sprintf(" tokens, %d%% of the window", pct))
	} else {
		totals += widget.MutedStyle.Render(" tokens")
	}
	out = append(out, widget.Fit(totals, w))

	outcomes := "   " + widget.MutedStyle.Render(fmt.Sprintf("%d done · %d halted · %d commits", g.Done, g.ReportHalted, g.Commits))
	if g.Landed > 0 {
		outcomes += widget.MutedStyle.Render(fmt.Sprintf("   %.1f rounds per landed binding", g.RoundsPerLand))
	}
	out = append(out, widget.Fit(outcomes, w))

	if len(g.ByCandidate) > 0 {
		out = append(out, widget.Fit("   "+widget.MutedStyle.Render("by candidate: "+v.statsGroupCandidates(env, g.ByCandidate)), w))
	}
	return out
}

// statsGroupCandidates is a group's top candidates for the detail block's last
// line: the three largest round counts, ties by name, each as
// "<name> <n> rounds" ("round" at one).
func (v statsView) statsGroupCandidates(env widget.Env, byCandidate map[string]int) string {
	type cand struct {
		name   string
		rounds int
	}
	names := env.Src.Base().Candidates.NameOf
	cands := make([]cand, 0, len(byCandidate))
	for token, rounds := range byCandidate {
		cands = append(cands, cand{name: names(token), rounds: rounds})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].rounds != cands[j].rounds {
			return cands[i].rounds > cands[j].rounds
		}
		return cands[i].name < cands[j].name
	})
	if len(cands) > 3 {
		cands = cands[:3]
	}
	parts := make([]string, 0, len(cands))
	for _, c := range cands {
		word := "rounds"
		if c.rounds == 1 {
			word = "round"
		}
		parts = append(parts, fmt.Sprintf("%s %d %s", c.name, c.rounds, word))
	}
	return strings.Join(parts, " · ")
}
