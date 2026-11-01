package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/ui/dash"
	"github.com/fuad-daoud/relevo/internal/ui/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// Exactly one of key (a live row's Key()) and hist (an archived row) is set.
type roundOpenMsg struct {
	key   string
	hist  *relevo.HistoryBinding
	round int
}

// The dashboard's slice of the ui's palette, so the screen never duplicates
// a colour.
func dashStyles() dash.Styles {
	return dash.Styles{
		Fg:        widget.TextStyle,
		Dim:       widget.MutedStyle,
		Faint:     widget.FaintStyle,
		Error:     widget.ErrorStyle,
		Empty:     widget.EmptyStyle,
		Selected:  widget.SelBandStyle,
		Archived:  widget.ArchivedStyle,
		Attention: widget.StateNeedsYouStyle,
		Live:      widget.StateActiveStyle,
		Strong:    widget.TextStyle.Bold(true),
		Accent:    widget.AccentStyle,
		Warn:      widget.WarnStyle,
		Ok:        widget.GreenStyle,
		Danger:    widget.RedStyle,
		Grid:      widget.GridStyle,
		Chip:      widget.ChipAccentStyle,
	}
}

func runningRounds(env widget.Env) func(string, int) bool {
	m := make(map[string]int)
	for _, b := range env.Report.Bindings {
		if groupOf(b) == groupWorking {
			m[b.Name] = b.Round
		}
	}
	return func(name string, n int) bool {
		r, ok := m[name]
		return ok && r == n
	}
}

func newRoundsView(env widget.Env, query, sortKey string) (widget.View, tea.Cmd, error) {
	if env.Src.Base().DB == nil {
		return nil, nil, relevo.ErrNoDatabase
	}
	d := dash.New(env.Src.Base().DB, time.Local, envNow(env), query, sortKey)
	d.Embedded = true
	d.Running = runningRounds(env)
	d.Names = func(token string) string { return env.Src.Base().Candidates.NameOf(token) }
	d.SetStyles(dashStyles())
	d.SetSize(env.Width, widget.BodyHeight(env))
	return roundsView{dash: d}, d.Init(), nil
}

type roundsView struct {
	dash dash.Model
}

func (r roundsView) Crumbs() []string { return []string{"rounds"} }

func (r roundsView) Capturing() bool { return r.dash.Editing() }

func clip(s string, width int) string { return clipName(s, width) }

func (r roundsView) Context(env widget.Env) (string, string) {
	r.dash.Running = runningRounds(env)
	var right string
	if f := r.dash.FilterText(); f != "" {
		right += widget.MutedStyle.Render("/ "+clip(f, 40)) + "   "
	}
	if axis := r.dash.GroupAxis(); axis != "" {
		axisLabel := axis
		if axis == "builder" {
			axisLabel = "candidate"
		}
		right += widget.FaintStyle.Render("by ") + widget.Chip(widget.ChipAccentStyle, axisLabel)
	} else {
		right += widget.FaintStyle.Render("sort ") + widget.MutedStyle.Render(r.dash.SortLabel())
	}
	right += "   "

	var left string
	if p := r.dash.Problem(); p != "" {
		left = "   " + widget.ErrorStyle.Render(p)
	} else {
		maxW := env.Width - 3 - lipgloss.Width(right) - 3
		left = "   " + r.dash.SummaryLine(maxW)
	}
	return left, right
}

func (r roundsView) Keys() []widget.KeyHelp {
	enterHelp := "open round"
	if r.dash.GroupAxis() != "" {
		enterHelp = "expand"
	}
	return []widget.KeyHelp{
		{Key: "↑↓", Help: "move"},
		{Key: "enter", Help: enterHelp},
		{Key: "/", Help: "filter"},
		{Key: "b", Help: "regroup"},
		{Key: "s", Help: "sort"},
		{Key: "S", Help: "flip"},
		{Key: "r", Help: "refresh"},
	}
}

func (r roundsView) Update(msg tea.Msg, env widget.Env) (widget.View, tea.Cmd) {
	switch msg := msg.(type) {
	case widget.TickMsg:
		if r.dash.ShouldRefresh(env.Now) {
			return r, r.dash.Refresh()
		}
		return r, nil

	case tea.WindowSizeMsg:
		r.dash.SetSize(env.Width, widget.BodyHeight(env))
		return r, nil

	case dash.JumpMsg:
		return r, openRound(env, msg)

	case roundOpenMsg:
		if msg.hist != nil {
			v, cmd := newHistRoundView(env, *msg.hist, msg.round)
			return r, push(v, cmd)
		}
		v, cmd := newRoundView(env, msg.key, msg.round)
		return r, push(v, cmd)
	}

	q0, s0 := r.dash.QueryText(), r.dash.SortKey()
	next, cmd := r.dash.Update(msg)
	r.dash = next
	q1, s1 := r.dash.QueryText(), r.dash.SortKey()
	var cmds []tea.Cmd
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	if q1 != q0 {
		cmds = append(cmds, func() tea.Msg { return widget.PrefMsg{Key: "dashboard", Value: q1} })
	}
	if s1 != s0 {
		cmds = append(cmds, func() tea.Msg { return widget.PrefMsg{Key: "dashboard_sort", Value: s1} })
	}
	return r, tea.Batch(cmds...)
}

