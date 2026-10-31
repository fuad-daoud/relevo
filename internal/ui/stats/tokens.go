package stats

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/candidate"
	datastats "github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

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

// statsBarRunes are the bars' levels; 0 is empty.
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
	// between two names.
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
// round with no candidate carries no tokens, so "(none)" is skipped.
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
