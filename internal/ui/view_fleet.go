package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// fit pads or truncates s to width cells. Truncation is by cell, no
// ellipsis: at a narrow width an ellipsis costs more than it says.
func fit(s string, width int) string {
	w := lipgloss.Width(s)
	if w == width {
		return s
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// ago is a coarse age: minutes under an hour, hours under a day, then days.
// "" for a zero time, so a caller can omit the fact.
func ago(since, now time.Time) string {
	if since.IsZero() {
		return ""
	}
	d := now.Sub(since)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// reportReady is the fleet's rule for a report waiting on the human at this
// cockpit: the binding's mastermind is `you`, and a payload is pending for it.
// Such a row's NOW cell says so, and opening it lands on the report tab --
// read, not claimed, so the payload is left for the route that pushes it.
func reportReady(b view.BindingStatus) bool {
	return b.MasterMindName == "you" && b.Pending != nil
}

// artifactWord is the word a cockpit row uses for a delivered artifact:
// "artifact" for a reader, "report" for a writer. The shape picks it, never a
// role name.
func artifactWord(b view.BindingStatus) string {
	if b.Shape == store.ShapeReader {
		return "artifact"
	}
	return "report"
}

// whatAge is a row's NOW cell: what the binding is on, and for how long,
// from the fields Status has today.
func whatAge(b view.BindingStatus, now time.Time) (what, age string) {
	if reportReady(b) {
		what = artifactWord(b) + " ready"
		if b.Last != nil {
			age = ago(b.Last.TS, now)
		}
		if age == "" && b.Waiting != nil {
			age = ago(b.Waiting.Since, now)
		}
		return what, age
	}
	switch b.Display {
	case "NEEDS YOU":
		if b.Waiting != nil {
			what = b.Waiting.Cause
			if what == "blocked" {
				what = "question"
			}
			age = ago(b.Waiting.Since, now)
		} else if b.Detail != "" {
			what = b.Detail
		} else {
			what = "needs you"
		}
	case "ACTIVE":
		what = b.BuilderStatus
		if b.QuietFor != "" {
			what += " · quiet " + b.QuietFor
		}
	case "PAUSED":
		what = "paused"
		if b.Last != nil && b.Last.Kind == "pause" {
			age = ago(b.Last.TS, now)
		}
	case "DONE":
		what = "done"
		if b.Last != nil {
			age = ago(b.Last.TS, now)
		}
	}
	if b.Display == "NEEDS YOU" && b.Stale != "" {
		what += " · " + b.Stale
	}
	return what, age
}

// clipName is name when it fits width cells, else its first width-1 cells
// and an ellipsis.
func clipName(name string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(name) <= width {
		return name
	}
	return lipgloss.NewStyle().MaxWidth(width-1).Render(name) + "…"
}

// fleetView is ':fleet': grouped sections, fold, card and gated line (§2.3, §4).
type fleetView struct {
	cursor    int    // index into rows(env)
	sticky    string // the selected row's Key(), so the selection survives reordering
	top       int    // first visible line index
	attention bool   // sort order: attention (SortRows true) or name; from the sort pref
	actions   bool   // Actions != nil at construction: the action keys are shown (r1)
	showDone  bool   // '.' toggles done section expansion (§2.3)

	filtering  bool            // the '/' filter input is open and owns every key (A4)
	filter     textinput.Model // the filter input: the same widget as the cmdline (A4)
	filterText string          // the applied filter text, kept after the input closes (A4)

	// memo holds the rows this cycle computed, and the key they were computed
	// from. A fleetView is a value, so the pointer is what survives the copies
	// Update hands back and the shell keeps on its stack.
	memo *rowsMemo
}

// newFleetView builds the table, sorted by attention or by name.
func newFleetView(attention bool) fleetView {
	return fleetView{attention: attention, filter: newFilterInput(), memo: &rowsMemo{}}
}

// rowsKey is what a row set was computed from: the report it was read off (its
// first row, its length and when it arrived), the filter in force, the done
// fold and the sort order. Any one of those changing means the rows are stale.
type rowsKey struct {
	report    *view.BindingStatus
	rows      int
	statusAt  time.Time
	filter    string
	showDone  bool
	attention bool
}

// rowsMemo is the fleet's one row set per cycle. Update, the window, the card
// and the list all read it, so a frame sorts the report once instead of once
// per reader. sorts counts the computations, one SortRows each, so a test can
// pin how often a frame really sorts.
type rowsMemo struct {
	key   rowsKey
	rows  []view.BindingStatus
	sorts int
}

// firstRow names a report's slice by its first element, so two reports can be
// told apart. The memo holds the pointer, so the backing array stays live and
// no later slice can be handed the same address.
func firstRow(rows []view.BindingStatus) *view.BindingStatus {
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// withActions marks whether the shell has an Actions seam, so Keys() shows
// the action keys exactly when one exists (r1).
func (f fleetView) withActions(a bool) fleetView {
	f.actions = a
	return f
}

// newFilterInput is the fleet's one-line filter input: the same textinput the
// command line uses, with a '/' prompt (A4).
func newFilterInput() textinput.Model {
	in := newTextInput()
	in.Prompt = "/"
	return in
}

// rows returns visible rows in GROUP order with fold and filter applied (§4, §5.1).
// The set is computed once per cycle and shared: every reader in a frame --
// Update, the window, the card, the list -- reads the memo.
func (f fleetView) rows(env Env) []view.BindingStatus {
	if f.memo == nil {
		return fleetRows(env.Report.Bindings, f.attention, f.activeFilter(), f.showDone)
	}
	key := rowsKey{
		report:    firstRow(env.Report.Bindings),
		rows:      len(env.Report.Bindings),
		statusAt:  env.StatusAt,
		filter:    f.activeFilter(),
		showDone:  f.showDone,
		attention: f.attention,
	}
	if f.memo.key == key {
		return f.memo.rows
	}
	f.memo.key = key
	f.memo.rows = fleetRows(env.Report.Bindings, f.attention, key.filter, f.showDone)
	f.memo.sorts++
	return f.memo.rows
}

// fleetRows is the sort, the filter and the group fold a report's rows go
// through, in the order a human reads them: what needs a decision, then what
// is working, then what is idle, then what is held, then the rest, and the
// done rows last unless the fold is open or a filter is narrowing the list.
func fleetRows(bindings []view.BindingStatus, attention bool, q string, showDone bool) []view.BindingStatus {
	sorted := view.SortRows(bindings, attention)
	var needsYou, working, idle, held, other, done []view.BindingStatus
	for _, b := range sorted {
		if q != "" && !fleetRowMatches(b, q) {
			continue
		}
		switch groupOf(b) {
		case groupNeedsYou:
			needsYou = append(needsYou, b)
		case groupWorking:
			working = append(working, b)
		case groupIdle:
			idle = append(idle, b)
		case groupHeld:
			held = append(held, b)
		case groupOther:
			other = append(other, b)
		case groupDone:
			done = append(done, b)
		}
	}
	out := make([]view.BindingStatus, 0, len(sorted))
	out = append(out, needsYou...)
	out = append(out, working...)
	out = append(out, idle...)
	out = append(out, held...)
	out = append(out, other...)
	if showDone || q != "" {
		out = append(out, done...)
	}
	return out
}

// activeFilter is the filter in force: the input's text while the input is
// open, the applied text once it has closed (A4).
func (f fleetView) activeFilter() string {
	if f.filtering {
		return f.filter.Value()
	}
	return f.filterText
}

// fleetRowMatches reports whether text is a case-insensitive substring of any
// of the row's shown fields: its key, actor, candidate, mastermind, repo or
// state (A4).
func fleetRowMatches(b view.BindingStatus, text string) bool {
	q := strings.ToLower(text)
	for _, s := range []string{b.Key(), actorCell(b), candidateText(b), b.MasterMindName, b.CWD, b.Display} {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

func (f fleetView) Crumbs() []string { return []string{"fleet"} }

// Capturing is true while the filter input is open: the shell then forwards
// ':', '?', 'q' and 'esc' to it (A4).
func (f fleetView) Capturing() bool { return f.filtering }

// Keys returns the footer's action or standard keys (§2.3).
func (f fleetView) Keys() []KeyHelp {
	if f.actions {
		return []KeyHelp{
			{"enter", "open"},
			{"s", "send"},
			{"x", "stop"},
			{"D", "done"},
			{"g", "gate"},
			{"/", "filter"},
		}
	}
	return []KeyHelp{
		{"enter", "open"},
		{"/", "filter"},
		{"a", "sort"},
	}
}

// HelpKeys implements helpKeyer, listing all fleet keys for the '?' overlay (§2.3).
func (f fleetView) HelpKeys() []KeyHelp {
	keys := []KeyHelp{
		{"↑↓", "move"},
		{"home/end", "first/last"},
		{"enter", "open"},
		{"a", "sort"},
		{".", "done rows"},
		{"/", "filter"},
	}
	if f.actions {
		keys = append(keys,
			KeyHelp{"s", "send"},
			KeyHelp{"E", "edit+send"},
			KeyHelp{"b", "bind"},
			KeyHelp{"x", "stop"},
			KeyHelp{"D", "done"},
			KeyHelp{"u", "unbind"},
			KeyHelp{"g", "gate"},
			KeyHelp{"r", "retry on…"},
			KeyHelp{"o", "shell"},
		)
	}
	return keys
}

// Context is the counts line on the left and the sort, filter and refresh
// state on the right (§2.3).
func (f fleetView) Context(env Env) (string, string) {
	liveCount, needCount, workingCount, idleCount, heldCount, doneCount := 0, 0, 0, 0, 0, 0
	for _, b := range env.Report.Bindings {
		if b.Display == "DONE" {
			doneCount++
		} else {
			liveCount++
			switch groupOf(b) {
			case groupNeedsYou:
				needCount++
			case groupWorking:
				workingCount++
			case groupIdle:
				idleCount++
			case groupHeld:
				heldCount++
			}
		}
	}

	item := func(count int, label string) string {
		return textStyle.Bold(true).Render(fmt.Sprintf("%d", count)) + " " + mutedStyle.Render(label)
	}

	var parts []string
	parts = append(parts, item(liveCount, "live"))
	if needCount > 0 {
		label := "need you"
		if needCount == 1 {
			label = "needs you"
		}
		parts = append(parts, item(needCount, label))
	}
	if workingCount > 0 {
		parts = append(parts, item(workingCount, "working"))
	}
	if idleCount > 0 {
		parts = append(parts, item(idleCount, "idle"))
	}
	if heldCount > 0 {
		parts = append(parts, item(heldCount, "on hold"))
	}
	if doneCount > 0 {
		parts = append(parts, item(doneCount, "done"))
	}

	left := "   " + strings.Join(parts, "   ")

	order := "attention"
	if !f.attention {
		order = "name"
	}
	right := "sort " + order
	if q := f.activeFilter(); q != "" {
		right = fmt.Sprintf("filter %q · ", q) + right
	}
	if !env.StatusAt.IsZero() {
		right += faintStyle.Render(" · refreshed " + ago(env.StatusAt, env.Now) + " ago")
	}
	return left, right
}

// resolveSticky re-points cursor at the row keyed by sticky after the rows
// have changed, clamping and re-pointing sticky at what it lands on.
func (f *fleetView) resolveSticky(rows []view.BindingStatus) {
	if len(rows) == 0 {
		f.cursor = 0
		f.sticky = ""
		return
	}
	if f.sticky != "" {
		for i, b := range rows {
			if b.Key() == f.sticky {
				f.cursor = i
				return
			}
		}
	}
	if f.cursor >= len(rows) {
		f.cursor = len(rows) - 1
	}
	if f.cursor < 0 {
		f.cursor = 0
	}
	f.sticky = rows[f.cursor].Key()
}

func (f *fleetView) moveCursor(rows []view.BindingStatus, delta int) {
	if len(rows) == 0 {
		return
	}
	c := f.cursor + delta
	if c < 0 {
		c = 0
	}
	if c > len(rows)-1 {
		c = len(rows) - 1
	}
	f.cursor = c
	f.sticky = rows[c].Key()
}

// Update handles the fleet's own keys (§2.3, §5.3, §5.4).
func (f fleetView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case statusMsg:
		rows := f.rows(env)
		f.resolveSticky(rows)
		f.top = f.windowTop(rows, env)
		return f, nil
	case tea.KeyMsg:
		if f.filtering {
			return f.updateFilter(msg, env)
		}
		rows := f.rows(env)
		sel := view.BindingStatus{}
		if len(rows) > 0 {
			sel = rows[clampCursor(f.cursor, len(rows))]
		}
		if cmd, ok := fleetActionKey(env, sel, msg.String()); ok {
			return f, cmd
		}
		switch msg.String() {
		case "/":
			f.filtering = true
			f.filter = newFilterInput()
			f.filter.Focus()
			return f, nil
		case "esc":
			if f.filterText != "" {
				f.filterText = ""
				rows = f.rows(env)
				f.resolveSticky(rows)
				f.top = f.windowTop(rows, env)
			}
			return f, nil
		case ".":
			f.showDone = !f.showDone
			return f.repointed(env), nil
		case "up", "k":
			f.moveCursor(rows, -1)
		case "down", "j":
			f.moveCursor(rows, 1)
		case "home":
			f.moveCursor(rows, -len(rows))
		case "end":
			f.moveCursor(rows, len(rows))
		case "a":
			f.attention = !f.attention
			rows = f.rows(env)
			f.resolveSticky(rows)
			f.top = f.windowTop(rows, env)
			order := "attention"
			if !f.attention {
				order = "name"
			}
			return f, func() tea.Msg { return prefMsg{"sort", order} }
		case "enter":
			if len(rows) == 0 {
				return f, nil
			}
			key := rows[f.cursor].Key()
			v, cmd := newRoundView(env, key, 0)
			return f, push(v, cmd)
		}
		if len(rows) > 0 && f.cursor != clampCursor(f.cursor, len(rows)) {
			f.cursor = clampCursor(f.cursor, len(rows))
		}
		f.top = f.windowTop(rows, env)
		return f, nil
	}
	return f, nil
}

// updateFilter owns every key while the filter input is open (A4).
func (f fleetView) updateFilter(msg tea.KeyMsg, env Env) (View, tea.Cmd) {
	switch msg.String() {
	case "enter":
		f.filterText = f.filter.Value()
		f.filtering = false
		f.filter.Blur()
		return f.repointed(env), nil
	case "esc":
		f.filterText = ""
		f.filter.SetValue("")
		f.filtering = false
		f.filter.Blur()
		return f.repointed(env), nil
	}
	var cmd tea.Cmd
	f.filter, cmd = f.filter.Update(msg)
	return f.repointed(env), cmd
}

// repointed re-resolves the cursor and the window after the rows or filter changed.
func (f fleetView) repointed(env Env) fleetView {
	rows := f.rows(env)
	f.resolveSticky(rows)
	f.top = f.windowTop(rows, env)
	return f
}

func clampCursor(c, n int) int {
	if c < 0 {
		return 0
	}
	if c > n-1 {
		return n - 1
	}
	return c
}

// candidateText is the ON cell: the model part of the binding's candidate
// token, everything after the second '/'.
func candidateText(b view.BindingStatus) string {
	if b.BuilderName != "" {
		return b.BuilderName
	}
	s := b.BuilderCandidate
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return "-"
	}
	return s
}

func actorCell(b view.BindingStatus) string {
	if b.Role == "" {
		return "builder"
	}
	return b.Role
}

// secondSegment is the provider part of a candidate ref or a gate token: the
// second '/'-separated segment, or the whole string when there is none
// (§3.3, §3.5).
func secondSegment(s string) string {
	parts := strings.Split(s, "/")
	if len(parts) >= 2 {
		return parts[1]
	}
	return s
}

func nowCell(b view.BindingStatus, now time.Time) string {
	what, age := whatAge(b, now)
	if age == "" {
		return what
	}
	return what + " · " + age
}

// spendText is the SPEND cell: money only (A2).
func spendText(s usage.Spend) string {
	sum := s.Measured + s.Estimated
	if sum > 0 {
		text := fmt.Sprintf("$%.2f", sum)
		if sum < 0.01 {
			text = "<$0.01"
		}
		if s.Estimated > 0 {
			text = "~" + text
		}
		return text
	}
	if s.Plan > 0 {
		return "plan"
	}
	return ""
}

func spendCell(b view.BindingStatus) string {
	if b.Spend == nil {
		return ""
	}
	return spendText(*b.Spend)
}

func mastermindCell(b view.BindingStatus) string {
	if b.MasterMindName == "" {
		return "-"
	}
	return b.MasterMindName
}

func repoCell(b view.BindingStatus) string {
	s := b.CWD
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.Replace(s, home, "~", 1)
	}
	return s
}

// pad pads s to width cells, truncating with an ellipsis past it.
func pad(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	if lipgloss.Width(s) == width {
		return s
	}
	return clipName(s, width)
}

// fleetRowPlan computes which optional columns fit at width (§2.3 narrow rule).
func fleetRowPlan(width int) (candidate, spend, mastermind bool) {
	if width >= 94 {
		return true, true, true
	}
	if width >= 80 {
		return true, true, false
	}
	if width >= 70 {
		return true, false, false
	}
	return false, false, false
}

// fleetRowLine renders one binding row according to §2.3.
func fleetRowLine(b view.BindingStatus, g fleetGroup, selected bool, now time.Time, width int) string {
	gutter := "   "
	if selected {
		gutter = " " + accentStyle.Render("▍") + " "
	}
	meta := groupMetas[g]
	dot := meta.dot.Render(meta.glyph)

	nameText := pad(clipName(b.Key(), 20), 20)
	nameStyled := textStyle.Render(nameText)
	if selected {
		nameStyled = textStyle.Bold(true).Render(nameText)
	}

	nowVal := rowNow(b, now)
	nowText := pad(clipName(nowVal, 24), 24)
	var nowStyled string
	switch {
	case strings.HasPrefix(b.BuilderStatus, "exited") || strings.HasPrefix(b.BuilderStatus, "stalled"):
		nowStyled = redStyle.Render(nowText)
	case b.Unread && g == groupIdle:
		nowStyled = accentStyle.Render(nowText)
	case selected:
		nowStyled = textStyle.Render(nowText)
	default:
		nowStyled = mutedStyle.Render(nowText)
	}

	line := gutter + dot + "  " + nameStyled + nowStyled

	cand, spend, plan := fleetRowPlan(width)
	if cand {
		candText := pad(clipName(candidateText(b), 24), 24)
		line += mutedStyle.Render(candText)
	}
	if plan {
		planText := pad(clipName(mastermindCell(b), 14), 14)
		line += faintStyle.Render(planText)
	}
	if spend {
		s := spendCell(b)
		if len([]rune(s)) < 6 {
			s = strings.Repeat(" ", 6-len([]rune(s))) + s
		}
		line += faintStyle.Render(s)
	}
	if b.Role != "" {
		line += "  " + faintStyle.Render(b.Role)
	}

	if selected {
		return selBandStyle.Render(fit(line, width))
	}
	return fit(line, width)
}

// doneFoldLine renders the single summary line replacing folded DONE rows (§2.3).
func doneFoldLine(rows []view.BindingStatus, now time.Time, width int) string {
	line := "   " + faintStyle.Render("✓") + "  " + mutedStyle.Render(fmt.Sprintf("%d done", len(rows)))

	todayCount := 0
	var newestRow *view.BindingStatus
	var newestTS time.Time
	yNow, mNow, dNow := now.Local().Date()

	for i := range rows {
		b := &rows[i]
		if b.Last != nil && !b.Last.TS.IsZero() {
			yB, mB, dB := b.Last.TS.Local().Date()
			if yB == yNow && mB == mNow && dB == dNow {
				todayCount++
			}
			if newestRow == nil || b.Last.TS.After(newestTS) {
				newestRow = b
				newestTS = b.Last.TS
			}
		}
	}

	if newestRow != nil {
		line += faintStyle.Render(fmt.Sprintf("  ·  %d today  ·  last %s %s ago", todayCount, newestRow.Key(), ago(newestTS, now)))
	}

	line += "      " + chip(kbdStyle, ".") + faintStyle.Render(" show")
	return fit(line, width)
}

// cardLines renders the 5-line detail card at the top of the body (§2.3).
func (f fleetView) cardLines(env Env, b view.BindingStatus, width int) []string {
	g := groupOf(b)

	// Top line
	sr := shownRound(b)
	rn := rowNow(b, env.Now)
	if sr > 0 {
		rn = strings.TrimPrefix(rn, fmt.Sprintf("r%d · ", sr))
	}
	roundPart := fmt.Sprintf("round %d · %s", sr, rn)
	if sr == 0 {
		roundPart = rn
	}
	title := accentStyle.Bold(true).Render(b.Key()) + "  " + faintStyle.Render(roundPart)

	// Meta line
	var metaParts []string
	mastermindName := mastermindCell(b)
	if g == groupNeedsYou {
		metaParts = append(metaParts, mutedStyle.Render("MasterMind ")+warnStyle.Render(mastermindName))
	} else {
		metaParts = append(metaParts, mutedStyle.Render("MasterMind "+mastermindName))
	}
	metaParts = append(metaParts, mutedStyle.Render(candidateText(b)))
	branch := b.Branch
	if branch == "" {
		branch = repoCell(b)
	}
	if branch != "" {
		metaParts = append(metaParts, mutedStyle.Render(branch))
	}
	if b.LastClose != nil && b.LastClose.Commits > 0 {
		commitWord := "commits"
		if b.LastClose.Commits == 1 {
			commitWord = "commit"
		}
		metaParts = append(metaParts, mutedStyle.Render(fmt.Sprintf("+%d %s", b.LastClose.Commits, commitWord)))
	}
	if s := spendCell(b); s != "" {
		metaParts = append(metaParts, mutedStyle.Render(s))
	}
	metaContent := "  " + strings.Join(metaParts, mutedStyle.Render("  ·  "))

	// Keys line
	var cardKeys []KeyHelp
	if env.Actions == nil {
		cardKeys = []KeyHelp{{"enter", "open round"}}
	} else {
		switch g {
		case groupNeedsYou:
			cardKeys = []KeyHelp{
				{"enter", "open round"},
				{"s", "send the next plan"},
				{"o", "shell"},
				{"x", "stop"},
			}
		case groupWorking:
			cardKeys = []KeyHelp{
				{"enter", "open round"},
				{"x", "stop"},
				{"g", "gate"},
				{"o", "shell"},
			}
		case groupIdle:
			cardKeys = []KeyHelp{
				{"s", "send the next plan"},
				{"enter", "open round"},
				{"D", "done"},
				{"o", "shell"},
			}
		case groupHeld, groupOther:
			cardKeys = []KeyHelp{
				{"enter", "open round"},
				{"s", "send"},
				{"D", "done"},
			}
		case groupDone:
			cardKeys = []KeyHelp{
				{"enter", "open round"},
				{"u", "unbind"},
			}
		}
	}
	keysContent := cardKeysRow(cardKeys)

	return renderCard(width, title, "", []string{metaContent, "", keysContent})
}

// gatedLine renders the provider gate notice as the last line of the body (§2.3).
func gatedLine(env Env, width int) string {
	if len(env.Report.Gated) == 0 {
		return ""
	}
	var order []string
	latest := map[string]time.Time{}
	for _, g := range env.Report.Gated {
		p := g.Token
		parts := strings.Split(g.Token, "/")
		if len(parts) >= 2 {
			p = parts[1]
		}
		if prev, ok := latest[p]; !ok {
			order = append(order, p)
			latest[p] = g.Until
		} else if g.Until.After(prev) {
			latest[p] = g.Until
		}
	}

	var entries []string
	for _, p := range order {
		until := latest[p]
		left := "until cleared"
		if !until.IsZero() {
			left = ago(env.Now, until)
		}
		entries = append(entries, fmt.Sprintf("%s %s", p, left))
	}

	line := "   " + redStyle.Render("◌") + faintStyle.Render(" gated  ") +
		strings.Join(entries, faintStyle.Render("  ·  ")) +
		faintStyle.Render("  ·  the pick skips them")
	return fit(clipName(line, width), width)
}

// fleetLine is one drawn body line and the row it belongs to.
type fleetLine struct {
	text string
	row  int
}

// fleetSlotKind says what a list line is, so a line the frame will not paint
// can be laid out without being rendered.
type fleetSlotKind int

const (
	slotBlank fleetSlotKind = iota
	slotSection
	slotRow
	slotQuote
	slotFold
)

// fleetSlot is one line of the fleet list before it is painted: what it is,
// and the facts its painter needs. Laying the list out and painting it are
// separate steps, so the window can be chosen on the layout and only the lines
// inside it are ever rendered.
type fleetSlot struct {
	kind  fleetSlotKind
	row   int        // the row this line belongs to; -1 for a section, blank or fold line
	group fleetGroup // lineRow: the section the row is drawn in
	count int        // lineSection: how many rows the section holds
	quote string     // lineQuote: the waiting line under a NEEDS YOU row
}

// fleetListSlots lays out the grouped sections and fold line (§2.3, §5.2)
// without painting any of them: one slot per drawn line, carrying the row it
// belongs to.
func (f fleetView) fleetListSlots(env Env, rows []view.BindingStatus) []fleetSlot {
	q := f.activeFilter()
	groups := []fleetGroup{groupNeedsYou, groupWorking, groupIdle, groupHeld, groupOther}
	if f.showDone || q != "" {
		groups = append(groups, groupDone)
	}

	grouped := map[fleetGroup][]int{}
	for i := range rows {
		g := groupOf(rows[i])
		grouped[g] = append(grouped[g], i)
	}

	var slots []fleetSlot
	for _, g := range groups {
		idx := grouped[g]
		if len(idx) == 0 {
			continue
		}
		slots = append(slots, fleetSlot{kind: slotSection, row: -1, group: g, count: len(idx)})

		for _, i := range idx {
			slots = append(slots, fleetSlot{kind: slotRow, row: i, group: g})
			if w := rows[i].Waiting; rows[i].Display == "NEEDS YOU" && w != nil && w.Line != "" {
				slots = append(slots, fleetSlot{kind: slotQuote, row: i, group: g, quote: w.Line})
			}
		}
		slots = append(slots, fleetSlot{kind: slotBlank, row: -1})
	}

	if !f.showDone && q == "" && anyDone(env.Report.Bindings) {
		slots = append(slots, fleetSlot{kind: slotFold, row: -1})
	}

	return slots
}

// paintSlot renders one list line at width.
func (f fleetView) paintSlot(s fleetSlot, rows []view.BindingStatus, env Env, width int) string {
	switch s.kind {
	case slotSection:
		return fit(sectionLine(s.group, s.count), width)
	case slotRow:
		return fleetRowLine(rows[s.row], s.group, s.row == f.cursor, env.Now, width)
	case slotQuote:
		return quoteLine(s.quote, s.row == f.cursor, width)
	case slotFold:
		return doneFoldLine(doneRows(env.Report.Bindings), env.Now, width)
	}
	return fit("", width)
}

// sectionLine is a section's header: its pill, how many rows it holds, and the
// hint that says what the section is for.
func sectionLine(g fleetGroup, count int) string {
	meta := groupMetas[g]
	hint := meta.hint
	if g == groupDone {
		hint = ". hide"
	}
	sec := "    " + chip(meta.pill, meta.label) + "  " + faintStyle.Render(fmt.Sprintf("%d", count))
	if hint != "" {
		sec += "    " + faintStyle.Italic(true).Render(hint)
	}
	return sec
}

// quoteLine is the second line under a NEEDS YOU row, carrying the builder's
// question; on the cursor's row it wears the selection band.
func quoteLine(quote string, selected bool, width int) string {
	line := "        " + faintStyle.Render("╰ ") + mutedStyle.Italic(true).Render(quote)
	if selected {
		return selBandStyle.Render(fit(line, width))
	}
	return fit(line, width)
}

// doneRows is a report's DONE rows, for the fold line's summary.
func doneRows(bindings []view.BindingStatus) []view.BindingStatus {
	var out []view.BindingStatus
	for i := range bindings {
		if bindings[i].Display == "DONE" {
			out = append(out, bindings[i])
		}
	}
	return out
}

// anyDone reports whether a report holds a DONE row at all, so a fleet with
// nothing finished draws no fold line.
func anyDone(bindings []view.BindingStatus) bool {
	for i := range bindings {
		if bindings[i].Display == "DONE" {
			return true
		}
	}
	return false
}

// fleetListLines renders every line of the grouped sections and fold line
// (§2.3, §5.2). A frame paints one window of them; this is the whole list.
func (f fleetView) fleetListLines(env Env, width int) []fleetLine {
	rows := f.rows(env)
	slots := f.fleetListSlots(env, rows)
	list := make([]fleetLine, 0, len(slots))
	for _, s := range slots {
		list = append(list, fleetLine{text: f.paintSlot(s, rows, env, width), row: s.row})
	}
	return list
}

// cardBlock returns the card's 5 lines plus one trailing blank line, or nil
// when there is no selected row or height < 16 (§2, §3).
func (f fleetView) cardBlock(env Env, width, height int) []string {
	rows := f.rows(env)
	if len(rows) == 0 || height < 16 {
		return nil
	}
	sel := rows[clampCursor(f.cursor, len(rows))]
	card := f.cardLines(env, sel, width)
	return append(card, fit("", width))
}

// gatedBlock returns the gated line in a single-element slice, or nil when empty (§2, §3).
func gatedBlock(env Env, width int) []string {
	if gl := gatedLine(env, width); gl != "" {
		return []string{gl}
	}
	return nil
}

func (f fleetView) tableHeight(env Env) int {
	bh := bodyHeight(env)
	fixedH := 1
	if f.filtering {
		fixedH++
	}
	h := bh - fixedH - len(f.cardBlock(env, env.Width, bh)) - len(gatedBlock(env, env.Width))
	if h < 0 {
		return 0
	}
	return h
}

// windowTopLines is the first line to draw so every line of the cursor's row is
// visible in height lines, moving top as little as possible.
func (f fleetView) windowTopLines(lines []fleetLine, height int) int {
	return windowTopIndex(len(lines), height, f.cursor, f.top, func(i int) int { return lines[i].row })
}

// windowTopSlots is windowTopLines over a laid-out list: the window is chosen
// on the layout, before a single line is rendered.
func (f fleetView) windowTopSlots(slots []fleetSlot, height int) int {
	return windowTopIndex(len(slots), height, f.cursor, f.top, func(i int) int { return slots[i].row })
}

// windowTopIndex is the window rule itself: the first of n lines to draw so the
// cursor's row is wholly inside the next height lines, moved as little as
// possible. rowOf names the row a line belongs to.
func windowTopIndex(n, height, cursor, top int, rowOf func(int) int) int {
	if height <= 0 || n <= height {
		return 0
	}
	first, last := -1, -1
	for i := 0; i < n; i++ {
		if rowOf(i) == cursor {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return 0
	}
	if top > n-height {
		top = n - height
	}
	if top < 0 {
		top = 0
	}
	if last >= top+height {
		top = last - height + 1
	}
	if first < top {
		top = first
	}
	return top
}

func (f fleetView) windowTop(rows []view.BindingStatus, env Env) int {
	slots := f.fleetListSlots(env, rows)
	return f.windowTopSlots(slots, f.tableHeight(env))
}

// fleetWindowLines renders only the lines of the list a frame of listH lines
// paints, padded out to listH. The window is chosen on the layout, so a line
// that falls outside it is never rendered.
func (f fleetView) fleetWindowLines(env Env, width, listH int) []string {
	rows := f.rows(env)
	slots := f.fleetListSlots(env, rows)
	start := f.windowTopSlots(slots, listH)
	end := len(slots)
	if listH > 0 && start+listH < end {
		end = start + listH
	}

	window := make([]string, 0, listH)
	if start < len(slots) {
		for _, s := range slots[start:end] {
			window = append(window, f.paintSlot(s, rows, env, width))
		}
	}
	for len(window) < listH {
		window = append(window, fit("", width))
	}
	return window
}

// Body renders the card, grouped list, done fold and gated line (§2.3, §5.2).
func (f fleetView) Body(env Env, width, height int) string {
	if !env.Loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	if len(env.Report.Bindings) == 0 {
		return strings.Join(emptyPaneBlock(width, height), "\n")
	}

	var fixed []string
	fixed = append(fixed, fit("", width))
	if f.filtering {
		fixed = append(fixed, fit(f.filter.View(), width))
	}

	card := f.cardBlock(env, width, height)
	gated := gatedBlock(env, width)

	listH := height - len(fixed) - len(card) - len(gated)
	if listH < 0 {
		listH = 0
	}

	window := f.fleetWindowLines(env, width, listH)

	var out []string
	out = append(out, fixed...)
	out = append(out, card...)
	out = append(out, window...)
	out = append(out, gated...)

	return strings.Join(fitLines(out, width, height), "\n")
}

// blockLines renders raw prose lines fitted to width and padded to height.
func blockLines(raw []string, width, height int) []string {
	out := make([]string, 0, height)
	for _, l := range raw {
		out = append(out, fit(emptyStyle.Render(l), width))
	}
	for len(out) < height {
		out = append(out, fit("", width))
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}

// fitLines pads lines to height, truncates the overflow and fits each to width.
func fitLines(lines []string, width, height int) []string {
	out := make([]string, 0, height)
	for _, l := range lines {
		out = append(out, fit(l, width))
	}
	for len(out) < height {
		out = append(out, fit("", width))
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}
