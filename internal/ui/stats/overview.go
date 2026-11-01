package stats

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
	datastats "github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// statsOverviewWide is the width at or above which the overview lays its tiles
// in four columns and its two tables side by side. Below it the
// tiles are 2x2 and the tables stack.
const statsOverviewWide = 96

// statsOverviewTablesWide is the width at or above which the overview's two
// tables sit side by side; below it they stack.
const statsOverviewTablesWide = 120

// overviewLines is the overview tab: the four tiles, the token chart and the
// candidates and busiest-repos tables. sel is the visible line of the focused
// table's selected row, or -1 when that table is empty.
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

// statsTileCell is one of a tile's three lines, cut to pitch - 2 with an
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

// statsTokenChartLines is the overview's per-day token chart. With no tokens
// in the window, one faint line in the gutter replaces the plot.
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
// sorted by its display name. A scorecard token missing from the configured
// set stays: its row is data, not config.
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

// statsOverviewCandidates is the overview's CANDIDATES table. Under-5 rows
// dim; when the table overflows, the `a–b of n` count rides in the header's
// name column. sel is the selected row's visible offset, or -1 when the table
// has no rows.
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

// statsOverviewCandidateCells is one CANDIDATES row's two parts, name and
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

// statsOverviewCandidateRow is one CANDIDATES row, the numbers in muted. A
// few-round lane's name and numbers are both faint
func statsOverviewCandidateRow(name string, s datastats.ScoreRow, nameW int, few bool) string {
	key, nums := statsOverviewCandidateCells(name, s, nameW)
	if few {
		return widget.FaintStyle.Render(key + nums)
	}
	return widget.TextStyle.Render(key) + widget.MutedStyle.Render(nums)
}

// statsOverviewRepos is the overview's BUSIEST REPOS table: the visible slice
// of every repo by tokens with its share of the window's tokens. When the
// table overflows, the `a–b of n` count rides in the header's name column.
// sel is the selected row's visible offset, or -1 when the table has no rows.
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