func (r roundsView) Body(env widget.Env, width, height int) string {
	r.dash.Running = runningRounds(env)
	r.dash.SetSize(width, height)
	return r.dash.View()
}

// Asynchronous, because the database read can block.
func openRound(env widget.Env, msg dash.JumpMsg) tea.Cmd {
	return func() tea.Msg {
		for i := range env.Report.Bindings {
			if env.Report.Bindings[i].Name == msg.BindingName {
				return roundOpenMsg{key: env.Report.Bindings[i].Key(), round: msg.Round}
			}
		}
		rows, err := relevo.Bindings(env.Ctx, env.Src.Base(), "")
		if err == nil {
			for i := range rows {
				if msg.BindingID != "" && rows[i].ID == msg.BindingID {
					h := rows[i]
					return roundOpenMsg{hist: &h, round: msg.Round}
				}
			}
			for i := range rows {
				if rows[i].Name == msg.BindingName {
					h := rows[i]
					return roundOpenMsg{hist: &h, round: msg.Round}
				}
			}
		}
		return widget.NoticeMsg{Text: fmt.Sprintf("%s not found", msg.BindingName)}
	}
}

func execLine(line string, env widget.Env, p prefs) tea.Cmd {
	name, args := parseExecLine(line)
	if name == "" {
		return nil
	}
	switch name {
	case "fleet":
		return root(newFleetView(p.Sort != "name").withActions(env.Actions != nil))
	case "log":
		return func() tea.Msg { return widget.LogMsg{} }
	case "rounds":
		return roundsExecCmd(args, env, p)
	case "round":
		return roundExecCmd(args, env, p)
	case "stats":
		return statsExecCmd(args, env, p)
	case "chains", "candidates", "actors", "workflows", "agents", "servers", "sync", "settings", "audit":
		return configExecCmd(name, env)
	case "ungate":
		return ungateExecCmd(args, env)
	case "help":
		return func() tea.Msg { return widget.HelpMsg{} }
	case "quit":
		return tea.Quit
	}
	return notice(fmt.Sprintf("unknown command %q (try :help)", ":"+name))
}

func parseExecLine(line string) (string, []string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], fields[1:]
}

func liveBinding(env widget.Env, key string) bool {
	for i := range env.Report.Bindings {
		if env.Report.Bindings[i].Key() == key {
			return true
		}
	}
	return false
}

func roundsExecCmd(args []string, env widget.Env, p prefs) tea.Cmd {
	q := strings.Join(args, " ")
	if q == "" {
		q = p.Dashboard
	}
	v, init, err := newRoundsView(env, q, p.DashboardSort)
	if err != nil {
		return notice("no database: " + err.Error())
	}
	return rootThen(init, v)
}

func roundExecCmd(args []string, env widget.Env, p prefs) tea.Cmd {
	if len(args) == 0 {
		return notice(`unknown binding ""`)
	}
	key := args[0]
	n := 0
	if len(args) > 1 {
		fmt.Sscanf(args[1], "%d", &n)
	}
	if !liveBinding(env, key) {
		return notice(fmt.Sprintf("unknown binding %q", key))
	}
	v, cmd := newRoundView(env, key, n)
	return rootThen(cmd, newFleetView(p.Sort != "name").withActions(env.Actions != nil), v)
}

func statsExecCmd(args []string, env widget.Env, p prefs) tea.Cmd {
	window := "30d"
	if len(args) > 0 {
		window = args[0]
	}
	switch window {
	case "7d", "30d", "90d", "all":
	default:
		return notice("stats: want 7d, 30d, 90d or all")
	}
	open := func(env widget.Env, q string) (widget.View, tea.Cmd, error) {
		return newRoundsView(env, q, "")
	}
	v, init, err := stats.NewStatsView(env, window, open)
	if err != nil {
		return notice("no database: " + err.Error())
	}
	return rootThen(init, v)
}

func configExecCmd(name string, env widget.Env) tea.Cmd {
	if env.Actions == nil {
		return notice("the config views need relevo ui on this machine")
	}
	switch name {
	case "chains":
		v, init := newChainsView(env)
		return rootThen(init, v)
	case "candidates":
		v, init := newCandidatesView(env)
		return rootThen(init, v)
	case "actors":
		v, init := newActorsView(env)
		return rootThen(init, v)
	case "workflows":
		v, init := newWorkflowsView(env)
		return rootThen(init, v)
	case "agents":
		v, init := newAgentsView(env)
		return rootThen(init, v)
	case "servers":
		v, init := newServersView(env)
		return rootThen(init, v)
	case "sync":
		v, init := newSyncView(env)
		return rootThen(init, v)
	case "settings":
		v, init := newSettingsView(env)
		return rootThen(init, v)
	case "audit":
		v, init := newAuditView(env)
		return rootThen(init, v)
	}
	return nil
}

func ungateExecCmd(args []string, env widget.Env) tea.Cmd {
	if len(args) == 0 {
		return notice("usage: ungate <provider|candidate>")
	}
	if env.Actions == nil {
		return nil
	}
	subject := args[0]
	return runAction(env.Ctx, "ungate", subject, func(ctx context.Context) widget.Result {
		return env.Actions.Ungate(ctx, subject)
	})
}
