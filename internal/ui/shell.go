package ui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/text"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/view"
)

// Model is the cockpit shell: a stack of views, status polling, global keys
// and message routing. It keeps its name because RunSource and the
// tests construct it.
type Model struct {
	src  widget.Source
	ctx  context.Context
	opts Options

	// stack is the view stack; stack[0] is the root and it is never empty
	// after newModel.
	stack []widget.View

	report view.Report // newest good status; kept on a failed refresh
	err    error       // last refresh error
	notice string      // sticky footer notice; cleared by the next key

	// statusInFlight is the status fetch's single-flight guard. It is
	// separate from a view's own guards: a terminal read can block.
	statusInFlight bool
	statusLoaded   bool
	statusAt       time.Time
	started        bool // opts.Start has been executed (after the first status)

	// firstFetchSlow is set by the watchdog when the first status outran one
	// poll interval. It is cleared by the refetch it triggers, so the re-fire
	// happens once per slow first load and never again.
	firstFetchSlow bool

	width, height int
	ready         bool // the first WindowSizeMsg has arrived
	now           func() time.Time

	cmd  cmdLine // the command line; cmd.open shows it
	help bool    // help overlay shown

	// overlay is the one modal box the shell holds: while it is set,
	// every key goes to it first.
	overlay overlay

	// running is the actions in flight, keyed by the binding they act on:
	// the footer shows them, and a second action on the same binding
	// is refused.
	running map[string]string

	// actionLog is ':log': every action result this session, newest last, no
	// persistence.
	actionLog []widget.ActionEntry

	// noticeErr marks an action's error (red), noticeFaint a captured stderr
	// line (faint); neither is the amber default.
	noticeErr   bool
	noticeFaint bool

	prefs prefs // the reduced prefs struct; saved on every widget.PrefMsg
}

func newModel(ctx context.Context, src widget.Source, opts Options) Model {
	m := Model{
		src:            src,
		ctx:            ctx,
		opts:           opts,
		statusInFlight: true,
		now:            time.Now,
		cmd:            newCmdLine(),
		running:        map[string]string{},
		prefs:          prefs{Sort: "attention"},
	}
	m.stack = []widget.View{newFleetView(true).withActions(opts.Actions != nil)}
	return m
}

// applyPrefs sets the shell from p; zero values mean the defaults.
func (m Model) applyPrefs(p prefs) Model {
	m.prefs = p
	if fv, ok := m.stack[0].(fleetView); ok {
		fv.attention = p.Sort != "name"
		m.stack[0] = fv
	}
	return m
}

// save is the command every preference change returns: the save when a
// store is configured, nil otherwise.
func (m Model) save() tea.Cmd {
	if m.opts.Prefs.KV == nil {
		return nil
	}
	return savePrefs(m.opts.Prefs, m.prefs)
}

// setPref applies one widget.PrefMsg: the sort pref also re-points the root fleet.
func (m Model) setPref(key, value string) Model {
	switch key {
	case "sort":
		m.prefs.Sort = value
		if fv, ok := m.stack[0].(fleetView); ok {
			fv.attention = value != "name"
			m.stack[0] = fv
		}
	case "dashboard":
		m.prefs.Dashboard = value
	case "dashboard_sort":
		m.prefs.DashboardSort = value
	}
	return m
}

func (m Model) top() widget.View { return m.stack[len(m.stack)-1] }

func (m Model) env() widget.Env {
	return widget.Env{
		Ctx:       m.ctx,
		Src:       m.src,
		Report:    m.report,
		Loaded:    m.statusLoaded,
		StatusAt:  m.statusAt,
		Now:       m.now(),
		Width:     m.width,
		Height:    m.height,
		ErrRows:   widget.ErrorRows(m.err, m.width),
		Actions:   m.opts.Actions,
		Running:   m.running,
		ActionLog: m.actionLog,
	}
}

// tick re-arms the poll. It is the ONLY timer; there is no goroutine.
func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return widget.TickMsg(t) })
}

