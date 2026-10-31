package stats

import (
	"fmt"
	"math"
	"strings"

	datastats "github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/ui/dash"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

func statsClamp(i, n int) int {
	if i < 0 {
		return 0
	}
	if i > n-1 {
		return n - 1
	}
	return i
}

// statsMaxTop is the largest top that still shows the body's last line.
func statsMaxTop(n, avail int) int {
	return max(0, n-avail)
}

// Widths are runes: "…" counts as one.
func statsCut(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w < 1 {
		return ""
	}
	return string(r[:w-1]) + "…"
}

func statsMinWidth(w int) int {
	if w < 1 {
		return 1
	}
	return w
}

// statsCandidateNameW is the CANDIDATES table's name column: cellW less the
// fixed widths RNDS 6, DONE 6, IN/RND 9, OUT/RND 9 and CACHE 7.
func statsCandidateNameW(cellW int) int { return statsMinWidth(cellW - 37) }

// statsRepoNameW is the BUSIEST REPOS table's name column: cellW less the
// fixed widths RNDS 6, TOKENS 9 and the share cell 17.
func statsRepoNameW(cellW int) int { return statsMinWidth(cellW - 32) }

func statsRepoCellW(nameW int, visible []statsRepoCol) int {
	w := nameW + statsRepoShareW
	for _, c := range visible {
		w += c.W
	}
	return w
}

// The percent is four cells; the bar is round(pct/10) with no minimum,
// so under 5% draws no bar.
func statsShareParts(tokens, total int64) (bars int, pct string) {
	value := 0.0
	if total > 0 {
		value = 100 * float64(tokens) / float64(total)
	}
	bars = int(math.Round(value / 10))
	if bars < 0 {
		bars = 0
	}
	if bars > 10 {
		bars = 10
	}
	return bars, fmt.Sprintf("%3.0f%%", value)
}

// Always 17 cells: two spaces, a ten-cell bar area, a space and the percent.
func statsSharePlain(tokens, total int64) string {
	bars, pct := statsShareParts(tokens, total)
	return "  " + strings.Repeat("▇", bars) + strings.Repeat(" ", 10-bars) + " " + pct
}

// Styled, always 17 cells like statsSharePlain.
func statsShareCell(tokens, total int64) string {
	bars, pct := statsShareParts(tokens, total)
	bar := ""
	if bars > 0 {
		bar = widget.AccentStyle.Render(strings.Repeat("▇", bars))
	}
	return "  " + bar + strings.Repeat(" ", 10-bars) + " " + widget.TextStyle.Render(pct)
}

// The unrecorded "(none)" bucket reads "(no repo)"; the scratch fold reads "scratch".
func shortRepo(key string) string {
	if key == "(none)" {
		return "(no repo)"
	}
	if key == datastats.ScratchKey {
		return "scratch"
	}
	return dash.ShortRepo(key)
}

// The unlabelled "(none)" bucket reads "(no feature)".
func shortFeature(key string) string {
	if key == "(none)" {
		return "(no feature)"
	}
	return key
}

// The unlabelled "(none)" bucket reads "(no ticket)".
func shortTicket(key string) string {
	if key == "(none)" {
		return "(no ticket)"
	}
	return key
}

// The two overview tables side by side on the same lines: a three-cell
// gutter, the left table leftW wide, three cells, then the right table
// rightW wide, whose last cell is width-4 so the pair ends at width-3.
func statsTableColumns(left, right []string, leftW, rightW, width int) []string {
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, widget.Fit("   "+widget.Fit(l, leftW)+"   "+widget.Fit(r, rightW), width))
	}
	return out
}

// One table under the other with three-cell gutters each side.
func statsStackedTable(cells []string, width int) []string {
	out := make([]string, 0, len(cells))
	for _, c := range cells {
		out = append(out, widget.Fit("   "+widget.Fit(c, width-6), width))
	}
	return out
}

// statsTableWindow is the visible slice [first, last) of n rows that keeps
// cursor visible, with no stored offset.
func statsTableWindow(n, cursor, rows int) (first, last int) {
	if cursor < rows {
		first = 0
	} else {
		first = cursor - rows + 1
	}
	last = first + rows
	if last > n {
		last = n
	}
	return first, last
}
