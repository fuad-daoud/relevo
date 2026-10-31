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
	"github.com/fuad-daoud/relevo/internal/db"
	datastats "github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/view"
)

// statsCandStatusW is the candidates tab's STATUS cell: three cells of gutter
// before it and it ends at width-3.
const statsCandStatusW = 10

// statsCandCol is one candidates-tab column after the name column.
type statsCandCol struct {
	Head string // the header label
	W    int    // the cell width; numbers are right-aligned in it
	// Drop is the drop rank: 0 never drops. The ranks leave in order 1, 2
	// when the name column would fall under 16 cells.
	Drop int
}

// statsCandCols is the candidates tab's column set, left to right.
var statsCandCols = []statsCandCol{
	{Head: "RNDS", W: 6},
	{Head: "DONE", W: 6},
	{Head: "HALTED", W: 8},
	{Head: "MED", W: 6, Drop: 2},
	{Head: "TTFT", W: 7, Drop: 1},
	{Head: "IN/RND", W: 9},
	{Head: "OUT/RND", W: 9},
	{Head: "CACHE", W: 7},
}

// statsCandVisible is the columns the candidates tab shows at width and the
// name column's room: the table is width-6 wide from column 3, and the
// droppable columns leave in their rank order while the name column would fall
// under 16 cells.
func statsCandVisible(width int) ([]statsCandCol, int) {
	visible := append([]statsCandCol(nil), statsCandCols...)
	for _, drop := range []int{1, 2} {
		if statsCandNameW(width, visible) >= 16 {
			break
		}
		for i, c := range visible {
			if c.Drop == drop {
				visible = append(append([]statsCandCol(nil), visible[:i]...), visible[i+1:]...)
				break
			}
		}
	}
	return visible, statsCandNameW(width, visible)
}

// statsCandNameW is the candidates table's name column: width - 6 less
// the visible columns, the three-cell gap and the STATUS cell.
func statsCandNameW(width int, visible []statsCandCol) int {
	sum := 0
	for _, c := range visible {
		sum += c.W
	}
	return statsMinWidth(width - 6 - sum - 3 - statsCandStatusW)
}

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

// statsCandStatus is one candidate's STATUS cell: the first active gate
// on the token that scopes to the builder role names the time left in red;
// with no such gate the candidate is ready, in green. An idle or Few row keeps
// this colour -- only its name and numbers dim.
func (v statsView) statsCandStatus(token string, now time.Time) (string, lipgloss.Style) {
	for _, g := range v.rep.Reliability.Active {
		if g.Token != token {
			continue
		}
		if g.Role != "" && g.Role != "builder" {
			continue
		}
		return GateLeft(g, now), widget.RedStyle
	}
	return "ready", widget.GreenStyle
}

// statsOverviewWide is the width at or above which the overview lays its tiles
// in four columns and its two tables side by side. Below it the
// tiles are 2x2 and the tables stack.
const statsOverviewWide = 96

// statsOverviewTablesWide is the width at or above which the overview's two
// tables sit side by side; below it they stack.
const statsOverviewTablesWide = 120

// overviewLines is the overview tab: the four tiles, the token chart and the
// candidates and busiest-repos tables. Each leaves three cells on the right
// height is the room the overview may fill. sel is the visible line of
// the focused table's selected row, or -1 when that table is empty.
func (v statsView) overviewLines(env widget.Env, width, height int) ([]string, int) {
	t := v.rep.Totals
	k := t.TokenKinds

	cache := "·"
	if pct, ok := k.CachePct(); ok {
		cache = fmt.Sprintf("%.0f%%", pct)
	}
	outPerRound := "· per round"
	if per, ok := k.PerRound(k.Out); ok {
		outPerRound = datastats.ShortTokens(per) + " per round"
	}
	tiles := [][3]string{
		{"ROUNDS", fmt.Sprintf("%d", t.Rounds),
			fmt.Sprintf("%d halted · %d open · %s median",
				v.rep.Outcomes.ByReport["halted"], v.rep.Outcomes.ByRound[db.OutcomeOpen],
				datastats.Duration(t.MedianMS))},
		{"TOKENS", datastats.ShortTokens(k.Total()), fmt.Sprintf("%d rounds measured", k.Measured)},
		{"CACHE", cache, "of input served from cache"},
		{"OUTPUT", datastats.ShortTokens(k.Out), outPerRound},
	}

	// The chart's height depends on the screen only: the block's other
	// lines -- the tiles (3, or 6 below 96 columns), two blanks, the heading,
	// the axis, the labels and a blank -- plus a floor of about eight lines for
	// the tables.
	chartRows := widget.Clamp(height-20, 8, 16)

	out := statsTilesLines(tiles, width)
	out = append(out, "", "")
	out = append(out, v.statsTokenChartLines(width, chartRows)...)
	out = append(out, "")
	fixed := len(out)

	if width >= statsOverviewTablesWide {
		leftW := (width - 9) / 2
		rightW := width - 9 - leftW
		rows := height - fixed - 1
		if rows < 3 {
			rows = 3
		}
		left, leftSel := v.statsOverviewCandidates(env, leftW, rows)
		right, rightSel := v.statsOverviewRepos(rightW, rows)
		// Side by side: both tables start at fixed, and each row's line is its
		// header and the rows before it.
		sel := leftSel
		if v.ovFocus != 0 {
			sel = rightSel
		}
		if sel >= 0 {
			sel += fixed + 1
		}
		return append(out, statsTableColumns(left, right, leftW, rightW, width)...), sel
	}

	rows := (height - fixed - 3) / 2
	if rows < 3 {
		rows = 3
	}
	left, leftSel := v.statsOverviewCandidates(env, width-6, rows)
	right, rightSel := v.statsOverviewRepos(width-6, rows)
	out = append(out, statsStackedTable(left, width)...)
	out = append(out, "")
	// Stacked: the repos rows start after the candidates block, its blank line,
	// and the repos header line.
	repos := len(out)
	out = append(out, statsStackedTable(right, width)...)
	sel := -1
	if v.ovFocus == 0 {
		if leftSel >= 0 {
			sel = fixed + 1 + leftSel
		}
	} else if rightSel >= 0 {
		sel = repos + 1 + rightSel
	}
	return out, sel
}