// watchdog is the first fetch's time-box: one timer, armed alongside the
// first fetch, that says whether that fetch outran a poll interval. It
// carries no report, so a slow widget.Source.Status cannot merge a partial one.
func watchdog(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return watchdogMsg(t) })
}

// Init fetches the first status and arms the tick. The watchdog goes out with
// it, so a first fetch that has not landed by the time the first tick was due
// is known to be slow rather than merely late.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchStatus(m.ctx, m.src),
		tick(m.opts.Interval),
		watchdog(m.opts.Interval),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.updateKey(msg)

	case tea.MouseMsg:
		return m.updateMouse(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		return m.updateStack(msg)

	case widget.TickMsg:
		cmds := []tea.Cmd{tick(m.opts.Interval)}
		if !m.statusInFlight {
			m.statusInFlight = true
			cmds = append(cmds, fetchStatus(m.ctx, m.src))
		}
		next, cmd := m.updateStack(msg)
		m = next.(Model)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)

	case statusMsg:
		return m.updateStatus(msg)

	case watchdogMsg:
		return m.onWatchdog()

	case widget.PushMsg:
		m.stack = append(m.stack, msg.View)
		return m, msg.Init

	case widget.PopMsg:
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
		}
		return m, nil

	case widget.RootMsg:
		if len(msg.Views) > 0 {
			m.stack = msg.Views
		}
		return m, msg.Init

	case widget.NoticeMsg:
		m.notice = msg.Text
		m.noticeErr = false
		m.noticeFaint = false
		return m, nil

	case openOverlayMsg:
		m.overlay = msg.ov
		return m, nil

	case workingMsg:
		if m.running == nil {
			m.running = map[string]string{}
		}
		m.running[msg.key] = msg.verb
		return m, nil

	case actionMsg:
		return m.finishAction(msg)

	case stderrMsg:
		m.notice = msg.line
		m.noticeErr = false
		m.noticeFaint = true
		return m, nil

	case widget.LogMsg:
		v, cmd := newLogView(m.env())
		m.stack = []widget.View{
			newFleetView(m.prefs.Sort != "name").withActions(m.opts.Actions != nil),
			v,
		}
		return m, cmd

	case widget.PrefMsg:
		m = m.setPref(msg.Key, msg.Value)
		return m, m.save()

	case widget.HelpMsg:
		m.help = true
		return m, nil
	}

	return m.updateStack(msg)
}

// updateStatus stores a statusMsg as today, then runs the start command once.
// A status the watchdog called slow refetches on landing, so the first paint
// is never followed by an interval of stale rows.
func (m Model) updateStatus(msg statusMsg) (tea.Model, tea.Cmd) {
	m.statusInFlight = false
	if msg.err != nil {
		m.err = msg.err
	} else {
		m.statusLoaded = true
		m.err = nil
		m.report = msg.report
		m.statusAt = m.now()
	}
	next, cmd := m.updateStack(msg)
	m = next.(Model)
	var start, refetch tea.Cmd
	if !m.started && m.opts.Start != "" {
		m.started = true
		start = execLine(m.opts.Start, m.env(), m.prefs)
	}
	if m.firstFetchSlow {
		// The re-fire is on landing, so it cannot slow the slow path: this
		// fetch has already returned. The single-flight guard stays set
		// across it, so the tick in between starts no second one.
		m.firstFetchSlow = false
		m.statusInFlight = true
		refetch = fetchStatus(m.ctx, m.src)
	}
	return m, tea.Batch(cmd, start, refetch)
}

// onWatchdog records that the first fetch outlived one poll interval, and
// paints nothing new: env.Loaded is still false, so the fleet is already
// drawing its bounded loading state and the watchdog carries no report to
// merge into it.
func (m Model) onWatchdog() (tea.Model, tea.Cmd) {
	m.firstFetchSlow = !m.statusLoaded
	return m, nil
}

