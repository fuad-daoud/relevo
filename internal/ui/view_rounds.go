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
)

// roundOpenMsg asks the rounds view to push a round detail. Exactly one of
// key (a live row's Key()) and hist (an archived row) is set.
type roundOpenMsg struct {
	key   string
	hist  *relevo.HistoryBinding
	round int
}

// dashStyles is the dashboard's slice of the ui's palette, so the screen
// never duplicates a colour (§5). Moved here from model.go:549 (R2.5).
func dashStyles() dash.Styles {
	return dash.Styles{
		Fg:        fgStyle,
		Dim:       dimStyle,
		Faint:     faintStyle,
		Error:     errorStyle,
		Empty:     emptyStyle,
		Selected:  selectedBg,
		Archived:  archivedStyle,
		Attention: stateNeedsYouStyle,
		Live:      stateActiveStyle,
		Strong:    textStyle.Bold(true),
		Accent:    accentStyle,
		Warn:      warnStyle,
		Ok:        greenStyle,
		Danger:    redStyle,
		Grid:      gridStyle,
		Chip:      chipAccentStyle,
	}
}

// runningRounds builds a lookup for rounds currently running in the working group.
func runningRounds(env Env) func(string, int) bool {
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

// newRoundsView hosts the dashboard as a view (§4.5). It refuses without a
// database, exactly as enterDash does today (model.go:551-570).
func newRoundsView(env Env, query, sortKey string) (View, tea.Cmd, error) {
	if env.Src.Base().DB == nil {
		return nil, nil, relevo.ErrNoDatabase
	}
	d := dash.New(env.Src.Base().DB, time.Local, envNow(env), query, sortKey)
	d.Embedded = true
	d.Running = runningRounds(env)
	d.Names = func(token string) string { return env.Src.Base().Candidates.NameOf(token) }
	d.SetStyles(dashStyles())
	d.SetSize(env.Width, bodyHeight(env))
	return roundsView{dash: d}, d.Init(), nil
}

// roundsView is ':rounds': today's dashboard grid, hosted (§4.5).
type roundsView struct {
	dash dash.Model
}

func (r roundsView) Crumbs() []string { return []string{"rounds"} }

func (r roundsView) Capturing() bool { return r.dash.Editing() }

func clip(s string, width int) string { return clipName(s, width) }

// Context names the summary line or problem, and the regroup axis or sort key (§5.5).
func (r roundsView) Context(env Env) (string, string) {
	r.dash.Running = runningRounds(env)
	var right string
	if f := r.dash.FilterText(); f != "" {
		right += dimStyle.Render("/ "+clip(f, 40)) + "   "
	}
	if axis := r.dash.GroupAxis(); axis != "" {
		axisLabel := axis
		if axis == "builder" {
			axisLabel = "candidate"
		}
		right += faintStyle.Render("by ") + chip(chipAccentStyle, axisLabel)
	} else {
		right += faintStyle.Render("sort ") + dimStyle.Render(r.dash.SortLabel())
	}
	right += "   "

	var left string
	if p := r.dash.Problem(); p != "" {
		left = "   " + errorStyle.Render(p)
	} else {
		maxW := env.Width - 3 - lipgloss.Width(right) - 3
		left = "   " + r.dash.SummaryLine(maxW)
	}
	return left, right
}

func (r roundsView) Keys() []KeyHelp {
	enterHelp := "open round"
	if r.dash.GroupAxis() != "" {
		enterHelp = "expand"
	}
	return []KeyHelp{
		{"↑↓", "move"},
		{"enter", enterHelp},
		{"/", "filter"},
		{"b", "regroup"},
		{"s", "sort"},
		{"S", "flip"},
		{"r", "refresh"},
	}
}

func (r roundsView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		if r.dash.ShouldRefresh(env.Now) {
			return r, r.dash.Refresh()
		}
		return r, nil

	case tea.WindowSizeMsg:
		r.dash.SetSize(env.Width, bodyHeight(env))
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
		cmds = append(cmds, func() tea.Msg { return prefMsg{"dashboard", q1} })
	}
	if s1 != s0 {
		cmds = append(cmds, func() tea.Msg { return prefMsg{"dashboard_sort", s1} })
	}
	return r, tea.Batch(cmds...)
}