// statsTilesLines lays the tiles out: four columns pitched (width-4)/4 from
// column 3, or 2x2 below statsOverviewWide.
func statsTilesLines(tiles [][3]string, width int) []string {
	rows := [][]int{{0, 1, 2, 3}}
	pitch := (width - 4) / 4
	if width < statsOverviewWide {
		rows = [][]int{{0, 1}, {2, 3}}
		pitch = (width - 4) / 2
	}
	var out []string
	for _, row := range rows {
		for line := 0; line < 3; line++ {
			var b strings.Builder
			b.WriteString("   ")
			for _, i := range row {
				b.WriteString(widget.Fit(statsTileCell(tiles[i], line, pitch), pitch))
			}
			out = append(out, b.String())
		}
	}
	return out
}

// statsTileCell is one of a tile's three lines: its label faint bold, its
// value text bold, its note faint. A longer line is cut to pitch - 2 with an
// ellipsis before it is styled.
func statsTileCell(tile [3]string, line, pitch int) string {
	text := tile[2]
	style := widget.FaintStyle
	switch line {
	case 0:
		text, style = tile[0], widget.FaintStyle.Bold(true)
	case 1:
		text, style = tile[1], widget.TextStyle.Bold(true)
	}
	return style.Render(statsCut(text, pitch-2))
}

// statsTokenChartLines is the overview's per-day token chart: the heading and
// the timeline chart below it. rows is the plot's row count. With no tokens in
// the window, one faint line in the gutter replaces the plot.
func (v statsView) statsTokenChartLines(width, rows int) []string {
	out := []string{"   " + widget.FaintStyle.Bold(true).Render("TOKENS")}
	days := v.rep.Spend.Days
	for _, d := range days {
		if d.Tokens > 0 {
			return append(out, statsTimelineLines(days, width, rows)...)
		}
	}
	return append(out, "   "+widget.FaintStyle.Render("no token usage recorded in this window"))
}

// statsNiceStep is the smallest "nice" y-axis step at or above max/4: a value
// from {1, 2, 2.5, 5} × 10^k, with 2.5 only above the ones. The step is never
// below 1, so ceil(max/step) is always in 1..4.
func statsNiceStep(max float64) float64 {
	threshold := max / 4
	base := 1.0
	for k := 0; k <= 18; k++ {
		mults := []float64{1, 2, 5}
		if k > 0 {
			mults = []float64{1, 2, 2.5, 5}
		}
		for _, mult := range mults {
			if step := mult * base; step >= threshold {
				return step
			}
		}
		base *= 10
	}
	return max
}

// statsTimeGeom is a time chart's x geometry: how days bucket into columns,
// where each column's bar and centre sit, and which columns carry an x tick.
// statsTimelineVals and the stacked charts share it, so their columns and
// ticks line up exactly.
type statsTimeGeom struct {
	Cols   []int  // Cols[i]..Cols[i+1] are the day indexes column i sums; the last is nDays
	Chunk  int    // the days per column
	Start  []int  // Start[i] is column i's first plot cell; the last entry is avail
	BarW   int    // the bar width, the same for every column
	Center []int  // Center[i] is column i's centre cell
	Tick   []bool // Tick[i] reports whether column i carries an x tick
	Owner  []int  // Owner[x] is the column that owns plot cell x; length avail
}

