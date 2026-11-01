package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/text"
	"github.com/fuad-daoud/relevo/internal/ui/dash"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

type logView struct {
	events    []db.EventLogRow
	hist      availability.History
	revs      []db.RevisionRow
	loaded    bool
	err       error
	fetching  bool
	lastFetch time.Time
	cursor    int // index into the rendered entry list (day rules excluded)
	top       int // first body line drawn
	follow    bool
	filter    string
	input     textinput.Model
	editing   bool
	// shapes answers a binding's stored shape, so a row's word can follow it.
	// nil (and "" for an unknown name) words as a writer.
	shapes func(binding string) string
}

type eventLogMsg struct {
	events []db.EventLogRow
	hist   availability.History
	revs   []db.RevisionRow
	err    error
	at     time.Time
	shapes func(binding string) string
}

func newLogView(env widget.Env) (widget.View, tea.Cmd) {
	in := newTextInput()
	in.Prompt = ""
	v := logView{
		follow: true,
		input:  in,
	}
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	var base relevo.Runtime
	if env.Src != nil {
		base = env.Src.Base()
	}
	return v, fetchEventLog(env.Ctx, base, now)
}

func fetchEventLog(ctx context.Context, rt relevo.Runtime, now time.Time) tea.Cmd {
	return func() tea.Msg {
		if rt.DB == nil {
			return eventLogMsg{err: relevo.ErrNoDatabase, at: now}
		}
		events, err := rt.DB.RecentEvents(now.Add(-7*24*time.Hour), 3000)
		if err != nil {
			return eventLogMsg{err: err, at: now}
		}
		revs, err := rt.DB.Revisions(0)
		if err != nil {
			return eventLogMsg{err: err, at: now}
		}
		hist, err := availability.LoadHistory(relevo.AvailabilityDeps(rt))
		if err != nil {
			hist = availability.History{}
		}
		return eventLogMsg{
			events: events,
			hist:   hist,
			revs:   revs,
			at:     now,
			shapes: bindingShapes(rt),
		}
	}
}

type foldKey struct {
	binding string
	roundID string
}

func (v logView) Crumbs() []string { return []string{"log"} }

func (v logView) Capturing() bool { return v.editing }

func (v logView) Keys() []widget.KeyHelp {
	return []widget.KeyHelp{
		{Key: "↑↓", Help: "move"},
		{Key: "enter", Help: "open round"},
		{Key: "/", Help: "filter"},
		{Key: "f", Help: "follow"},
	}
}

