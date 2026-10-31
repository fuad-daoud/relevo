package stats

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

func (v statsView) Update(msg tea.Msg, env widget.Env) (widget.View, tea.Cmd) {
	switch msg := msg.(type) {
	case StatsMsg:
		// A reply for another window is stale after a `w`: drop it.
		if msg.Window != v.window {
			return v, nil
		}
		v.fetching = false
		v.fetchedAt = env.Now
		if msg.Err != nil {
			// The last good report stays on screen.
			v.err = msg.Err
		} else {
			v.rep = msg.Rep
			v.err = nil
			v.loaded = true
			v.configured = nil
			if rt := env.Src.Base(); rt.Candidates != nil {
				v.configured = rt.Candidates.Refs()
			}
		}
		if len(msg.Warnings) > 0 {
			return v, notice(msg.Warnings[0])
		}
		return v, nil

	case widget.TickMsg:
		if !v.fetching && env.Now.Sub(v.fetchedAt) > statsRefreshEvery {
			v.fetching = true
			return v, v.fetch(env)
		}
		return v, nil

	case tea.KeyMsg:
		return v.updateKey(msg, env)
	}
	return v, nil
}

func (v statsView) updateKey(k tea.KeyMsg, env widget.Env) (widget.View, tea.Cmd) {
	switch k.String() {
	case "w":
		v.window = nextStatsWindow(v.window)
		v.top = 0
		v.ovCursor = [2]int{}
		v.fetching = true
		return v, v.fetch(env)
	case "r":
		v.fetching = true
		return v, v.fetch(env)
	case "tab":
		v.tab = (v.tab + 1) % len(statsTabs)
		v.top = 0
		v.setFocus()
		return v, nil
	case "shift+tab":
		v.tab = (v.tab - 1 + len(statsTabs)) % len(statsTabs)
		v.top = 0
		v.setFocus()
		return v, nil
	case "1", "2", "3", "4", "5":
		v.tab = int(k.String()[0] - '1')
		v.top = 0
		v.setFocus()
		return v, nil
	case "up", "k", "down", "j", "pgup", "pgdown", "pgdn", " ", "home", "end":
		return v.updatePageKey(k.String(), env)
	case "left", "h":
		switch statsTabs[v.tab] {
		case "overview":
			v.ovFocus = 0
			v.follow(env)
		case "repos":
			v.collapseSelectedRepo(env)
		}
		return v, nil
	case "right", "l":
		switch statsTabs[v.tab] {
		case "overview":
			v.ovFocus = 1
			v.follow(env)
		case "repos":
			return v, v.expandSelectedRepo(env)
		}
		return v, nil
	case "s":
		if v.tab == statsTabTokens {
			v.split = (v.split + 1) % len(statsSplitNames)
			v.top = 0
		}
		return v, nil
	case "enter":
		switch statsTabs[v.tab] {
		case "overview":
			return v.enterOverview(env)
		case "candidates", "repos":
			return v.enter(env)
		}
		return v, nil
	}
	return v, nil
}

// updatePageKey moves the page for the keys that scroll it: the cursor tabs
// move their own cursor and follow it, the rest move the top line.
func (v statsView) updatePageKey(key string, env widget.Env) (widget.View, tea.Cmd) {
	switch key {
	case "up", "k":
		if v.tabHasCursor() {
			v.moveStatsCursor(-1)
			v.follow(env)
			return v, nil
		}
		lines, _, avail := v.page(env)
		v.top = widget.Clamp(v.top-1, 0, statsMaxTop(len(lines), avail))
		return v, nil
	case "down", "j":
		if v.tabHasCursor() {
			v.moveStatsCursor(1)
			v.follow(env)
			return v, nil
		}
		lines, _, avail := v.page(env)
		v.top = widget.Clamp(v.top+1, 0, statsMaxTop(len(lines), avail))
		return v, nil
	case "pgup":
		_, _, avail := v.page(env)
		v.top = max(0, v.top-max(1, avail-1))
		return v, nil
	case "pgdown", "pgdn": // bubbletea reports Page Down as "pgdown"
		lines, _, avail := v.page(env)
		v.top = min(v.top+max(1, avail-1), statsMaxTop(len(lines), avail))
		return v, nil
	case " ":
		// Space pages every other tab; on repos it toggles the selected row's
		// repo's expansion.
		if statsTabs[v.tab] == "repos" {
			return v, v.toggleSelectedRepo(env)
		}
		lines, _, avail := v.page(env)
		v.top = min(v.top+max(1, avail-1), statsMaxTop(len(lines), avail))
		return v, nil
	case "home":
		v.top = 0
		return v, nil
	case "end":
		lines, _, avail := v.page(env)
		v.top = statsMaxTop(len(lines), avail)
		return v, nil
	}
	return v, nil
}

// setFocus points the focusable panels' cursor at the tab's panel: candidates
// is focus 0, repos focus 1, and the other tabs leave focus alone.
func (v *statsView) setFocus() {
	switch statsTabs[v.tab] {
	case "candidates":
		v.focus = 0
	case "repos":
		v.focus = 1
	}
}