// statsGeom is the x geometry for nDays days across avail plot cells: at most
// avail/2 columns (statsBuckets' bucketing), the pitch, the bar width and the
// x-tick rule -- exactly what statsTimelineLines computed inline before this
// extraction.
func statsGeom(nDays, avail int) statsTimeGeom {
	if avail < 1 {
		avail = 1
	}
	maxCols := avail / 2
	if maxCols < 1 {
		maxCols = 1
	}
	chunk, ncols := 1, nDays
	if nDays > maxCols {
		chunk = (nDays + maxCols - 1) / maxCols
		ncols = (nDays + chunk - 1) / chunk
	}

	g := statsTimeGeom{
		Chunk:  chunk,
		Cols:   make([]int, ncols+1),
		Start:  make([]int, ncols+1),
		Center: make([]int, ncols),
		Tick:   make([]bool, ncols),
		Owner:  make([]int, avail),
	}
	for i := range g.Cols {
		g.Cols[i] = min(i*chunk, nDays)
	}

	// The plot always fills avail; the pitch spreads the columns over it.
	pitch := 1.0
	if ncols > 0 {
		pitch = float64(avail) / float64(ncols)
	}
	for i := 0; i < ncols; i++ {
		g.Start[i] = int(math.Floor(float64(i) * pitch))
	}
	g.Start[ncols] = avail
	cw := int(math.Floor(pitch))
	gap := 0
	if cw != 1 {
		gap = cw / 4
		if gap < 1 {
			gap = 1
		}
	}
	g.BarW = cw - gap
	for i := 0; i < ncols; i++ {
		g.Center[i] = g.Start[i] + (g.BarW-1)/2
		for x := g.Start[i]; x < g.Start[i+1] && x < avail; x++ {
			g.Owner[x] = i
		}
	}

	// The x tick step: the smallest allowed day count whose columns take ten
	// cells, so a 30-day window at 132 columns ticks weekly.
	xSteps := []int{1, 7, 14, 28, 56, 91, 182, 364}
	stepCols := xSteps[len(xSteps)-1]
	for _, s := range xSteps {
		if float64(s)*pitch >= 10 {
			stepCols = s
			break
		}
	}
	for i := 0; i < ncols; i++ {
		g.Tick[i] = (ncols-1-i)%stepCols == 0
	}
	return g
}

// colVals sums vals into the geometry's columns, as statsBuckets does: the
// bucket count is the geometry's column count, so its chunk is Chunk.
func (g statsTimeGeom) colVals(vals []float64) []float64 {
	return statsBuckets(vals, len(g.Cols)-1)
}

// statsChartOpts are a chart body's options: the plot rows, a fixed y
// scale, and a minimum y-label width for the stacked charts.
type statsChartOpts struct {
	Rows     int     // the plot rows
	FixedMax float64 // when > 0, the y scale is computed from this value instead of the data's column max
	AxisW    int     // when > 0, the minimum y-label width
}

// tokensOf is a day series' token counts, one value per day, for a chart body.
func tokensOf(days []datastats.DayCost) []float64 {
	toks := make([]float64, len(days))
	for i, d := range days {
		toks[i] = float64(d.Tokens)
	}
	return toks
}

// statsTimelineLines is the overview's chart body: the timeline of a day
// series' tokens, drawn by statsTimelineVals.
func statsTimelineLines(days []datastats.DayCost, width, rows int) []string {
	return statsTimelineVals(tokensOf(days), days, width, statsChartOpts{Rows: rows})
}

// statsTimelineScale settles the y axis: the label width corrected against
// the ticks it carries, and the tick count and step from the column max.
func statsTimelineScale(vals []float64, width int, o statsChartOpts) (g statsTimeGeom, cols []float64, axisW, m int, step float64) {
	axisW = 4
	if o.AxisW > axisW {
		axisW = o.AxisW
	}
	for pass := 0; pass < 2; pass++ {
		avail := width - axisW - 8
		if avail < 1 {
			avail = 1
		}
		g = statsGeom(len(vals), avail)
		cols = g.colVals(vals)
		colMax := o.FixedMax
		if colMax <= 0 {
			for _, c := range cols {
				if c > colMax {
					colMax = c
				}
			}
		}
		step = statsNiceStep(colMax)
		m = int(math.Ceil(colMax / step))
		if m < 1 {
			m = 1
		}
		if m > 4 {
			m = 4
		}
		newW := lipgloss.Width("0")
		for j := 1; j <= m; j++ {
			if w := lipgloss.Width(datastats.ShortTokens(int64(math.Round(float64(j) * step)))); w > newW {
				newW = w
			}
		}
		if newW < o.AxisW {
			newW = o.AxisW
		}
		if newW == axisW {
			break
		}
		axisW = newW
	}
	return g, cols, axisW, m, step
}