func (v logView) filteredEntries(env widget.Env) []logEntry {
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	since := now.Add(-7 * 24 * time.Hour)
	var nameOf func(string) string
	if env.Src != nil && env.Src.Base().Candidates != nil {
		nameOf = env.Src.Base().Candidates.NameOf
	}
	all := buildLogEntries(v.events, v.hist, v.revs, env.ActionLog, since, nameOf, v.shapes)
	if v.filter == "" {
		return all
	}
	q := strings.ToLower(v.filter)
	var filtered []logEntry
	for _, e := range all {
		b := strings.ToLower(e.Binding)
		w := strings.ToLower(e.Word)
		d := strings.ToLower(e.Detail)
		if strings.Contains(b, q) || strings.Contains(w, q) || strings.Contains(d, q) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func (v logView) Context(env widget.Env) (string, string) {
	if v.err != nil && !v.loaded {
		return "   " + widget.ErrorStyle.Render(v.err.Error()), ""
	}
	if !v.loaded {
		return "   " + widget.MutedStyle.Render("loading…"), ""
	}

	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	since := now.Add(-7 * 24 * time.Hour)
	var nameOf func(string) string
	if env.Src != nil && env.Src.Base().Candidates != nil {
		nameOf = env.Src.Base().Candidates.NameOf
	}
	entries := buildLogEntries(v.events, v.hist, v.revs, env.ActionLog, since, nameOf, v.shapes)

	todayCount := 0
	counts := make(map[string]int)
	for _, e := range entries {
		if dash.DayLabel(e.At, now, time.Local) == "today" {
			todayCount++
			counts[e.Word]++
		}
	}

	var items []string
	items = append(items, widget.TextStyle.Bold(true).Render(fmt.Sprintf("%d", todayCount))+" "+widget.MutedStyle.Render("events today"))
	for _, w := range []string{"sent", "done", "halted", "exited", "switched", "gated"} {
		if c := counts[w]; c > 0 {
			items = append(items, widget.TextStyle.Bold(true).Render(fmt.Sprintf("%d", c))+" "+widget.MutedStyle.Render(w))
		}
	}
	left := "   " + strings.Join(items, "   ")

	var right string
	if v.filter != "" {
		right += widget.MutedStyle.Render("/ "+clip(v.filter, 40)) + "   "
	}
	if v.follow {
		right += widget.AccentStyle.Render("following")
	} else {
		right += widget.FaintStyle.Render("paused")
	}
	right += "   "

	return left, right
}

func (v logView) Update(msg tea.Msg, env widget.Env) (widget.View, tea.Cmd) {
	switch msg := msg.(type) {
	case widget.TickMsg:
		return v.tickUpdate(env)

	case eventLogMsg:
		return v.eventUpdate(msg, env)

	case roundOpenMsg:
		return v.openUpdate(msg, env)

	case tea.KeyMsg:
		if v.editing {
			return v.filterUpdate(msg)
		}
		return v.keyUpdate(msg, env)
	}

	return v, nil
}

func (v logView) tickUpdate(env widget.Env) (widget.View, tea.Cmd) {
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	if !v.fetching && (v.lastFetch.IsZero() || now.Sub(v.lastFetch) >= 5*time.Second) {
		v.fetching = true
		var base relevo.Runtime
		if env.Src != nil {
			base = env.Src.Base()
		}
		return v, fetchEventLog(env.Ctx, base, now)
	}
	return v, nil
}

func (v logView) eventUpdate(msg eventLogMsg, env widget.Env) (widget.View, tea.Cmd) {
	v.fetching = false
	v.lastFetch = msg.at
	if msg.err != nil {
		v.err = msg.err
	} else {
		v.events = msg.events
		v.hist = msg.hist
		v.revs = msg.revs
		v.shapes = msg.shapes
		v.loaded = true
		v.err = nil
	}
	if v.follow {
		v.cursor = 0
		v.top = 0
	} else {
		entries := v.filteredEntries(env)
		if v.cursor >= len(entries) && len(entries) > 0 {
			v.cursor = len(entries) - 1
		}
	}
	return v, nil
}

func (v logView) openUpdate(msg roundOpenMsg, env widget.Env) (widget.View, tea.Cmd) {
	if msg.hist != nil {
		next, cmd := newHistRoundView(env, *msg.hist, msg.round)
		return v, push(next, cmd)
	}
	next, cmd := newRoundView(env, msg.key, msg.round)
	return v, push(next, cmd)
}

func (v logView) filterUpdate(msg tea.KeyMsg) (widget.View, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		v.editing = false
		v.filter = strings.TrimSpace(v.input.Value())
		v.cursor = 0
		v.top = 0
		v.input.Blur()
		return v, nil
	case tea.KeyEsc:
		v.editing = false
		v.input.Blur()
		return v, nil
	default:
		var cmd tea.Cmd
		v.input, cmd = v.input.Update(msg)
		return v, cmd
	}
}

func (v logView) keyUpdate(msg tea.KeyMsg, env widget.Env) (widget.View, tea.Cmd) {
	entries := v.filteredEntries(env)
	pageSize := widget.BodyHeight(env) - 3
	if pageSize < 1 {
		pageSize = 1
	}

	if next, cmd, ok := v.moveKey(msg, entries, pageSize); ok {
		return next, cmd
	}

	switch msg.String() {
	case "enter":
		if v.cursor >= 0 && v.cursor < len(entries) {
			e := entries[v.cursor]
			if e.Binding != "" && e.Binding != "you" && e.Round > 0 {
				return v, openRound(env, dash.JumpMsg{BindingName: e.Binding, Round: e.Round})
			}
		}
		return v, nil
	case "/":
		v.editing = true
		in := v.input
		if in.Prompt == "" && in.Placeholder == "" && in.Width == 0 {
			in = newTextInput()
			in.Prompt = ""
		}
		in.SetValue(v.filter)
		cmd := in.Focus()
		v.input = in
		return v, cmd
	}

	return v, nil
}

func (v logView) moveKey(msg tea.KeyMsg, entries []logEntry, pageSize int) (widget.View, tea.Cmd, bool) {
	switch msg.String() {
	case "up", "k":
		if v.cursor > 0 {
			v.cursor--
			v.follow = false
		}
		return v, nil, true
	case "down", "j":
		if v.cursor < len(entries)-1 {
			v.cursor++
		}
		v.follow = false
		return v, nil, true
	case "pgup":
		if v.cursor > 0 {
			v.cursor -= pageSize
			if v.cursor < 0 {
				v.cursor = 0
			}
			v.follow = false
		}
		return v, nil, true
	case "pgdown", " ":
		if len(entries) > 0 {
			v.cursor += pageSize
			if v.cursor >= len(entries) {
				v.cursor = len(entries) - 1
			}
		}
		v.follow = false
		return v, nil, true
	case "home":
		v.cursor = 0
		v.top = 0
		v.follow = true
		return v, nil, true
	case "end":
		if len(entries) > 0 {
			v.cursor = len(entries) - 1
		}
		v.follow = false
		return v, nil, true
	case "f":
		v.follow = true
		v.cursor = 0
		v.top = 0
		return v, nil, true
	}
	return v, nil, false
}

func (v logView) Body(env widget.Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}

	cw := width - 6
	if cw < 0 {
		cw = 0
	}

	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}

	blankRow := "   " + strings.Repeat(" ", cw) + "   "
	line1 := v.headLine(cw)

	entries := v.filteredEntries(env)

	if v.loaded && len(entries) == 0 {
		return strings.Join(logEmptyBody(blankRow, line1, cw, height), "\n")
	}

	runs, labels, runIndices := groupLogRuns(entries, now)

	entryLine := make(map[int]int)
	var bodyLines []string

	detailW := cw - 5 - 2 - 14 - 2 - 4 - 2 - 9 - 2
	if detailW < 0 {
		detailW = 0
	}

	for r := range runs {
		bodyLines = append(bodyLines, logDayRule(labels[r], len(runs[r]), cw))
		for subIdx, e := range runs[r] {
			entryIdx := runIndices[r][subIdx]
			entryLine[entryIdx] = len(bodyLines)
			bodyLines = append(bodyLines, logEntryLine(e, entryIdx == v.cursor, cw, detailW))
		}
	}

	return logTailWindow(bodyLines, entryLine, v.cursor, v.top, len(entries), blankRow, line1, height)
}

