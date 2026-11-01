// Package stats owns the cockpit's stats view: the windowed rounds, cost and
// health board and its gate helpers.
package stats

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	datastats "github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// statsWindows is the `:stats` window cycle: 7d → 30d → 90d → all → 7d.
var statsWindows = []string{"7d", "30d", "90d", "all"}

// statsRefreshEvery is how stale a report may grow before a tick refetches it.
const statsRefreshEvery = 30 * time.Second

// statsTabs is the `:stats` tab bar, in tab-key order. A new view
// starts on overview.
var statsTabs = []string{"overview", "candidates", "tokens", "reliability", "repos"}

const (
	statsTabOverview = iota
	statsTabCandidates
	statsTabTokens
	statsTabReliability
	statsTabRepos
)

// StatsMsg is one stats fetch's reply: the report for Window, or the fetch
// error, plus any non-fatal read warnings.
type StatsMsg struct {
	Window   string
	Rep      datastats.Report
	Err      error
	Warnings []string
}

// statsView is ':stats': it reads the report from a fetch and never opens the
// database on the render path.
type statsView struct {
	window     string
	tab        int
	rep        datastats.Report
	loaded     bool // a report has arrived at least once
	err        error
	fetching   bool // a fetch is in flight
	fetchedAt  time.Time
	focus      int      // 0 = candidates panel, 1 = repos panel
	cursor     [2]int   // selected row per focusable panel
	split      int      // tokens split: 0 total, 1 candidate, 2 provider, 3 kind
	top        int      // first body line shown in the stacked layout
	ovFocus    int      // overview cursor: 0 candidates, 1 busiest repos
	ovCursor   [2]int   // selected row per overview table
	configured []string // every configured candidate's token, sorted; nil when the set is nil
	// expanded is the repos tab's view state: the repo keys whose children
	// show. A nil map is every repo collapsed.
	expanded map[string]bool
	// open opens the rounds view for an enter query, injected by the caller.
	open OpenRounds
}

// OpenRounds opens the rounds view for a stats enter query. The ui passes an
// adapter over its own rounds constructor; a test passes a stub.
type OpenRounds func(env widget.Env, query string) (widget.View, tea.Cmd, error)

// NewStatsView builds ':stats' on window. It refuses without a database; the
// returned command is the first fetch.
func NewStatsView(env widget.Env, window string, open OpenRounds) (widget.View, tea.Cmd, error) {
	if env.Src.Base().DB == nil {
		return nil, nil, relevo.ErrNoDatabase
	}
	v := statsView{window: window, fetching: true, split: 1, open: open}
	if rt := env.Src.Base(); rt.Candidates != nil {
		v.configured = rt.Candidates.Refs()
	}
	return v, v.fetch(env), nil
}

func notice(text string) tea.Cmd { return func() tea.Msg { return widget.NoticeMsg{Text: text} } }

func push(v widget.View, init tea.Cmd) tea.Cmd {
	return func() tea.Msg { return widget.PushMsg{View: v, Init: init} }
}

func (v statsView) fetch(env widget.Env) tea.Cmd {
	window := v.window
	rt := env.Src.Base()
	cut := cutFor(window, env.Now)
	return func() tea.Msg {
		in, warnings, err := relevo.StatsInputs(rt, cut)
		if err != nil {
			return StatsMsg{Window: window, Err: err}
		}
		return StatsMsg{Window: window, Rep: datastats.Build(in), Warnings: warnings}
	}
}

func (v statsView) Crumbs() []string { return []string{"stats"} }

func (v statsView) Capturing() bool { return false }

func (v statsView) Keys() []widget.KeyHelp {
	switch statsTabs[v.tab] {
	case "overview":
		return []widget.KeyHelp{
			{Key: "↑↓", Help: "move"},
			{Key: "←→", Help: "table"},
			{Key: "enter", Help: "its rounds"},
			{Key: "tab", Help: "next tab"},
			{Key: "w", Help: "window"},
		}
	case "candidates":
		return []widget.KeyHelp{
			{Key: "↑↓", Help: "move"},
			{Key: "enter", Help: "its rounds"},
			{Key: "tab", Help: "next tab"},
			{Key: "w", Help: "window"},
		}
	case "repos":
		return []widget.KeyHelp{
			{Key: "↑↓", Help: "move"},
			{Key: "space", Help: "expand"},
			{Key: "enter", Help: "its rounds"},
			{Key: "tab", Help: "next tab"},
			{Key: "w", Help: "window"},
		}
	case "tokens":
		return []widget.KeyHelp{
			{Key: "↑↓", Help: "scroll"},
			{Key: "s", Help: "split"},
			{Key: "tab", Help: "next tab"},
			{Key: "w", Help: "window"},
		}
	case "reliability":
		return []widget.KeyHelp{
			{Key: "↑↓", Help: "scroll"},
			{Key: "tab", Help: "next tab"},
			{Key: "w", Help: "window"},
		}
	}
	return []widget.KeyHelp{
		{Key: "tab", Help: "next tab"},
		{Key: "w", Help: "window"},
	}
}