// statsTimelinePlotRows draws the H plot rows: the y labels and rules on the
// left, then one bar, grid or blank cell per plot column. A bar cell always
// wins over a grid cell.
func statsTimelinePlotRows(cols []float64, g statsTimeGeom, H, m int, step float64, axisW, avail int, axisMax float64) []string {
	// Tick j sits on row H - round(j*H/m), with row 0 at the top, so tick m is
	// always row 0 and the ticks are as even as the division allows.
	tickAtRow := make(map[int]int, m)
	for j := 1; j <= m; j++ {
		tickAtRow[H-int(math.Round(float64(j)*float64(H)/float64(m)))] = j
	}
	tickRow := func(r int) (int, bool) {
		j, ok := tickAtRow[r]
		return j, ok
	}

	out := make([]string, 0, H)
	for r := 0; r < H; r++ {
		var b statsRunBuilder
		j, tick := tickRow(r)
		if tick {
			b.add(statsCellMuted, fmt.Sprintf("%*s", axisW, datastats.ShortTokens(int64(math.Round(float64(j)*step)))))
			b.add(statsCellRule, " ┤")
		} else {
			b.add(statsCellPlain, strings.Repeat(" ", axisW))
			b.add(statsCellRule, " │")
		}
		for x := 0; x < avail; x++ {
			i := g.Owner[x]
			bar := ' '
			if axisMax > 0 && x < g.Start[i]+g.BarW {
				lvl := int(math.Round(8 * float64(H) * cols[i] / axisMax))
				v := lvl - (H-1-r)*8
				if v < 0 {
					v = 0
				}
				if v > 8 {
					v = 8
				}
				bar = statsBarRunes[v]
			}
			switch {
			case bar != ' ':
				b.add(statsCellBar, string(bar))
			case tick:
				b.add(statsCellGrid, "┈")
			case g.Center[i] == x && g.Tick[i]:
				b.add(statsCellGrid, "┊")
			default:
				b.add(statsCellPlain, " ")
			}
		}
		out = append(out, "   "+b.String())
	}
	return out
}

// statsTimelineVals is the chart body under the heading: H plot rows, the axis
// row and the date-label row, over vals, one value per day, with days supplying
// the date labels. rows is the plot's row count and H is max(rows, m), so every
// y interval keeps at least one row and a tiny rows still draws. A faint
// gridline runs at every y tick (┈) and every x tick (┊) inside the plot; a bar
// cell always wins over a grid cell. Each line starts with the three-cell gutter
// and none extends past width-3. A positive o.FixedMax replaces the
// data's column max as the y scale and o.AxisW widens the y-label column at its
// narrowest, so stacked charts share a scale and start their plots on one column
func statsTimelineVals(vals []float64, days []datastats.DayCost, width int, o statsChartOpts) []string {
	rows := o.Rows
	g, cols, axisW, m, step := statsTimelineScale(vals, width, o)

	avail := width - axisW - 8
	if avail < 1 {
		avail = 1
	}
	H := max(rows, m)
	axisMax := float64(m) * step

	out := statsTimelinePlotRows(cols, g, H, m, step, axisW, avail, axisMax)

	var ab statsRunBuilder
	ab.add(statsCellMuted, fmt.Sprintf("%*s", axisW, "0"))
	ab.add(statsCellRule, " └")
	for x := 0; x < avail; x++ {
		if g.Center[g.Owner[x]] == x && g.Tick[g.Owner[x]] {
			ab.add(statsCellRule, "┴")
		} else {
			ab.add(statsCellRule, "─")
		}
	}
	out = append(out, "   "+ab.String())
	out = append(out, statsTimelineLabels(days, cols, g.Chunk, g.Center, func(i int) bool { return g.Tick[i] }, axisW+5, width))
	return out
}

// statsTimelineLabels is the chart's date row: each x tick column's first day
// as MM-DD, five cells centred on the column's centre and placed right to
// left. offset is the plot's first cell, so every stacked chart shares these
// rules. A label that would leave the frame or crowd the one to
// its right is skipped; its ┊ and ┴ stay.
func statsTimelineLabels(days []datastats.DayCost, cols []float64, chunk int, centers []int, tickCol func(int) bool, offset, width int) string {
	type placedLabel struct {
		start int
		text  string
	}
	var placed []placedLabel
	prevStart := -1
	limit := width - 3
	for i := len(cols) - 1; i >= 0; i-- {
		if !tickCol(i) {
			continue
		}
		text := datastats.MonthDay(days[i*chunk].Day)
		w := lipgloss.Width(text)
		start := offset + centers[i] - (w-1)/2
		if start+w > limit {
			start = limit - w
		}
		if start < 0 {
			continue
		}
		if prevStart >= 0 && start+w > prevStart-2 {
			continue
		}
		placed = append(placed, placedLabel{start: start, text: text})
		prevStart = start
	}

	cells := make([]rune, width)
	for i := range cells {
		cells[i] = ' '
	}
	marked := make([]bool, width)
	for _, lab := range placed {
		for k, r := range []rune(lab.text) {
			if x := lab.start + k; x >= 0 && x < width {
				cells[x] = r
				marked[x] = true
			}
		}
	}

	var b statsRunBuilder
	b.add(statsCellPlain, "   ")
	for x := 3; x < width; x++ {
		if marked[x] {
			b.add(statsCellMuted, string(cells[x]))
			continue
		}
		b.add(statsCellPlain, " ")
	}
	return b.String()
}

