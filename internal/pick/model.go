package pick

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/view"
)

type screen int

const (
	screenList screen = iota
	screenResult
	screenConfirm
)

// statusMsg is the list's rows, or why there are none.
type statusMsg struct {
	report view.Report
	err    error
}

// verbDoneMsg is what the verb said, or why it failed.
type verbDoneMsg struct {
	text string
	err  error
}

// Model is the whole picker: one of four screens at a time. Every terminal
// and store call runs in a tea.Cmd and comes back as a message, so Update
// is pure and tests drive it with messages.
type Model struct {
	ctx  context.Context
	rt   relevo.Runtime
	opts Options

	screen        screen
	width, height int

	// list screen
	rows   []view.BindingStatus
	loaded bool // false until the first statusMsg
	cursor int
	top    int

	result resultModel
	// confirm is the row a done/unbind is waiting on a `y` for (#103).
	// Meaningful only while screen == screenConfirm.
	confirm view.BindingStatus

	// outcome is what Run returns: nil after a verb succeeded, else one of
	// the sentinels in verb.go. Set exactly once, by quit.
	outcome error
}

func newModel(ctx context.Context, rt relevo.Runtime, opts Options) Model {
	return Model{ctx: ctx, rt: rt, opts: opts, screen: screenList}
}

func (m Model) Init() tea.Cmd {
	return fetchStatus(m.ctx, m.rt)
}

func fetchStatus(ctx context.Context, rt relevo.Runtime) tea.Cmd {
	return func() tea.Msg {
		rep, err := relevo.Status(ctx, rt)
		if err != nil {
			return statusMsg{err: err}
		}
		return statusMsg{report: rep}
	}
}

// runVerb runs the chosen verb and reports its text, which is the same text
// the CLI prints (spec §5).
func runVerb(ctx context.Context, rt relevo.Runtime, opts Options, name string) tea.Cmd {
	return func() tea.Msg {
		switch opts.Verb {
		case VerbDone:
			res, err := relevo.Done(ctx, rt, name)
			if err != nil {
				return verbDoneMsg{err: err}
			}
			return verbDoneMsg{text: relevo.DoneText(name, res)}
		case VerbUnbind:
			res, err := relevo.Unbind(ctx, rt, name, opts.Archive)
			if err != nil {
				return verbDoneMsg{err: err}
			}
			return verbDoneMsg{text: relevo.UnbindText(name, res)}
		}
		return verbDoneMsg{err: fmt.Errorf("unknown verb %q", opts.Verb)}
	}
}

// quit records the outcome and ends the program. It is the only place
// outcome is written.
func (m Model) quit(outcome error) (tea.Model, tea.Cmd) {
	m.outcome = outcome
	return m, tea.Quit
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.top = listWindow(m.top, m.cursor, m.listRows(), len(m.rows))
		return m, nil

	case statusMsg:
		return m.onStatus(msg)

	case verbDoneMsg:
		m.result = resultModel{text: msg.text, err: msg.err}
		if msg.err != nil {
			m.result.outcome = ErrVerbFailed
		}
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m.quit(ErrCancelled)
		}
		switch m.screen {
		case screenList:
			return m.listKeys(msg)
		case screenResult:
			return m.resultKeys(msg)
		case screenConfirm:
			return m.confirmKeys(msg)
		}
	}
	return m, nil
}

func (m Model) onStatus(msg statusMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.screen = screenResult
		m.result = resultModel{err: msg.err, outcome: ErrVerbFailed}
		return m, nil
	}
	m.rows = rowsFor(m.opts.Verb, msg.report)
	m.loaded = true
	if len(m.rows) == 0 {
		m.screen = screenResult
		m.result = resultModel{text: emptyText(m.opts.Verb), outcome: ErrNothingToPick}
		return m, nil
	}
	return m, nil
}

func (m Model) listKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		return m.quit(ErrCancelled)
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "enter":
		if !m.loaded || len(m.rows) == 0 {
			return m, nil
		}
		return m.pick(m.rows[m.cursor])
	}
	m.top = listWindow(m.top, m.cursor, m.listRows(), len(m.rows))
	return m, nil
}

// pick acts on the chosen row. done and unbind run at once on a DONE row and
// stop for a `y` on any other (#103, needsConfirm).
func (m Model) pick(r view.BindingStatus) (tea.Model, tea.Cmd) {
	switch m.opts.Verb {
	case VerbDone, VerbUnbind:
		if needsConfirm(m.opts.Verb, r) {
			m.screen = screenConfirm
			m.confirm = r
			return m, nil
		}
		return m.run(r.Name)
	}
	return m, nil
}

// run moves to a pending result screen and starts the verb on name. It is
// what Enter did before #103; the confirm screen's `y` reaches it now.
func (m Model) run(name string) (tea.Model, tea.Cmd) {
	m.screen = screenResult
	m.result = resultModel{pending: true}
	return m, runVerb(m.ctx, m.rt, m.opts, name)
}

// confirmKeys: only a lowercase y proceeds. Every other key -- Enter
// included, since a stray Enter is the whole reason this screen exists --
// returns to the list with the cursor where it was. ctrl+c is handled
// before this in Update and still cancels the picker.
func (m Model) confirmKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "y" {
		return m.run(m.confirm.Name)
	}
	m.screen = screenList
	return m, nil
}

func (m Model) resultKeys(_ tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.result.pending {
		return m, nil
	}
	return m.quit(m.result.outcome)
}

func (m Model) View() string {
	switch m.screen {
	case screenResult:
		return m.resultView()
	case screenConfirm:
		return m.confirmView()
	default:
		return m.listView()
	}
}