// finishAction applies one actionMsg: the notice is the first line of Text
// while ':log' keeps the whole text, and a Refresh refetches status
// immediately.
func (m Model) finishAction(msg actionMsg) (tea.Model, tea.Cmd) {
	delete(m.running, msg.key)
	m.noticeErr = false
	m.noticeFaint = false
	logText := msg.res.Text
	isErr := false
	if msg.res.Err != nil {
		m.notice = msg.res.Err.Error()
		m.noticeErr = true
		logText = msg.res.Err.Error()
		isErr = true
	} else {
		m.notice = text.FirstRaw(msg.res.Text)
	}
	m.actionLog = append(m.actionLog, widget.ActionEntry{
		At:   m.now(),
		Verb: msg.verb,
		Key:  msg.key,
		Text: logText,
		Err:  isErr,
	})
	var cmd tea.Cmd
	if msg.res.Refresh && !m.statusInFlight {
		m.statusInFlight = true
		cmd = fetchStatus(m.ctx, m.src)
	}
	return m, cmd
}

func (m Model) updateKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.notice = ""
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.cmd.open {
		var cmd tea.Cmd
		m.cmd, cmd = m.cmd.update(k, m.env(), m.prefs)
		return m, cmd
	}
	// A modal overlay owns every key while it is set.
	if m.overlay != nil {
		ov, cmd, closed := m.overlay.update(k)
		if closed {
			m.overlay = nil
		} else {
			m.overlay = ov
		}
		return m, cmd
	}
	if m.help {
		switch k.String() {
		case "esc", "?", "q":
			m.help = false
		}
		return m, nil
	}
	if m.top().Capturing() {
		return m.updateTop(k)
	}
	switch k.String() {
	case ":":
		m.cmd = m.cmd.opened()
		return m, nil
	case "?":
		m.help = true
		return m, nil
	case "esc":
		if len(m.stack) > 1 {
			return m, pop()
		}
		// At the root the top view still gets esc: the fleet clears an
		// applied filter with it (A4), and every other view ignores it.
		return m.updateTop(k)
	case "q":
		if len(m.stack) == 1 {
			return m, tea.Quit
		}
		return m, pop()
	}
	return m.updateTop(k)
}

// wheelStep is the number of up/down keys one wheel event becomes. It
// matches the bubbles viewport's default MouseWheelDelta of 3.
const wheelStep = 3

// updateMouse turns one wheel event into wheelStep arrow keys for the top
// view; every other mouse event is thrown away and nothing is forwarded to
// updateStack.
func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	var k tea.KeyMsg
	switch {
	case msg.Action != tea.MouseActionPress:
		return m, nil
	case msg.Button == tea.MouseButtonWheelUp:
		k = tea.KeyMsg{Type: tea.KeyUp}
	case msg.Button == tea.MouseButtonWheelDown:
		k = tea.KeyMsg{Type: tea.KeyDown}
	default:
		return m, nil
	}
	if m.cmd.open || m.overlay != nil || m.help || m.top().Capturing() {
		return m, nil
	}
	var cmds []tea.Cmd
	for range wheelStep {
		next, cmd := m.updateTop(k)
		m = next.(Model)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

func (m Model) updateTop(msg tea.Msg) (tea.Model, tea.Cmd) {
	i := len(m.stack) - 1
	next, cmd := m.stack[i].Update(msg, m.env())
	m.stack[i] = next
	return m, cmd
}

func (m Model) updateStack(msg tea.Msg) (tea.Model, tea.Cmd) {
	env := m.env()
	var cmds []tea.Cmd
	for i, v := range m.stack {
		next, cmd := v.Update(msg, env)
		m.stack[i] = next
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}
	env := m.env()
	rows := []string{m.headerView(env), widget.Fit("", env.Width), m.contextView(env)}
	if m.err != nil {
		rows = append(rows, strings.Split(m.errorBlock(env), "\n")...)
	}
	rows = append(rows, strings.Split(m.body(env), "\n")...)
	rows = append(rows, "", m.keysView(env))
	return joinLines(rows)
}

func joinLines(rows []string) string {
	out := ""
	for i, r := range rows {
		if i > 0 {
			out += "\n"
		}
		out += r
	}
	return out
}