func (v logView) headLine(cw int) string {
	if v.editing {
		return "   " + widget.Fit("/ "+v.input.View(), cw) + "   "
	}
	detailW := cw - 5 - 2 - 14 - 2 - 4 - 2 - 9 - 2
	if detailW < 0 {
		detailW = 0
	}
	header := fmt.Sprintf("%-5s  %-14s  %-4s  %-9s  %-*s", "TIME", "BINDING", "RND", "EVENT", detailW, "DETAIL")
	return "   " + widget.Fit(widget.FaintStyle.Bold(true).Render(header), cw) + "   "
}

func logEmptyBody(blankRow, line1 string, cw, height int) []string {
	var lines []string
	lines = append(lines, blankRow, line1)
	lines = append(lines, "   "+widget.Fit(widget.EmptyStyle.Render("no events in the last 7 days"), cw)+"   ")
	for len(lines) < height {
		lines = append(lines, blankRow)
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

func groupLogRuns(entries []logEntry, now time.Time) ([][]logEntry, []string, [][]int) {
	var runs [][]logEntry
	var labels []string
	var runIndices [][]int

	i := 0
	for i < len(entries) {
		lbl := dash.DayLabel(entries[i].At, now, time.Local)
		j := i + 1
		for j < len(entries) {
			if dash.DayLabel(entries[j].At, now, time.Local) != lbl {
				break
			}
			j++
		}
		runs = append(runs, entries[i:j])
		labels = append(labels, lbl)
		var idxs []int
		for k := i; k < j; k++ {
			idxs = append(idxs, k)
		}
		runIndices = append(runIndices, idxs)
		i = j
	}
	return runs, labels, runIndices
}

func logDayRule(label string, count, cw int) string {
	rightText := " " + text.Plural(count, "event")
	leftWidth := lipgloss.Width(label) + 1
	rightWidth := lipgloss.Width(rightText)
	fill := cw - leftWidth - rightWidth
	if fill < 0 {
		fill = 0
	}
	ruleContent := widget.MutedStyle.Render(label) + " " + widget.GridStyle.Render(strings.Repeat("┈", fill)) + widget.FaintStyle.Render(rightText)
	return "   " + widget.Fit(ruleContent, cw) + "   "
}

func logEntryLine(e logEntry, isCursor bool, cw, detailW int) string {
	timeText := e.At.Local().Format("15:04")
	tStyle := widget.MutedStyle
	if isCursor {
		tStyle = tStyle.Background(widget.SelBandStyle.GetBackground())
	}
	col1 := tStyle.Render(timeText)

	bindText := e.Binding
	var bStyle lipgloss.Style
	switch bindText {
	case "":
		bindText = "—"
		bStyle = widget.FaintStyle
	case "you":
		bStyle = widget.AccentStyle
	default:
		bStyle = widget.TextStyle
	}
	if isCursor {
		bStyle = bStyle.Bold(true).Background(widget.SelBandStyle.GetBackground())
	}
	col2 := bStyle.Render(widget.Pad(clip(bindText, 14), 14))

	rndText := ""
	if e.Round > 0 {
		rndText = fmt.Sprintf("r%d", e.Round)
	}
	rStyle := widget.MutedStyle
	if isCursor {
		rStyle = rStyle.Background(widget.SelBandStyle.GetBackground())
	}
	col3 := rStyle.Render(widget.Pad(rndText, 4))

	eStyle := e.Style
	if isCursor {
		eStyle = eStyle.Background(widget.SelBandStyle.GetBackground())
	}
	col4 := eStyle.Render(widget.Pad(clip(e.Word, 9), 9))

	dStyle := widget.MutedStyle
	if e.Err {
		dStyle = widget.RedStyle
	}
	if isCursor {
		dStyle = dStyle.Background(widget.SelBandStyle.GetBackground())
	}
	detailText := clip(e.Detail, detailW)
	col5 := dStyle.Render(detailText)

	sep := "  "
	if isCursor {
		sep = widget.SelBandStyle.Render("  ")
	}
	rowContent := col1 + sep + col2 + sep + col3 + sep + col4 + sep + col5
	var line string
	if isCursor {
		if lipgloss.Width(rowContent) > cw {
			rowContent = lipgloss.NewStyle().MaxWidth(cw).Render(rowContent)
		}
		if w := lipgloss.Width(rowContent); w < cw {
			rowContent += widget.SelBandStyle.Render(strings.Repeat(" ", cw-w))
		}
		line = "   " + rowContent + "   "
	} else {
		line = "   " + widget.Fit(rowContent, cw) + "   "
	}
	return line
}

func logTailWindow(bodyLines []string, entryLine map[int]int, cursor, top, nEntries int, blankRow, line1 string, height int) string {
	visibleHeight := height - 2
	if visibleHeight < 0 {
		visibleHeight = 0
	}

	if nEntries > 0 && cursor >= 0 && cursor < nEntries {
		cursorLine := entryLine[cursor]
		if cursorLine < top {
			top = cursorLine
		}
		if cursorLine >= top+visibleHeight {
			top = cursorLine - visibleHeight + 1
		}
		if top > len(bodyLines)-visibleHeight {
			top = len(bodyLines) - visibleHeight
		}
		if top < 0 {
			top = 0
		}
	}

	var displayed []string
	if top < len(bodyLines) {
		end := top + visibleHeight
		if end > len(bodyLines) {
			end = len(bodyLines)
		}
		displayed = bodyLines[top:end]
	}

	allLines := []string{blankRow, line1}
	allLines = append(allLines, displayed...)
	for len(allLines) < height {
		allLines = append(allLines, blankRow)
	}
	if len(allLines) > height {
		allLines = allLines[:height]
	}

	return strings.Join(allLines, "\n")
}