// tabHasCursor reports whether the active tab has a row cursor for ↑↓ to move:
// true on overview, candidates and repos, false on tokens and reliability,
// whose ↑↓ scroll the page instead.
func (v statsView) tabHasCursor() bool {
	switch statsTabs[v.tab] {
	case "overview", "candidates", "repos":
		return true
	}
	return false
}

func nextStatsWindow(window string) string {
	for i, w := range statsWindows {
		if w == window {
			return statsWindows[(i+1)%len(statsWindows)]
		}
	}
	return statsWindows[0]
}

func (v statsView) panelRows() int {
	if v.focus == 0 {
		return len(v.overviewCandRows(func(s string) string { return s }))
	}
	return len(v.repoTabRows())
}

// reposSelected is the repo owning the selected repos-tab row: its key and
// whether that row is one of its children.
func (v statsView) reposSelected() (key string, child, ok bool) {
	rows := v.repoTabRows()
	if len(rows) == 0 {
		return "", false, false
	}
	r := rows[statsClamp(v.cursor[1], len(rows))]
	if r.parent != "" {
		return r.parent, true, true
	}
	if r.kind != kindRepo {
		return "", false, false
	}
	return r.group.Key, false, true
}

// repoRowIndex is the visible index of a repo's own row, or -1.
func (v statsView) repoRowIndex(key string) int {
	for i, r := range v.repoTabRows() {
		if r.kind == kindRepo && r.group.Key == key {
			return i
		}
	}
	return -1
}

// toggleSelectedRepo flips the expansion of the repo owning the selected row
// A child collapses its repo and leaves the cursor on the repo's row; a
// repo row toggles in place, so the cursor does not move. A repo with nothing to
// show notices instead of expanding, so the key never looks dead.
func (v *statsView) toggleSelectedRepo(env widget.Env) tea.Cmd {
	key, child, ok := v.reposSelected()
	if !ok {
		return nil
	}
	if v.expanded[key] {
		v.collapseRepo(key, child)
		v.follow(env)
		return nil
	}
	return v.expandRepo(key)
}

// expandSelectedRepo shows the selected row's repo's children; expanding never
// moves the cursor.
func (v *statsView) expandSelectedRepo(env widget.Env) tea.Cmd {
	if key, _, ok := v.reposSelected(); ok {
		return v.expandRepo(key)
	}
	return nil
}

// collapseSelectedRepo hides the selected row's repo's children; a cursor on one
// of them lands on the repo's own row.
func (v *statsView) collapseSelectedRepo(env widget.Env) {
	if key, child, ok := v.reposSelected(); ok {
		v.collapseRepo(key, child)
		v.follow(env)
	}
}

// expandRepo shows a repo's children without moving the cursor. A repo with no
// children stays collapsed and notices, so the key never looks dead.
func (v *statsView) expandRepo(key string) tea.Cmd {
	if !v.repoHasChildren(key) {
		return notice(shortRepo(key) + " has no features or tickets")
	}
	if v.expanded == nil {
		v.expanded = map[string]bool{}
	}
	v.expanded[key] = true
	return nil
}

func (v statsView) repoHasChildren(key string) bool {
	for _, r := range v.rep.Repos {
		if r.Key == key {
			return len(repoChildren(r)) > 0
		}
	}
	return false
}

// collapseRepo hides a repo's children; when the cursor was on one of them it
// lands on the repo's own row.
func (v *statsView) collapseRepo(key string, child bool) {
	if v.expanded == nil {
		return
	}
	idx := v.repoRowIndex(key)
	delete(v.expanded, key)
	if child && idx >= 0 {
		v.cursor[1] = idx
	}
}

func (v *statsView) moveCursor(delta int) {
	n := v.panelRows()
	if n == 0 {
		v.cursor[v.focus] = 0
		return
	}
	c := v.cursor[v.focus] + delta
	if c < 0 {
		c = 0
	}
	if c > n-1 {
		c = n - 1
	}
	v.cursor[v.focus] = c
}

// moveStatsCursor moves the active tab's cursor: the overview's own table
// cursor on overview, the candidates/repos panels' shared one otherwise
func (v *statsView) moveStatsCursor(delta int) {
	switch statsTabs[v.tab] {
	case "overview":
		v.moveOverviewCursor(delta)
	case "candidates", "repos":
		v.moveCursor(delta)
	}
}

func (v *statsView) moveOverviewCursor(delta int) {
	n := v.overviewRows(v.ovFocus)
	if n == 0 {
		v.ovCursor[v.ovFocus] = 0
		return
	}
	c := v.ovCursor[v.ovFocus] + delta
	if c < 0 {
		c = 0
	}
	if c > n-1 {
		c = n - 1
	}
	v.ovCursor[v.ovFocus] = c
}

// overviewRows is one overview table's row count: every scorecard row, or the
// repo rows in tokens order.
func (v statsView) overviewRows(focus int) int {
	if focus == 0 {
		return len(v.overviewCandRows(func(s string) string { return s }))
	}
	return len(v.overviewRepoRows())
}
