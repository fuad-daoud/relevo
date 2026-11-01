package stats

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"

	datastats "github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

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
// x-tick rule, shared with the stacked charts so their columns line up.
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

type statsChartOpts struct {
	Rows     int     // the plot rows
	FixedMax float64 // when > 0, the y scale is computed from this value instead of the data's column max
	AxisW    int     // when > 0, the minimum y-label width
}

func tokensOf(days []datastats.DayCost) []float64 {
	toks := make([]float64, len(days))
	for i, d := range days {
		toks[i] = float64(d.Tokens)
	}
	return toks
}

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

// statsTimelinePlotRows draws the H plot rows. A bar cell always wins over a
// grid cell.
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

// statsTimelineVals is the chart body under the heading, over vals (one value
// per day, with days supplying the date labels): H plot rows, the axis
// row and the date-label row. rows is the plot's row count and H is
// max(rows, m), so every y interval keeps at least one row. Each line starts
// with the three-cell gutter and none extends past width-3. A positive
// o.FixedMax replaces the data's column max as the y scale and o.AxisW widens
// the y-label column at its narrowest, so stacked charts share a scale and
// start their plots on one column
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

// statsTimelineLabels is the chart's date row: each x tick column's first
// day as MM-DD. offset is the plot's first cell, so every stacked chart shares
// these rules. A label that would leave the frame or crowd the one to its
// right is skipped; its ┊ and ┴ stay.
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
