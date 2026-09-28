package dash

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/histq"
	"github.com/fuad-daoud/relevo/internal/stats"
)

// ShortRepo trims prefixes, suffixes, and returns the last two segments
// (or the key itself when fewer than two).
func ShortRepo(key string) string {
	key = strings.TrimRight(key, "/")
	if strings.HasSuffix(key, "/.git") {
		key = strings.TrimSuffix(key, "/.git")
	} else if strings.HasSuffix(key, ".git") {
		key = strings.TrimSuffix(key, ".git")
	}
	if at := strings.Index(key, "@"); at >= 0 {
		if colon := strings.Index(key[at+1:], ":"); colon >= 0 {
			key = key[at+1+colon+1:]
		}
	}
	parts := strings.Split(key, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return key
}

// lineKind separates the grid's row shapes.
type lineKind int

const (
	lineGroup lineKind = iota
	lineRound
	lineDay
)

type dayRule struct {
	label  string
	rounds int
	tokens int64
}

// line is one visible grid row: a group, a day rule, or a round.
type line struct {
	kind  lineKind
	group histq.GroupRow
	row   db.RoundRow
	day   dayRule
}

// Summary is a pure summary over rows.
type Summary struct {
	Rounds   int
	Tokens   int64
	ByWord   map[string]int // outcome word (§3.5) -> rounds
	MedianMS int64          // median of rows with DurationMS; 0 when none
}

// outcomeWord maps a round's outcome fields to its display word and style (§3.5).
func (m Model) outcomeWord(r db.RoundRow) (string, lipgloss.Style) {
	switch r.Outcome {
	case db.OutcomeOpen:
		if m.Running != nil && m.Running(r.BindingName, r.Number) {
			return "running", m.styles.Accent
		}
		return "open", m.styles.Dim
	case db.OutcomeExited:
		return "exited", m.styles.Danger
	case db.OutcomeHalted:
		return "halted", m.styles.Warn
	case db.OutcomeSwitched:
		return "switched", m.styles.Warn
	case db.OutcomeDoneNoReport:
		return "no output", m.styles.Faint
	case db.OutcomeReported:
		if r.ReportOutcome != nil {
			switch *r.ReportOutcome {
			case "done":
				return "done", m.styles.Ok
			case "halted":
				return "halted", m.styles.Warn
			case "blocked":
				return "blocked", m.styles.Warn
			}
		}
		return "no outcome", m.styles.Faint
	default:
		return r.Outcome, m.styles.Faint
	}
}

// Summary calculates a Summary over m.rows (the whole query result, unsorted).
func (m Model) Summary() Summary {
	s := Summary{
		Rounds: len(m.rows),
		ByWord: make(map[string]int),
	}
	var durations []int64
	for _, r := range m.rows {
		s.Tokens += tokenValue(r)
		word, _ := m.outcomeWord(r)
		s.ByWord[word]++
		if r.DurationMS != nil {
			durations = append(durations, *r.DurationMS)
		}
	}
	s.MedianMS = median(durations)
	return s
}

// SummaryLine produces the styled summary items (§5.4), joined by 3 spaces,
// with no margin and no padding. When maxWidth > 0, items are dropped from the
// end until lipgloss.Width(joined) <= maxWidth. The first two items (rounds, tokens)
// are never dropped; if even those do not fit, they are returned clipped.
// maxWidth <= 0 means no limit.
func (m Model) SummaryLine(maxWidth int) string {
	s := m.Summary()
	var items []string

	item := func(num string, label string) string {
		return m.styles.Strong.Render(num) + " " + m.styles.Dim.Render(label)
	}

	items = append(items, item(strconv.Itoa(s.Rounds), "rounds"))
	items = append(items, item(shortTokens(s.Tokens), "tokens"))
	items = append(items, item(strconv.Itoa(s.ByWord["done"]), "done"))

	for _, word := range []string{"halted", "blocked", "exited", "switched", "running", "open"} {
		if cnt := s.ByWord[word]; cnt > 0 {
			items = append(items, item(strconv.Itoa(cnt), word))
		}
	}

	if s.MedianMS > 0 {
		items = append(items, item(shortDuration(s.MedianMS), "median"))
	}

	if maxWidth <= 0 {
		return strings.Join(items, "   ")
	}

	for len(items) > 2 {
		joined := strings.Join(items, "   ")
		if lipgloss.Width(joined) <= maxWidth {
			return joined
		}
		items = items[:len(items)-1]
	}

	joined := strings.Join(items, "   ")
	if lipgloss.Width(joined) <= maxWidth {
		return joined
	}
	return clip(joined, maxWidth)
}

// Problem returns Notice if set, else "query failed: " + fetchErr if set, else "".
func (m Model) Problem() string {
	if m.Notice != "" {
		return m.Notice
	}
	if m.fetchErr != "" {
		return "query failed: " + m.fetchErr
	}
	return ""
}

// FilterText returns stripBy(m.text).
func (m Model) FilterText() string {
	return stripBy(m.text)
}

// SortLabel returns the round sort key when ungrouped, group sort key when grouped.
func (m Model) SortLabel() string {
	key := m.roundSortKey()
	if m.grouped() {
		key = m.groupSortKey()
	}
	if key == "started" {
		if m.sortDesc {
			return "newest"
		}
		return "oldest"
	}
	if m.sortDesc {
		return key + " ↓"
	}
	return key + " ↑"
}

// visible is the flattened list the cursor addresses: one line per row
// when By == none (sorted), else one line per group (sorted) with an
// expanded group's rows indented beneath it.
func (m Model) visible() []line {
	if !m.grouped() {
		rows := m.sortedRows(m.rows)
		if m.roundSortKey() != "started" {
			out := make([]line, len(rows))
			for i := range rows {
				out[i] = line{kind: lineRound, row: rows[i]}
			}
			return out
		}
		var out []line
		i := 0
		for i < len(rows) {
			j := i + 1
			y0, m0, d0 := rows[i].StartedAt.In(m.loc).Date()
			for j < len(rows) {
				yj, mj, dj := rows[j].StartedAt.In(m.loc).Date()
				if yj != y0 || mj != m0 || dj != d0 {
					break
				}
				j++
			}
			run := rows[i:j]
			var runTokens int64
			for _, r := range run {
				runTokens += tokenValue(r)
			}
			lbl := m.dayLabel(run[0].StartedAt)
			out = append(out, line{
				kind: lineDay,
				day: dayRule{
					label:  lbl,
					rounds: len(run),
					tokens: runTokens,
				},
			})
			for _, r := range run {
				out = append(out, line{kind: lineRound, row: r})
			}
			i = j
		}
		return out
	}
	groups := m.sortedGroups(m.groups)
	var out []line
	for _, g := range groups {
		out = append(out, line{kind: lineGroup, group: g})
		if m.expanded[g.Key] {
			for _, r := range m.sortedRows(g.Rows) {
				out = append(out, line{kind: lineRound, row: r})
			}
		}
	}
	return out
}

// gridContent is the grid's whole text, one line per visible row; the
// empty state is prose, never an error.
func (m Model) gridContent() string {
	lines := m.visible()
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	if len(lines) == 0 {
		return "   " + fit(m.styles.Empty.Render("no rounds"), cw) + "   "
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = m.renderLine(l, i == m.cursor)
	}
	return strings.Join(out, "\n")
}

// windowTop is the first grid line to draw so the cursor stays visible: it
// sits mid-window, clamped to the ends.
func (m Model) windowTop() int {
	n := len(m.visible())
	h := m.gridHeight()
	if h <= 0 || n <= h {
		return 0
	}
	top := m.cursor - h/2
	if top < 0 {
		top = 0
	}
	if top > n-h {
		top = n - h
	}
	return top
}

// renderLine draws one grid row.
func (m Model) renderLine(l line, cursor bool) string {
	switch l.kind {
	case lineGroup:
		return m.groupLine(l.group, cursor)
	case lineDay:
		return m.dayRuleLine(l.day)
	default:
		return m.roundLine(l.row, cursor, m.grouped())
	}
}

// dayRuleLine renders one day rule line.
func (m Model) dayRuleLine(d dayRule) string {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	label := d.label
	rightText := " " + plural(d.rounds, "round") + " · " + shortTokens(d.tokens)
	leftWidth := lipgloss.Width(label) + 1
	rightWidth := lipgloss.Width(rightText)
	fill := cw - leftWidth - rightWidth
	if fill < 0 {
		fill = 0
	}
	content := m.styles.Dim.Render(label) + " " + m.styles.Grid.Render(strings.Repeat("┈", fill)) + m.styles.Faint.Render(rightText)
	return "   " + fit(content, cw) + "   "
}

type roundColLayout struct {
	hasRepo    bool
	hasTreeCom bool
	hasTokens  bool
	bindWidth  int
}

func (m Model) roundLayout(indented bool) roundColLayout {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	avail := cw
	if indented {
		avail -= 2
	}
	layout := roundColLayout{
		hasRepo:    true,
		hasTreeCom: true,
		hasTokens:  true,
	}
	if avail-106 >= 12 {
		layout.bindWidth = avail - 106
		return layout
	}
	layout.hasRepo = false
	if avail-82 >= 12 {
		layout.bindWidth = avail - 82
		return layout
	}
	layout.hasTreeCom = false
	if avail-66 >= 12 {
		layout.bindWidth = avail - 66
		return layout
	}
	layout.hasTokens = false
	w := avail - 57
	if w < 12 {
		w = 12
	}
	layout.bindWidth = w
	return layout
}

// roundLine is one round row (§5.2).
func (m Model) roundLine(r db.RoundRow, cursor, indented bool) string {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	layout := m.roundLayout(indented)
	var parts []string

	add := func(text string, width int, alignRight bool, s lipgloss.Style) {
		text = clip(text, width)
		var cell string
		if alignRight {
			if w := lipgloss.Width(text); w < width {
				cell = strings.Repeat(" ", width-w) + text
			} else {
				cell = text
			}
		} else {
			cell = pad(text, width)
		}
		if cursor {
			s = s.Background(m.styles.Selected.GetBackground())
		}
		parts = append(parts, s.Render(cell))
	}

	// STARTED: StartedAt.In(loc).Format("15:04"), Dim, width 7
	add(r.StartedAt.In(m.loc).Format("15:04"), 7, false, m.styles.Dim)

	// BINDING: binding name, Fg (Strong on cursor row), width layout.bindWidth
	bStyle := m.styles.Fg
	if cursor {
		bStyle = m.styles.Strong
	}
	add(r.BindingName, layout.bindWidth, false, bStyle)

	// RND: r<n>, Dim, width 4
	add(fmt.Sprintf("r%d", r.Number), 4, false, m.styles.Dim)

	// CANDIDATE: nameOf(Candidate), · when nil, Dim, width 21
	candText := "·"
	if r.Candidate != nil {
		candText = m.nameOf(*r.Candidate)
	}
	add(candText, 21, false, m.styles.Dim)

	// REPO: ShortRepo(*Repo), (no repo) when nil, Dim, width 22
	if layout.hasRepo {
		repoText := "(no repo)"
		if r.Repo != nil {
			repoText = ShortRepo(*r.Repo)
		}
		add(repoText, 22, false, m.styles.Dim)
	}

	// OUTCOME: outcomeWord, outcomeStyle, width 10
	outWord, outStyle := m.outcomeWord(r)
	add(outWord, 10, false, outStyle)

	// COMMITS & TREE
	if layout.hasTreeCom {
		if r.Commits != nil {
			cStyle := m.styles.Fg
			if *r.Commits == 0 {
				cStyle = m.styles.Faint
			}
			add(strconv.Itoa(*r.Commits), 7, true, cStyle)
		} else {
			add("·", 7, true, m.styles.Faint)
		}

		if r.Tree != nil {
			tStyle := m.styles.Faint
			if *r.Tree == "dirty" {
				tStyle = m.styles.Warn
			}
			add(*r.Tree, 5, false, tStyle)
		} else {
			add("·", 5, false, m.styles.Faint)
		}
	}

	// TOKENS: shortTokens(sum); · when all four token columns are nil, Fg, width 7
	if layout.hasTokens {
		if r.InTokens == nil && r.CacheTokens == nil && r.WriteTokens == nil && r.OutTokens == nil {
			add("·", 7, true, m.styles.Fg)
		} else {
			add(shortTokens(tokenValue(r)), 7, true, m.styles.Fg)
		}
	}

	// TOOK: shortDuration; for running round now-StartedAt; for nil DurationMS it is ·, Dim, width 5
	tookText := "·"
	if outWord == "running" {
		durMS := m.now().Sub(r.StartedAt).Milliseconds()
		tookText = shortDuration(durMS)
	} else if r.DurationMS != nil {
		tookText = shortDuration(*r.DurationMS)
	}
	add(tookText, 5, true, m.styles.Dim)

	sep := "  "
	if cursor {
		sep = m.styles.Selected.Render("  ")
	}
	line := strings.Join(parts, sep)
	if indented {
		indent := "  "
		if cursor {
			indent = m.styles.Selected.Render("  ")
		}
		line = indent + line
	}
	if cursor {
		if lipgloss.Width(line) > cw {
			line = lipgloss.NewStyle().MaxWidth(cw).Render(line)
		}
		if w := lipgloss.Width(line); w < cw {
			line += m.styles.Selected.Render(strings.Repeat(" ", cw-w))
		}
		return "   " + line + "   "
	}
	content := fit(line, cw)
	return "   " + content + "   "
}

// roundHeader labels the round grid's columns.
func (m Model) roundHeader() string {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	layout := m.roundLayout(false)
	var parts []string
	add := func(label string, width int, alignRight bool) {
		if alignRight {
			parts = append(parts, fmt.Sprintf("%*s", width, label))
		} else {
			parts = append(parts, pad(label, width))
		}
	}
	add("STARTED", 7, false)
	add("BINDING", layout.bindWidth, false)
	add("RND", 4, false)
	add("CANDIDATE", 21, false)
	if layout.hasRepo {
		add("REPO", 22, false)
	}
	add("OUTCOME", 10, false)
	if layout.hasTreeCom {
		add("COMMITS", 7, true)
		add("TREE", 5, false)
	}
	if layout.hasTokens {
		add("TOKENS", 7, true)
	}
	add("TOOK", 5, true)

	line := strings.Join(parts, "  ")
	styled := m.styles.Faint.Bold(true).Render(line)
	return "   " + fit(styled, cw) + "   "
}

type groupColLayout struct {
	hasBindings  bool
	hasCommits   bool
	hasTokensPct bool
	keyWidth     int
}

func (m Model) groupLayout() groupColLayout {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	hasTokensPct := m.width >= 110
	hasCommits := m.width >= 90
	hasBindings := m.width >= 90 && m.query.By != histq.AxisBinding

	numCols := 6                    // KEY, RNDS, DONE, HALTED, TOKENS, LAST
	fixedWidth := 5 + 5 + 6 + 7 + 9 // 32
	if hasBindings {
		numCols++
		fixedWidth += 8
	}
	if hasCommits {
		numCols++
		fixedWidth += 7
	}
	if hasTokensPct {
		numCols++
		fixedWidth += 15
	}
	seps := 2 * (numCols - 1)
	kw := cw - fixedWidth - seps
	if kw < 12 {
		kw = 12
	}
	return groupColLayout{
		hasBindings:  hasBindings,
		hasCommits:   hasCommits,
		hasTokensPct: hasTokensPct,
		keyWidth:     kw,
	}
}

// groupHeader labels the group grid's columns.
func (m Model) groupHeader() string {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	layout := m.groupLayout()
	var parts []string
	add := func(label string, width int, alignRight bool) {
		if alignRight {
			parts = append(parts, fmt.Sprintf("%*s", width, label))
		} else {
			parts = append(parts, pad(label, width))
		}
	}

	keyLabel := strings.ToUpper(string(m.query.By))
	if m.query.By == histq.AxisCandidate {
		keyLabel = "CANDIDATE"
	}
	add(keyLabel, layout.keyWidth, false)
	add("RNDS", 5, true)
	if layout.hasBindings {
		add("BINDINGS", 8, true)
	}
	add("DONE", 5, true)
	add("HALTED", 6, true)
	if layout.hasCommits {
		add("COMMITS", 7, true)
	}
	add("TOKENS", 7, true)
	if layout.hasTokensPct {
		add("% TOKENS", 15, true)
	}
	add("LAST", 9, true)

	line := strings.Join(parts, "  ")
	styled := m.styles.Faint.Bold(true).Render(line)
	return "   " + fit(styled, cw) + "   "
}

// groupKeyLabel formats a group key display string.
func (m Model) groupKeyLabel(key string) string {
	if key == "-" || key == "" {
		switch m.query.By {
		case histq.AxisRepo:
			return "(no repo)"
		case histq.AxisFeature:
			return "(no feature)"
		case histq.AxisTicket:
			return "(no ticket)"
		default:
			return "(none)"
		}
	}
	switch m.query.By {
	case histq.AxisRepo:
		return ShortRepo(key)
	case histq.AxisCandidate:
		return m.nameOf(key)
	default:
		return key
	}
}

// tokensBar formats a 15-cell tokens bar.
func (m Model) tokensBar(pct int, band bool) string {
	barCount := int(math.Round(float64(pct) / 10.0))
	if barCount < 0 {
		barCount = 0
	}
	if barCount > 10 {
		barCount = 10
	}
	barStr := strings.Repeat("▇", barCount)
	barPadded := pad(barStr, 10)
	accent := m.styles.Accent
	fg := m.styles.Fg
	space := " "
	if band {
		bg := m.styles.Selected.GetBackground()
		accent = accent.Background(bg)
		fg = fg.Background(bg)
		space = m.styles.Selected.Render(" ")
	}
	return accent.Render(barPadded) + space + fg.Render(fmt.Sprintf("%3d%%", pct))
}

// groupLine is one group row (§5.3).
func (m Model) groupLine(g histq.GroupRow, cursor bool) string {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	layout := m.groupLayout()
	var parts []string

	add := func(text string, width int, alignRight bool, s lipgloss.Style) {
		text = clip(text, width)
		var cell string
		if alignRight {
			if w := lipgloss.Width(text); w < width {
				cell = strings.Repeat(" ", width-w) + text
			} else {
				cell = text
			}
		} else {
			cell = pad(text, width)
		}
		if cursor {
			s = s.Background(m.styles.Selected.GetBackground())
		}
		parts = append(parts, s.Render(cell))
	}

	// KEY
	kStyle := m.styles.Fg
	if cursor {
		kStyle = m.styles.Strong
	}
	add(m.groupKeyLabel(g.Key), layout.keyWidth, false, kStyle)

	// RNDS
	add(strconv.Itoa(g.Rounds), 5, true, m.styles.Dim)

	// BINDINGS
	if layout.hasBindings {
		distinct := make(map[string]bool)
		for _, r := range g.Rows {
			distinct[r.BindingName] = true
		}
		add(strconv.Itoa(len(distinct)), 8, true, m.styles.Dim)
	}

	// DONE & HALTED
	doneCnt := 0
	haltCnt := 0
	for _, r := range g.Rows {
		w, _ := m.outcomeWord(r)
		if w == "done" {
			doneCnt++
		} else if w == "halted" {
			haltCnt++
		}
	}
	add(strconv.Itoa(doneCnt), 5, true, m.styles.Dim)
	add(strconv.Itoa(haltCnt), 6, true, m.styles.Dim)

	// COMMITS
	if layout.hasCommits {
		add(strconv.Itoa(g.Commits), 7, true, m.styles.Dim)
	}

	// TOKENS
	if g.Tokens == 0 {
		add("·", 7, true, m.styles.Faint)
	} else {
		add(shortTokens(g.Tokens), 7, true, m.styles.Dim)
	}

	// % TOKENS
	if layout.hasTokensPct {
		var total int64
		for _, grp := range m.groups {
			total += grp.Tokens
		}
		var pct int
		if total > 0 {
			pct = int(math.Round(float64(100*g.Tokens) / float64(total)))
		}
		parts = append(parts, m.tokensBar(pct, cursor))
	}

	// LAST
	var lastStr string
	if isToday(g.Last, m.loc, m.now()) {
		lastStr = "today"
	} else {
		lastStr = strings.ToLower(g.Last.In(m.loc).Format("Jan 02"))
	}
	add(lastStr, 9, true, m.styles.Dim)

	sep := "  "
	if cursor {
		sep = m.styles.Selected.Render("  ")
	}
	line := strings.Join(parts, sep)
	if cursor {
		if lipgloss.Width(line) > cw {
			line = lipgloss.NewStyle().MaxWidth(cw).Render(line)
		}
		if w := lipgloss.Width(line); w < cw {
			line += m.styles.Selected.Render(strings.Repeat(" ", cw-w))
		}
		return "   " + line + "   "
	}
	content := fit(line, cw)
	return "   " + content + "   "
}

// headerLine is §5's line 1: identity, the applied query, the axis, and the
// key hints, which drop below 120 columns.
func (m Model) headerLine() string {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	q := stripBy(m.text)
	if q == "" {
		q = "(all rounds)"
	}
	axis := string(m.query.By)
	if axis == "" {
		axis = string(histq.AxisNone)
	}
	left := " relevo · dashboard   "
	right := "/ filter  b regroup  s sort  d fleet  r refresh"

	avail := cw - lipgloss.Width(left) - lipgloss.Width("   by:"+axis)
	if cw >= 120 {
		avail -= lipgloss.Width(right) + 1
	}
	left += clip(q, avail) + "   by:" + axis

	var content string
	if cw >= 120 {
		content = fit(spread(left, right, cw), cw)
	} else {
		content = fit(left, cw)
	}
	return "   " + content + "   "
}

// thirdLine is §5's line 3: the editor while editing (with any parse error
// beside it), the column header otherwise.
func (m Model) thirdLine() string {
	cw := m.width - 6
	if cw < 0 {
		cw = 0
	}
	if m.editing {
		line := "/ " + strings.TrimRight(m.input.View(), " ")
		if m.parseErr != "" {
			room := cw - lipgloss.Width(line) - 3
			if room > 0 {
				line += "   " + m.styles.Error.Render(clip(m.parseErr, room))
			}
		}
		return "   " + fit(line, cw) + "   "
	}
	if m.grouped() {
		return m.groupHeader()
	}
	return m.roundHeader()
}

func isToday(t time.Time, loc *time.Location, now time.Time) bool {
	tIn := t.In(loc)
	nowIn := now.In(loc)
	y1, m1, d1 := tIn.Date()
	y2, m2, d2 := nowIn.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

// DayLabel formats a timestamp as "today", "yesterday", or "mon 02 jan" in loc relative to now.
func DayLabel(t, now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	nowIn := now.In(loc)
	tl := t.In(loc)
	nowY, nowM, nowD := nowIn.Date()
	tY, tM, tD := tl.Date()
	today := time.Date(nowY, nowM, nowD, 0, 0, 0, 0, loc)
	tDate := time.Date(tY, tM, tD, 0, 0, 0, 0, loc)
	switch {
	case tDate.Equal(today):
		return "today"
	case tDate.Equal(today.AddDate(0, 0, -1)):
		return "yesterday"
	default:
		return strings.ToLower(tl.Format("Mon 02 Jan"))
	}
}

func (m Model) dayLabel(t time.Time) string {
	return DayLabel(t, m.now(), m.loc)
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func median(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]int64(nil), xs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func shortTokens(n int64) string {
	return stats.ShortTokens(n)
}

func shortDuration(ms int64) string {
	switch {
	case ms <= 0:
		return "-"
	case ms < 60_000:
		return "<1m"
	}
	minutes := ms / 60_000
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh%02dm", minutes/60, minutes%60)
}

func stripBy(text string) string {
	fields := strings.Fields(text)
	out := fields[:0]
	for _, f := range fields {
		if strings.HasPrefix(f, "by:") {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

func pad(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return clip(s, width)
}

func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return lipgloss.NewStyle().MaxWidth(width-1).Render(s) + "…"
}

func fit(s string, width int) string {
	if width <= 0 {
		return s
	}
	w := lipgloss.Width(s)
	if w == width {
		return s
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

func spread(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) nameOf(token string) string {
	if token == "" || token == "-" {
		return token
	}
	if m.Names != nil {
		return m.Names(token)
	}
	return token
}