// Body is the dashboard grid, sized to the shell's body box.
func (r roundsView) Body(env Env, width, height int) string {
	r.dash.Running = runningRounds(env)
	r.dash.SetSize(width, height)
	return r.dash.View()
}

// openRound resolves a dashboard jump to the round it names: a live row
// first, then the database, then a notice (§5.4). Asynchronous, because the
// database read can block.
func openRound(env Env, msg dash.JumpMsg) tea.Cmd {
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
		return noticeMsg{text: fmt.Sprintf("%s not found", msg.BindingName)}
	}
}

// execLine runs one command line against env, as the ':' command line does
// and the shell does for Options.Start (§6).
func execLine(line string, env Env, p prefs) tea.Cmd {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	name := fields[0]
	args := fields[1:]
	switch name {
	case "fleet":
		return root(newFleetView(p.Sort != "name").withActions(env.Actions != nil))
	case "log":
		return func() tea.Msg { return logMsg{} }
	case "rounds":
		q := strings.Join(args, " ")
		if q == "" {
			q = p.Dashboard
		}
		v, init, err := newRoundsView(env, q, p.DashboardSort)
		if err != nil {
			return notice("no database: " + err.Error())
		}
		return rootThen(init, v)
	case "round":
		if len(args) == 0 {
			return notice(`unknown binding ""`)
		}
		key := args[0]
		n := 0
		if len(args) > 1 {
			fmt.Sscanf(args[1], "%d", &n)
		}
		live := false
		for i := range env.Report.Bindings {
			if env.Report.Bindings[i].Key() == key {
				live = true
				break
			}
		}
		if !live {
			return notice(fmt.Sprintf("unknown binding %q", key))
		}
		v, cmd := newRoundView(env, key, n)
		return rootThen(cmd, newFleetView(p.Sort != "name").withActions(env.Actions != nil), v)
	case "stats":
		window := "30d"
		if len(args) > 0 {
			window = args[0]
		}
		switch window {
		case "7d", "30d", "90d", "all":
		default:
			return notice("stats: want 7d, 30d, 90d or all")
		}
		v, init, err := newStatsView(env, window)
		if err != nil {
			return notice("no database: " + err.Error())
		}
		return rootThen(init, v)
	case "candidates":
		if env.Actions == nil {
			return notice("the config views need relevo ui on this machine")
		}
		v, init := newCandidatesView(env)
		return rootThen(init, v)
	case "actors":
		if env.Actions == nil {
			return notice("the config views need relevo ui on this machine")
		}
		v, init := newActorsView(env)
		return rootThen(init, v)
	case "agents":
		if env.Actions == nil {
			return notice("the config views need relevo ui on this machine")
		}
		v, init := newAgentsView(env)
		return rootThen(init, v)
	case "settings":
		if env.Actions == nil {
			return notice("the config views need relevo ui on this machine")
		}
		v, init := newSettingsView(env)
		return rootThen(init, v)
	case "audit":
		if env.Actions == nil {
			return notice("the config views need relevo ui on this machine")
		}
		v, init := newAuditView(env)
		return rootThen(init, v)
	case "ungate":
		if len(args) == 0 {
			return notice("usage: ungate <provider|candidate>")
		}
		if env.Actions == nil {
			return nil
		}
		subject := args[0]
		return runAction(env.Ctx, "ungate", subject, func(ctx context.Context) Result {
			return env.Actions.Ungate(ctx, subject)
		})
	case "help":
		return func() tea.Msg { return helpMsg{} }
	case "quit":
		return tea.Quit
	}
	return notice(fmt.Sprintf("unknown command %q (try :help)", ":"+name))
}
