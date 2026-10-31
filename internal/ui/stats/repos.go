package stats

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	datastats "github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

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
