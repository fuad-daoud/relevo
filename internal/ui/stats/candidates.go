package stats

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/candidate"
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

// statsCandNameW is width-6 less the visible columns, the three-cell gap
// before STATUS, and STATUS itself.
func statsCandNameW(width int, visible []statsCandCol) int {
	sum := 0
	for _, c := range visible {
		sum += c.W
	}
	return statsMinWidth(width - 6 - sum - 3 - statsCandStatusW)
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

// candidatesTabLines is the candidates tab: every overviewCandRows row with
// no windowing, because the page scrolls, then two blank lines and the
// selected row's detail block. sel is the selected row's line, 1+i, which the
// page follows; with no row it is the header alone and sel is -1.
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

// accountTabLines is the candidates tab's account section. It is only reached
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

// statsCandCell is one non-idle row's cell: a lane with nothing measured
// shows "·" where its median, ttft, per-round mean or cache share would be.
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

// statsCandDetail is the selected candidates row's detail block: the name
// line, then the round, token and measured lines. An idle row says it has no
// rounds instead of the last three. Each line starts
// with the three-cell gutter and is cut to width-6.
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

// statsCandRoundLine is the detail block's second line. "round" and
// "binding" are singular at one; the share is omitted when the window measured
// no token at all.
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

// statsCandTokenLine is the detail block's third line, all muted. The "in …
// out" group and its three trailing spaces go when the lane measured no token,
// and the median and the ttft clause go when their value is unknown, so a
// missing value never leaves an empty clause behind.
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