// statsCellKind names the style a timeline cell uses, so runs of cells can be
// merged without comparing lipgloss styles.
type statsCellKind int

const (
	statsCellPlain statsCellKind = iota
	statsCellBar
	statsCellGrid
	statsCellMuted
	statsCellRule
)

// statsCellStyle maps a cell kind to its style.
func statsCellStyle(kind statsCellKind) lipgloss.Style {
	switch kind {
	case statsCellBar:
		return widget.AccentStyle
	case statsCellGrid:
		return widget.GridStyle
	case statsCellMuted:
		return widget.MutedStyle
	case statsCellRule:
		return widget.BorderStyle
	}
	return widget.NormalStyle
}

// statsRunBuilder renders a chart line cell by cell, merging each maximal run
// of same-styled cells into one styled string rather than one ANSI span per
// cell.
type statsRunBuilder struct {
	out  []string
	kind statsCellKind
	text strings.Builder
	open bool
}

func (b *statsRunBuilder) add(kind statsCellKind, text string) {
	if b.open && kind == b.kind {
		b.text.WriteString(text)
		return
	}
	b.flush()
	b.kind, b.open = kind, true
	b.text.WriteString(text)
}

func (b *statsRunBuilder) flush() {
	if !b.open {
		return
	}
	b.open = false
	if s := b.text.String(); s != "" {
		b.out = append(b.out, statsCellStyle(b.kind).Render(s))
	}
	b.text.Reset()
}

func (b *statsRunBuilder) String() string {
	b.flush()
	return strings.Join(b.out, "")
}

// overviewRepoRows is the BUSIEST REPOS table's order: a copy of the report's
// repos, stable-sorted by tokens desc.
func (v statsView) overviewRepoRows() []datastats.RepoRow {
	rows := append([]datastats.RepoRow(nil), v.rep.Repos...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Tokens > rows[j].Tokens })
	return rows
}

// statsOverviewCand is one overview candidate row: its canonical token, its
// scorecard row (the zero value when Idle) and whether it is a configured
// candidate with no scorecard row in this window.
type statsOverviewCand struct {
	Token string
	Row   datastats.ScoreRow
	Idle  bool
}

// overviewCandRows is the overview CANDIDATES table's order: every
// scorecard row in report order, then every configured token no row carries,
// sorted by its display name. A scorecard token that is no longer configured
// stays: its row is data, not config.
func (v statsView) overviewCandRows(names func(string) string) []statsOverviewCand {
	rows := make([]statsOverviewCand, 0, len(v.rep.Scorecard)+len(v.configured))
	seen := make(map[string]bool, len(v.rep.Scorecard))
	for _, s := range v.rep.Scorecard {
		rows = append(rows, statsOverviewCand{Token: s.Token, Row: s})
		seen[s.Token] = true
	}
	idle := make([]statsOverviewCand, 0, len(v.configured))
	for _, token := range v.configured {
		if seen[token] {
			continue
		}
		idle = append(idle, statsOverviewCand{Token: token, Idle: true})
	}
	sort.SliceStable(idle, func(i, j int) bool { return names(idle[i].Token) < names(idle[j].Token) })
	return append(rows, idle...)
}

// statsOverviewCandidates is the overview's CANDIDATES table: a header
// row and the visible slice of every scorecard row, under-5 rows dimmed. When
// the table overflows, the `a–b of n` count rides in the header's name column.
// cellW is the table's width and rows its viewport. sel is the selected row's
// visible offset, or -1 when the table has no rows.
func (v statsView) statsOverviewCandidates(env widget.Env, cellW, rows int) ([]string, int) {
	nameW := statsCandidateNameW(cellW)
	names := env.Src.Base().Candidates.NameOf
	cands := v.overviewCandRows(names)
	n := len(cands)
	cur := 0
	if n > 0 {
		cur = statsClamp(v.ovCursor[0], n)
	}
	first, last := statsTableWindow(n, cur, rows)
	label := "CANDIDATE"
	if n > rows {
		label = fmt.Sprintf("CANDIDATE   %d–%d of %d", first+1, last, n)
	}
	out := []string{
		widget.FaintStyle.Bold(true).Render(fmt.Sprintf("%-*s%6s%6s%9s%9s%7s",
			nameW, datastats.FitKey(label, nameW, false), "RNDS", "DONE", "IN/RND", "OUT/RND", "CACHE")),
	}
	for i := first; i < last; i++ {
		c := cands[i]
		if c.Idle {
			text := datastats.FitKey(names(c.Token), nameW, false) +
				fmt.Sprintf("%6d%6s%9s%9s%7s", 0, "·", "·", "·", "·")
			if v.ovFocus == 0 && i == cur {
				out = append(out, widget.SelBandStyle.Foreground(widget.TextStyle.GetForeground()).Bold(true).Render(widget.Fit(text, cellW)))
				continue
			}
			out = append(out, widget.FaintStyle.Render(text))
			continue
		}
		s := c.Row
		if v.ovFocus == 0 && i == cur {
			key, nums := statsOverviewCandidateCells(names(s.Token), s, nameW)
			out = append(out, widget.SelBandStyle.Foreground(widget.TextStyle.GetForeground()).Bold(true).Render(widget.Fit(key+nums, cellW)))
			continue
		}
		out = append(out, statsOverviewCandidateRow(names(s.Token), s, nameW, s.Few))
	}
	sel := -1
	if n > 0 {
		sel = cur - first
	}
	return out, sel
}

