package ui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/view"
)

// actionEntry is one recorded cockpit action result for the session (:log).
type actionEntry struct {
	At   time.Time // when the result arrived (m.now())
	Verb string    // actionMsg.verb
	Key  string    // actionMsg.key (the binding key acted on; may be "")
	Text string    // the result text, or the error text
	Err  bool
}

// Model is the cockpit shell: a stack of views, status polling, global keys
// and message routing (§4.2). It keeps its name because RunSource and the
// tests construct it.
type Model struct {
	src  Source
	ctx  context.Context
	opts Options

	// stack is the view stack; stack[0] is the root and it is never empty
	// after newModel.
	stack []View

	report view.Report // newest good status; kept on a failed refresh
	err    error       // last refresh error
	notice string      // sticky footer notice; cleared by the next key

	// statusInFlight is the status fetch's single-flight guard. It is
	// separate from a view's own guards: a terminal read can block.
	statusInFlight bool
	statusLoaded   bool
	statusAt       time.Time
	started        bool // opts.Start has been executed (after the first status)

	width, height int
	ready         bool // the first WindowSizeMsg has arrived
	now           func() time.Time

	cmd  cmdLine // the command line; cmd.open shows it
	help bool    // help overlay shown

	// overlay is the one modal box the shell holds (§3): while it is set,
	// every key goes to it first (rule 2.5, §5.2).
	overlay overlay

	// running is the actions in flight, keyed by the binding they act on
	// (§4.3): the footer shows them, and a second action on the same binding
	// is refused.
	running map[string]string

	// actionLog is ':log': every action result this session, newest last, no
	// persistence (§4.3).
	actionLog []actionEntry

	// noticeErr and noticeFaint pick the notice's style: a red failure, a
	// faint captured stderr line, or the amber default (§4.3, §4.4).
	noticeErr   bool
	noticeFaint bool

	prefs prefs // the reduced prefs struct (§4.6); saved on every prefMsg
}

// newModel builds the shell at ':fleet'.
func newModel(ctx context.Context, src Source, opts Options) Model {
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
	m.stack = []View{newFleetView(true).withActions(opts.Actions != nil)}
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

// setPref applies one prefMsg: the sort pref also re-points the root fleet.
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

// top is the top view.
func (m Model) top() View { return m.stack[len(m.stack)-1] }

// env is what the shell lends a view on every call (§4.1).
func (m Model) env() Env {
	return Env{
		Ctx:       m.ctx,
		Src:       m.src,
		Report:    m.report,
		Loaded:    m.statusLoaded,
		StatusAt:  m.statusAt,
		Now:       m.now(),
		Width:     m.width,
		Height:    m.height,
		ErrRows:   errorRows(m.err, m.width),
		Actions:   m.opts.Actions,
		Running:   m.running,
		ActionLog: m.actionLog,
	}
}

// tick re-arms the poll. It is the ONLY timer; there is no goroutine.
func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Init fetches the first status and arms the tick.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchStatus(m.ctx, m.src),
		tick(m.opts.Interval),
	)
}

// Update routes one message (§5.2). It stays under the 70-line cap by
// delegating keys to updateKey and the stack-wide cases to updateStack.
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

	case tickMsg:
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

	case pushMsg:
		m.stack = append(m.stack, msg.v)
		return m, msg.init

	case popMsg:
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
		}
		return m, nil

	case rootMsg:
		if len(msg.vs) > 0 {
			m.stack = msg.vs
		}
		return m, msg.init

	case noticeMsg:
		m.notice = msg.text
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

	case logMsg:
		v, cmd := newLogView(m.env())
		m.stack = []View{
			newFleetView(m.prefs.Sort != "name").withActions(m.opts.Actions != nil),
			v,
		}
		return m, cmd

	case prefMsg:
		m = m.setPref(msg.key, msg.value)
		return m, m.save()

	case helpMsg:
		m.help = true
		return m, nil
	}

	return m.updateStack(msg)
}

// updateStatus stores a statusMsg as today, then runs the start command
// once the first one has arrived (§5.2).
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
	var start tea.Cmd
	if !m.started && m.opts.Start != "" {
		m.started = true
		start = execLine(m.opts.Start, m.env(), m.prefs)
	}
	return m, tea.Batch(cmd, start)
}

// finishAction applies one actionMsg (§4.3, §6): the notice is the first
// line of Text, or the error in errorStyle; the whole text -- or the error --
// is appended to ':log'; and a Refresh refetches status immediately.
func (m Model) finishAction(msg actionMsg) (tea.Model, tea.Cmd) {
	delete(m.running, msg.key)
	m.noticeErr = false
	m.noticeFaint = false
	text := msg.res.Text
	isErr := false
	if msg.res.Err != nil {
		m.notice = msg.res.Err.Error()
		m.noticeErr = true
		text = msg.res.Err.Error()
		isErr = true
	} else {
		m.notice = firstLine(msg.res.Text)
	}
	m.actionLog = append(m.actionLog, actionEntry{
		At:   m.now(),
		Verb: msg.verb,
		Key:  msg.key,
		Text: text,
		Err:  isErr,
	})
	var cmd tea.Cmd
	if msg.res.Refresh && !m.statusInFlight {
		m.statusInFlight = true
		cmd = fetchStatus(m.ctx, m.src)
	}
	return m, cmd
}

// firstLine is s up to its first newline.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// updateKey is §5.2's key routing, the first rule that applies.
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
	// Rule 2.5: a modal overlay owns every key while it is set (§3, §5.2).
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
// view (§4), and throws away every other mouse event. It is never forwarded
// to updateStack.
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

// updateTop forwards one message to the top view alone.
func (m Model) updateTop(msg tea.Msg) (tea.Model, tea.Cmd) {
	i := len(m.stack) - 1
	next, cmd := m.stack[i].Update(msg, m.env())
	m.stack[i] = next
	return m, cmd
}

// updateStack forwards one message to every view, bottom to top, replacing
// each at its index and batching their commands (§5.2).
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

// View draws the frame: header, context, error block, body, rule, keys
// (§5.3).
func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}
	env := m.env()
	rows := []string{m.headerView(env), fit("", env.Width), m.contextView(env)}
	if m.err != nil {
		rows = append(rows, strings.Split(m.errorBlock(env), "\n")...)
	}
	rows = append(rows, strings.Split(m.body(env), "\n")...)
	rows = append(rows, "", m.keysView(env))
	return joinLines(rows)
}

// joinLines joins rows with newlines.
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