// Context is the tabs row on the left and the windows and refresh state on the
// right. No dollars: tokens are what a subscription lane can watch.
func (v statsView) Context(env widget.Env) (string, string) {
	chips := make([]string, len(statsWindows))
	for i, w := range statsWindows {
		if w == v.window {
			chips[i] = widget.Chip(widget.ChipAccentStyle, w)
		} else {
			chips[i] = widget.MutedStyle.Render(widget.Chip(widget.NormalStyle, w))
		}
	}
	right := widget.FaintStyle.Render("window  ") + strings.Join(chips, " ")
	if v.fetching {
		right += " · refreshing"
	} else if v.err != nil {
		right += " · " + widget.ErrorStyle.Render(v.err.Error())
	}
	right += "  "
	return v.statsTabsRow(), right
}

func (v statsView) Body(env widget.Env, width, height int) string {
	if !v.loaded && v.err != nil {
		lines := strings.Split(widget.RenderError(v.err, width), "\n")
		return strings.Join(widget.FitLines(lines, width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(widget.BlockLines([]string{"loading…"}, width, height), "\n")
	}
	if v.rep.Totals.Rounds == 0 {
		return strings.Join(widget.Centered("no rounds in this window", width, height), "\n")
	}
	avail := height - 1
	body, _ := v.tabLines(env, width, avail)
	if avail < 0 {
		avail = 0
	}
	start := v.top
	if start > len(body)-avail {
		start = len(body) - avail
	}
	if start < 0 {
		start = 0
	}
	end := start + avail
	if end > len(body) {
		end = len(body)
	}
	head := v.scrollHint(width, len(body), avail, start)
	return strings.Join(widget.FitLines(append([]string{head}, body[start:end]...), width, height), "\n")
}

// scrollHint is the body's head line: blank while the whole body fits, else a
// faint, right-aligned hint ending at width-3 that names the way off this page.
// start is the clamped first body line shown.
func (v statsView) scrollHint(width, n, avail, start int) string {
	if n <= avail {
		return ""
	}
	text := "pgup · pgdn"
	maxTop := statsMaxTop(n, avail)
	switch start {
	case maxTop:
		text = "more above · pgup"
	case 0:
		text = "more below · pgdn"
	}
	pad := width - 3 - lipgloss.Width(text)
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + widget.FaintStyle.Render(text)
}

func (v statsView) statsTabsRow() string {
	words := make([]string, len(statsTabs))
	for i, t := range statsTabs {
		if i == v.tab {
			words[i] = widget.Chip(widget.ChipAccentStyle, t)
		} else {
			words[i] = widget.MutedStyle.Render(widget.Chip(widget.NormalStyle, t))
		}
	}
	return "   " + strings.Join(words, "   ")
}

// tabLines is the active tab's body. sel is the visible line of
// the overview's selected table row, or -1 on every other tab.
func (v statsView) tabLines(env widget.Env, width, height int) ([]string, int) {
	switch statsTabs[v.tab] {
	case "candidates":
		return v.candidatesTabLines(env, width)
	case "tokens":
		return v.tokensTabLines(env, width, height), -1
	case "reliability":
		return v.reliabilityTabLines(env, width), -1
	case "repos":
		return v.reposTabLines(env, width)
	}
	return v.overviewLines(env, width, height)
}

// page is the body lines as Body would lay them out. sel is the tab's
// selected line, -1 when the tab has none. It is only meaningful once a report
// is loaded and non-empty.
func (v statsView) page(env widget.Env) (lines []string, sel, avail int) {
	avail = widget.BodyHeight(env) - 1
	if !v.loaded || v.rep.Totals.Rounds == 0 {
		return nil, -1, avail
	}
	lines, sel = v.tabLines(env, env.Width, avail)
	return lines, sel, avail
}

// follow scrolls the page to keep the overview's selected row visible after a
// cursor or focus move; on a tab with no selected row it does nothing.
func (v *statsView) follow(env widget.Env) {
	lines, sel, avail := v.page(env)
	if sel < 0 {
		return
	}
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+avail {
		v.top = sel - avail + 1
	}
	maxTop := statsMaxTop(len(lines), avail)
	if v.top < 0 {
		v.top = 0
	}
	if v.top > maxTop {
		v.top = maxTop
	}
}