// statsOverviewCandidateCells is one CANDIDATES row as its two parts, name and
// numbers, so a selected row can also be rebuilt as plain text. IN/RND
// is (In+Cache)/Measured, OUT/RND is Out/Measured, both via ShortTokens; a
// lane with nothing measured shows "·".
func statsOverviewCandidateCells(name string, s datastats.ScoreRow, nameW int) (string, string) {
	k := s.TokenKinds
	inRound, outRound, cache := "·", "·", "·"
	if k.Measured > 0 {
		if n, ok := k.PerRound(k.In + k.Cache); ok {
			inRound = datastats.ShortTokens(n)
		}
		if n, ok := k.PerRound(k.Out); ok {
			outRound = datastats.ShortTokens(n)
		}
		if pct, ok := k.CachePct(); ok {
			cache = fmt.Sprintf("%.0f%%", pct)
		}
	}
	return datastats.FitKey(name, nameW, false),
		fmt.Sprintf("%6d%6s%9s%9s%7s",
			s.Rounds, datastats.PctText(s.DonePct, s.Closed), inRound, outRound, cache)
}

// statsOverviewCandidateRow is one CANDIDATES row: the name in the name column,
// then rounds, done %, input per round, output per round and the cache share,
// the numbers in muted. A few-round lane's name and numbers are both faint
func statsOverviewCandidateRow(name string, s datastats.ScoreRow, nameW int, few bool) string {
	key, nums := statsOverviewCandidateCells(name, s, nameW)
	if few {
		return widget.FaintStyle.Render(key + nums)
	}
	return widget.TextStyle.Render(key) + widget.MutedStyle.Render(nums)
}

// statsOverviewRepos is the overview's BUSIEST REPOS table: a header row
// and the visible slice of every repo by tokens, each with an accent share bar
// and its share of the window's tokens. When the table overflows, the `a–b of
// n` count rides in the header's name column. cellW is the table's width and
// rows its viewport. sel is the selected row's visible offset, or -1 when the
// table has no rows.
func (v statsView) statsOverviewRepos(cellW, rows int) ([]string, int) {
	nameW := statsRepoNameW(cellW)
	all := v.overviewRepoRows()
	n := len(all)
	cur := 0
	if n > 0 {
		cur = statsClamp(v.ovCursor[1], n)
	}
	first, last := statsTableWindow(n, cur, rows)
	label := "REPO"
	if n > rows {
		label = fmt.Sprintf("REPO   %d–%d of %d", first+1, last, n)
	}
	out := []string{
		widget.FaintStyle.Bold(true).Render(fmt.Sprintf("%-*s%6s%9s", nameW, datastats.FitKey(label, nameW, false), "RNDS", "TOKENS") + fmt.Sprintf("%17s", "% TOKENS")),
	}
	total := v.rep.Totals.TokenKinds.Total()
	for i := first; i < last; i++ {
		g := all[i]
		if v.ovFocus == 1 && i == cur {
			text := datastats.FitKey(shortRepo(g.Key), nameW, false) +
				fmt.Sprintf("%6d%9s", g.Rounds, datastats.ShortTokens(g.Tokens)) +
				statsSharePlain(g.Tokens, total)
			out = append(out, widget.SelBandStyle.Foreground(widget.TextStyle.GetForeground()).Bold(true).Render(widget.Fit(text, cellW)))
			continue
		}
		out = append(out, widget.TextStyle.Render(datastats.FitKey(shortRepo(g.Key), nameW, false))+
			widget.TextStyle.Render(fmt.Sprintf("%6d%9s", g.Rounds, datastats.ShortTokens(g.Tokens)))+
			statsShareCell(g.Tokens, total))
	}
	sel := -1
	if n > 0 {
		sel = cur - first
	}
	return out, sel
}

