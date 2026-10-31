package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/view"
)

type fleetView struct {
	cursor    int
	sticky    string
	top       int
	attention bool
	actions   bool
	showDone  bool

	filtering  bool
	filter     textinput.Model
	filterText string

	// A fleetView is a value, so the pointer is what survives the copies
	// Update hands back and the shell keeps on its stack.
	memo *rowsMemo
}

func newFleetView(attention bool) fleetView {
	return fleetView{attention: attention, filter: newFilterInput(), memo: &rowsMemo{}}
}

// Any one of the report, the filter, the done fold or the sort order
// changing means the rows are stale.
type rowsKey struct {
	report    *view.BindingStatus
	rows      int
	statusAt  time.Time
	filter    string
	showDone  bool
	attention bool
}

// One row set per cycle, read by Update, the window, the card and the list,
// so a frame sorts the report once instead of once per reader. sorts counts
// the computations, one SortRows each, so a test can pin how often a frame
// really sorts.
type rowsMemo struct {
	key   rowsKey
	rows  []view.BindingStatus
	sorts int
}

// The memo holds the pointer, so the backing array stays live and no later
// slice can be handed the same address.
func firstRow(rows []view.BindingStatus) *view.BindingStatus {
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// Keys() shows the action keys exactly when one exists.
func (f fleetView) withActions(a bool) fleetView {
	f.actions = a
	return f
}

func newFilterInput() textinput.Model {
	in := newTextInput()
	in.Prompt = "/"
	return in
}

func (f fleetView) rows(env widget.Env) []view.BindingStatus {
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

func (f fleetView) activeFilter() string {
	if f.filtering {
		return f.filter.Value()
	}
	return f.filterText
}

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

// The shell forwards ':', '?', 'q' and 'esc' to the input while it is open.
func (f fleetView) Capturing() bool { return f.filtering }

func (f fleetView) Keys() []widget.KeyHelp {
	if f.actions {
		return []widget.KeyHelp{
			{Key: "enter", Help: "open"},
			{Key: "s", Help: "send"},
			{Key: "x", Help: "stop"},
			{Key: "D", Help: "done"},
			{Key: "g", Help: "gate"},
			{Key: "/", Help: "filter"},
		}
	}
	return []widget.KeyHelp{
		{Key: "enter", Help: "open"},
		{Key: "/", Help: "filter"},
		{Key: "a", Help: "sort"},
	}
}

func (f fleetView) HelpKeys() []widget.KeyHelp {
	keys := []widget.KeyHelp{
		{Key: "↑↓", Help: "move"},
		{Key: "home/end", Help: "first/last"},
		{Key: "enter", Help: "open"},
		{Key: "a", Help: "sort"},
		{Key: ".", Help: "done rows"},
		{Key: "/", Help: "filter"},
	}
	if f.actions {
		keys = append(keys,
			widget.KeyHelp{Key: "s", Help: "send"},
			widget.KeyHelp{Key: "E", Help: "edit+send"},
			widget.KeyHelp{Key: "b", Help: "bind"},
			widget.KeyHelp{Key: "x", Help: "stop"},
			widget.KeyHelp{Key: "D", Help: "done"},
			widget.KeyHelp{Key: "u", Help: "unbind"},
			widget.KeyHelp{Key: "g", Help: "gate"},
			widget.KeyHelp{Key: "r", Help: "retry on…"},
			widget.KeyHelp{Key: "o", Help: "shell"},
		)
	}
	return keys
}

func (f fleetView) Context(env widget.Env) (string, string) {
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
		return widget.TextStyle.Bold(true).Render(fmt.Sprintf("%d", count)) + " " + widget.MutedStyle.Render(label)
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
		right += widget.FaintStyle.Render(" · refreshed " + ago(env.StatusAt, env.Now) + " ago")
	}
	return left, right
}

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

func (f fleetView) Update(msg tea.Msg, env widget.Env) (widget.View, tea.Cmd) {
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
			return f, func() tea.Msg { return widget.PrefMsg{Key: "sort", Value: order} }
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

func (f fleetView) updateFilter(msg tea.KeyMsg, env widget.Env) (widget.View, tea.Cmd) {
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

func (f fleetView) repointed(env widget.Env) fleetView {
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
