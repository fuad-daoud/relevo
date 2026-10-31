package dash

import (
	"fmt"
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
	ByWord   map[string]int // outcome word -> rounds
	MedianMS int64          // median of rows with DurationMS; 0 when none
}

// outcomeWord maps a round's outcome fields to its display word and style.
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
	s.MedianMS = stats.Median(durations)
	return s
}

// SummaryLine produces the styled summary items, joined by 3 spaces,
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

// headerLine is the first fixed line: identity, the applied query, the axis,
// and the key hints, which drop below 120 columns.
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

// thirdLine is the third fixed line: the editor while editing (with any parse
// error beside it), the column header otherwise.
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

// cell clips text to width, aligns it, and paints the cursor background
// while the row has focus. Both row shapes share it so round and group
// rows pad and highlight the same way.
func (m Model) cell(text string, width int, alignRight bool, s lipgloss.Style, cursor bool) string {
	text = clip(text, width)
	var out string
	if alignRight {
		if w := lipgloss.Width(text); w < width {
			out = strings.Repeat(" ", width-w) + text
		} else {
			out = text
		}
	} else {
		out = pad(text, width)
	}
	if cursor {
		s = s.Background(m.styles.Selected.GetBackground())
	}
	return s.Render(out)
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