// candidatesTabLines is the candidates tab: one full-width table of
// every configured candidate -- the header row, then every overviewCandRows
// row with no windowing, because the page scrolls -- two blank lines and the
// selected row's detail block. sel is the selected row's line, 1+i, which the
// page follows. A table with no row is its header alone, and sel is -1.
func (v statsView) candidatesTabLines(env widget.Env, width int) ([]string, int) {
	names := env.Src.Base().Candidates.NameOf
	rows := v.overviewCandRows(names)
	visible, nameW := statsCandVisible(width)

	head := datastats.FitKey("CANDIDATE", nameW, false)
	for _, col := range visible {
		head += fmt.Sprintf("%*s", col.W, col.Head)
	}
	out := []string{"   " + widget.FaintStyle.Bold(true).Render(head+"   STATUS")}
	if len(rows) == 0 {
		return out, -1
	}

	cur := statsClamp(v.cursor[0], len(rows))
	for i, c := range rows {
		name := names(c.Token)
		var key, nums string
		if c.Idle {
			key, nums = statsCandIdleCells(name, visible, nameW)
		} else {
			key, nums = statsCandCells(c.Row, name, visible, nameW)
		}
		status, statusStyle := v.statsCandStatus(c.Token, env.Now)
		cell := widget.Fit(status, statsCandStatusW)
		switch {
		case i == cur:
			out = append(out, "   "+
				widget.SelBandStyle.Foreground(widget.TextStyle.GetForeground()).Bold(true).Render(key+nums)+
				widget.SelBandStyle.Inherit(statusStyle).Render("   "+cell))
		case c.Idle || c.Row.Few:
			out = append(out, "   "+widget.FaintStyle.Render(key+nums)+"   "+statusStyle.Render(cell))
		default:
			out = append(out, "   "+widget.TextStyle.Render(key)+widget.MutedStyle.Render(nums)+"   "+statusStyle.Render(cell))
		}
	}
	out = append(out, "", "")
	out = append(out, v.statsCandDetail(env, rows[cur])...)
	if len(v.rep.Accounts) > 0 {
		out = append(out, "", "")
		out = append(out, v.accountTabLines(width)...)
	}
	return out, 1 + cur
}

// accountTabLines is the candidates tab's account section: the same columns as
// the candidate table, one row per login a round drew from. It is only reached
// when the report carries accounts, so a host with none shows the tab it
// always did. Status is per candidate, so this table has none.
func (v statsView) accountTabLines(width int) []string {
	visible, nameW := statsCandVisible(width)
	head := datastats.FitKey("ACCOUNT", nameW, false)
	for _, col := range visible {
		head += fmt.Sprintf("%*s", col.W, col.Head)
	}
	out := []string{"   " + widget.FaintStyle.Bold(true).Render(head)}
	for _, s := range v.rep.Accounts {
		key, nums := statsCandCells(s, s.Token, visible, nameW)
		out = append(out, "   "+widget.TextStyle.Render(key)+widget.MutedStyle.Render(nums))
	}
	return out
}

// statsCandCells is one candidates-tab row's plain parts: the name
// fitted to the name column and the numbers right-aligned in each visible
// column.
func statsCandCells(s datastats.ScoreRow, name string, visible []statsCandCol, nameW int) (string, string) {
	var b strings.Builder
	for _, col := range visible {
		fmt.Fprintf(&b, "%*s", col.W, statsCandCell(s, col.Head))
	}
	return datastats.FitKey(name, nameW, false), b.String()
}

// statsCandIdleCells is an idle candidate's row: 0 rounds and "·" in
// every other visible column.
func statsCandIdleCells(name string, visible []statsCandCol, nameW int) (string, string) {
	var b strings.Builder
	for _, col := range visible {
		value := "·"
		if col.Head == "RNDS" {
			value = "0"
		}
		fmt.Fprintf(&b, "%*s", col.W, value)
	}
	return datastats.FitKey(name, nameW, false), b.String()
}

// statsCandCell is one cell of a non-idle row. A lane with nothing
// measured shows "·" where its median, ttft, per-round mean or cache share
// would be.
func statsCandCell(s datastats.ScoreRow, head string) string {
	k := s.TokenKinds
	switch head {
	case "RNDS":
		return strconv.Itoa(s.Rounds)
	case "DONE":
		return datastats.PctText(s.DonePct, s.Closed)
	case "HALTED":
		return strconv.Itoa(s.ReportHalted)
	case "MED":
		if s.Closed > 0 && s.HasMedian {
			return datastats.Duration(s.MedianMS)
		}
	case "TTFT":
		if s.HasTTFT {
			return datastats.TTFTText(s)
		}
	case "IN/RND":
		if n, ok := k.PerRound(k.In + k.Cache); ok {
			return datastats.ShortTokens(n)
		}
	case "OUT/RND":
		if n, ok := k.PerRound(k.Out); ok {
			return datastats.ShortTokens(n)
		}
	case "CACHE":
		if pct, ok := k.CachePct(); ok {
			return fmt.Sprintf("%.0f%%", pct)
		}
	}
	return "·"
}

// statsCandDetail is the selected candidates row's detail block: the
// name with its harness, provider and roles, then the round, binding and token
// totals, then the measured split, then the rounds that reported token usage.
// An idle row says it has no rounds instead of the last three lines. Each line
// starts with the three-cell gutter and is cut to width-6.
func (v statsView) statsCandDetail(env widget.Env, c statsOverviewCand) []string {
	width := env.Width - 6
	if width < 1 {
		width = 1
	}
	name := env.Src.Base().Candidates.NameOf(c.Token)
	first := "   " + widget.FaintStyle.Bold(true).Render(name)
	if meta := statsCandMeta(env, c.Token); meta != "" {
		first += "   " + widget.MutedStyle.Render(meta)
	}
	out := []string{widget.Fit(first, width)}
	if c.Idle {
		return append(out, widget.Fit("   "+widget.MutedStyle.Render("no rounds in this window"), width))
	}
	return append(out,
		widget.Fit(v.statsCandRoundLine(c.Row), width),
		widget.Fit(statsCandTokenLine(c.Row), width),
		widget.Fit(statsCandMeasuredLine(c.Row), width))
}

// statsCandMeta is a candidate token's "harness · provider · roles":
// the harness and provider come from the token, the roles from the runtime's
// role registry (view.CandidateRoles), which is where the actors config
// keeps them. They are omitted, with their " · ", when the token does not
// parse, the set is nil, or the candidate has no role.
func statsCandMeta(env widget.Env, token string) string {
	ref, err := candidate.ParseRef(token)
	if err != nil {
		return ""
	}
	parts := []string{ref.Harness, ref.Provider}
	rt := env.Src.Base()
	if served := view.CandidateRoles(rt.RoleRegistry(), rt.Candidates, token); len(served) > 0 {
		parts = append(parts, strings.Join(served, ", "))
	}
	return strings.Join(parts, " · ")
}

// statsCandRoundLine is the detail block's second line: the rounds and
// the bindings they ran on, then the lane's tokens and their share of the
// window's. "round" and "binding" are singular at one; the share is omitted
// when the window measured no token at all.
func (v statsView) statsCandRoundLine(s datastats.ScoreRow) string {
	roundWord, bindingWord := "rounds", "bindings"
	if s.Rounds == 1 {
		roundWord = "round"
	}
	if s.Bindings == 1 {
		bindingWord = "binding"
	}
	total := s.TokenKinds.Total()
	line := "   " + widget.TextStyle.Bold(true).Render(strconv.Itoa(s.Rounds)) +
		widget.MutedStyle.Render(" "+roundWord+" on ") +
		widget.TextStyle.Bold(true).Render(strconv.Itoa(s.Bindings)) +
		widget.MutedStyle.Render(" "+bindingWord+"   ") +
		widget.TextStyle.Bold(true).Render(datastats.ShortTokens(total))
	if window := v.rep.Totals.TokenKinds.Total(); window > 0 {
		p := int(math.Round(100 * float64(total) / float64(window)))
		return line + widget.MutedStyle.Render(fmt.Sprintf(" tokens, %d%% of the window", p))
	}
	return line + widget.MutedStyle.Render(" tokens")
}

// statsCandTokenLine is the detail block's third line, all muted: the
// measured input, cache and output tokens, then the median, the ttft, the
// report halts and the switches. The "in … out" group and its three trailing
// spaces go when the lane measured no token, and the median and the ttft
// clause go when their value is unknown, so a missing value never leaves an
// empty clause behind.
func statsCandTokenLine(s datastats.ScoreRow) string {
	k := s.TokenKinds
	var clauses []string
	if s.Closed > 0 && s.HasMedian {
		clauses = append(clauses, "median "+datastats.Duration(s.MedianMS))
	}
	if s.HasTTFT {
		clauses = append(clauses, "ttft "+datastats.TTFTText(s))
	}
	clauses = append(clauses,
		fmt.Sprintf("%d halted", s.ReportHalted),
		fmt.Sprintf("%d switches", s.Switches))
	measured := ""
	if k.Measured > 0 {
		measured = fmt.Sprintf("in %s · cache %s · out %s   ",
			datastats.ShortTokens(k.In), datastats.ShortTokens(k.Cache), datastats.ShortTokens(k.Out))
	}
	return "   " + widget.MutedStyle.Render(measured+strings.Join(clauses, " · "))
}

// statsCandMeasuredLine is the detail block's fourth line, all muted:
// how many of the lane's rounds reported token usage, in plain words. Every
// round and no round read as sentences; the numeric form names the counts and
// uses the singular "round" at one.
func statsCandMeasuredLine(s datastats.ScoreRow) string {
	k := s.TokenKinds
	switch k.Measured {
	case 0:
		return "   " + widget.MutedStyle.Render("no round reported token usage")
	case s.Rounds:
		return "   " + widget.MutedStyle.Render("every round reported token usage")
	}
	roundWord := "rounds"
	if s.Rounds == 1 {
		roundWord = "round"
	}
	return "   " + widget.MutedStyle.Render(fmt.Sprintf("%d of %d %s reported token usage",
		k.Measured, s.Rounds, roundWord))
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
