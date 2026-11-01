package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/view"
)

// getwd is os.Getwd, to the repo path the bind confirm names, "" when the
// working directory cannot be read. It is replaceable so a test can pin the
// repo without changing the process's directory.
var getwd = func() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

// overlay is a modal box the shell draws over the body's last rows. It
// owns every key while it is set; update returns the next overlay, the
// command to run, and whether the overlay closed.
type overlay interface {
	update(tea.KeyMsg) (overlay, tea.Cmd, bool)
	view(width int) []string
}

type modalOverlay interface {
	modal(width int) (title string, rows []string, want int, danger bool)
}

type overlayKeyer interface{ keys() []widget.KeyHelp }

// confirmBox is a one-line yes/no confirm: y runs onYes, any other key cancels.
type confirmBox struct {
	kind   string
	yes    string
	danger bool
	title  string
	lines  []string
	onYes  tea.Cmd
}

func (c confirmBox) update(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
	switch k.String() {
	case "y", "Y":
		return c, c.onYes, true
	default:
		return c, nil, true
	}
}

func (c confirmBox) modal(width int) (string, []string, int, bool) {
	kind := c.kind
	if kind == "" {
		kind = "confirm"
	}
	yes := c.yes
	if yes == "" {
		yes = "yes"
	}
	yStyle := widget.KbdStyle
	if c.danger {
		yStyle = widget.ChipDangerStyle
	}
	buttons := widget.Chip(yStyle, "y") + " " + widget.MutedStyle.Render(yes) +
		"      " + widget.Chip(widget.KbdStyle, "n") + widget.MutedStyle.Render(" cancel") +
		"      " + widget.FaintStyle.Render("any other key cancels")

	rows := []string{
		"",
		widget.TextStyle.Bold(true).Render(c.title),
		"",
	}
	for _, l := range c.lines {
		rows = append(rows, widget.MutedStyle.Render(l))
	}
	rows = append(rows,
		"",
		buttons,
		"",
	)
	return kind, rows, 72, c.danger
}

func (c confirmBox) view(width int) []string {
	out := []string{widget.Fit(widget.AccentStyle.Render(c.title), width)}
	for _, l := range c.lines {
		out = append(out, widget.Fit(widget.MutedStyle.Render(l), width))
	}
	return out
}

func (c confirmBox) keys() []widget.KeyHelp {
	yes := c.yes
	if yes == "" {
		yes = "yes"
	}
	return []widget.KeyHelp{{Key: "y", Help: yes}, {Key: "n", Help: "cancel"}}
}

// promptBox is a single-line input, optionally with a choice list tab cycles
// through. validate, when set, runs on enter: a non-nil error keeps the
// prompt open with the message under the input. esc cancels.
//
// complete, when set, is the tab key's own source of choices: it is called
// with the value the first tab started from and returns what that value can
// become, so a path prompt lists and cycles the directory's entries.
type promptBox struct {
	title   string
	kind    string // box title; "" means "input"
	input   textinput.Model
	choices []string
	onEnter func(value string) tea.Cmd

	complete func(value string) []string
	base     string // the value the first tab completed from

	validate func(value string) error
	err      string
	sel      int
	tabbed   bool
}

// newPromptInput builds the prompt's input, focused, with no prompt string of
// its own: the box's title is the question, and the modals already label their
// fields.
func newPromptInput() textinput.Model {
	in := newTextInput()
	in.Prompt = ""
	in.Focus()
	return in
}

func (p promptBox) update(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
	switch k.String() {
	case "esc":
		return p, nil, true
	case "tab", "shift+tab", "back_tab":
		return p.completeTab(), nil, false
	case "enter":
		value := p.input.Value()
		if p.validate != nil {
			if err := p.validate(value); err != nil {
				p.err = err.Error()
				return p, nil, false
			}
		}
		p.err = ""
		if p.onEnter == nil {
			return p, nil, true
		}
		return p, p.onEnter(value), true
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(k)
	// A new value restarts the completion: the next tab lists against what
	// the human typed, not what a previous tab inserted.
	p.base, p.tabbed = "", false
	return p, cmd, false
}

// completeTab is tab inside a prompt: a completion function lists what the
// value the first tab started from can become and cycles the matches; a fixed
// choice list cycles.
func (p promptBox) completeTab() promptBox {
	if p.complete != nil {
		if !p.tabbed {
			p.base = p.input.Value()
			p.sel = 0
		}
		matches := p.complete(p.base)
		if len(matches) == 0 {
			return p
		}
		p.tabbed = true
		p.sel %= len(matches)
		p.input.SetValue(matches[p.sel])
		p.input.CursorEnd()
		p.sel = (p.sel + 1) % len(matches)
		return p
	}
	if len(p.choices) > 0 {
		p.sel = (p.sel + 1) % len(p.choices)
		p.input.SetValue(p.choices[p.sel])
		p.input.CursorEnd()
	}
	return p
}

func (p promptBox) view(width int) []string {
	out := []string{widget.Fit(widget.AccentStyle.Render(p.title), width), widget.Fit(p.input.View(), width)}
	if p.err != "" {
		out = append(out, widget.Fit(widget.ErrorStyle.Render(p.err), width))
	}
	return out
}

func (p promptBox) modal(width int) (string, []string, int, bool) {
	const want = 72
	kind := p.kind
	if kind == "" {
		kind = "input"
	}
	rows := []string{
		"",
		widget.TextStyle.Render(p.title),
		"",
		p.input.View(),
	}
	if len(p.choices) > 0 {
		rows = append(rows, widget.FaintStyle.Render("tab cycles: "+strings.Join(p.choices, " · ")))
	}
	if p.err != "" {
		rows = append(rows, widget.ErrorStyle.Render(p.err))
	}
	rows = append(rows, "")
	return kind, rows, want, false
}

func (p promptBox) keys() []widget.KeyHelp {
	keys := []widget.KeyHelp{{Key: "enter", Help: "ok"}}
	if len(p.choices) > 0 || p.complete != nil {
		keys = append(keys, widget.KeyHelp{Key: "tab", Help: "complete"})
	}
	keys = append(keys, widget.KeyHelp{Key: "esc", Help: "cancel"})
	return keys
}

// mastermindConfirmLine names the mastermind a confirm affects, when that mastermind
// is neither the human at the cockpit nor empty. wait is the phrase
// that follows the name.
func mastermindConfirmLine(b view.BindingStatus, wait string) string {
	if b.MasterMindName == "" || b.MasterMindName == "you" {
		return ""
	}
	return "MasterMind " + b.MasterMindName + " " + wait
}

// stopConfirmLines is x's confirm body. Pure, so it is tested directly.
func stopConfirmLines(b view.BindingStatus, now time.Time) []string {
	var lines []string
	if l := mastermindConfirmLine(b, "is waiting on this round"); l != "" {
		lines = append(lines, l)
	}
	lines = append(lines, joinFacts(actorCell(b)+" on "+candidateText(b), nowCell(b, now), spendCell(b)))
	return lines
}

func ownershipConfirmLines(b view.BindingStatus) []string {
	var lines []string
	if l := mastermindConfirmLine(b, "owns this binding"); l != "" {
		lines = append(lines, l)
	}
	return lines
}

func joinFacts(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}

func stopCmd(env widget.Env, b view.BindingStatus) tea.Cmd {
	key := b.Key()
	return openOverlay(confirmBox{
		kind:   "stop",
		yes:    "stop the round",
		danger: true,
		title:  fmt.Sprintf("Stop %s round %d?", key, b.Round),
		lines:  stopConfirmLines(b, env.Now),
		onYes: runAction(env.Ctx, "stop", key, func(ctx context.Context) widget.Result {
			return env.Actions.Stop(ctx, key)
		}),
	})
}

func doneCmd(env widget.Env, b view.BindingStatus) tea.Cmd {
	key := b.Key()
	return openOverlay(confirmBox{
		kind:   "done",
		yes:    "mark done",
		danger: false,
		title:  fmt.Sprintf("Mark %s done?", key),
		lines:  ownershipConfirmLines(b),
		onYes: runAction(env.Ctx, "done", key, func(ctx context.Context) widget.Result {
			return env.Actions.Done(ctx, key)
		}),
	})
}

func unbindCmd(env widget.Env, b view.BindingStatus) tea.Cmd {
	key := b.Key()
	return openOverlay(confirmBox{
		kind:   "unbind",
		yes:    "unbind and archive",
		danger: true,
		title:  fmt.Sprintf("Unbind %s? The binding is archived; its branch %s is kept.", key, b.Branch),
		lines:  ownershipConfirmLines(b),
		onYes: runAction(env.Ctx, "unbind", key, func(ctx context.Context) widget.Result {
			return env.Actions.Unbind(ctx, key)
		}),
	})
}

// gateCmd is the g key's one form on b; the duration must parse and be
// positive, or the form stays open on that field.
func gateCmd(env widget.Env, b view.BindingStatus) tea.Cmd {
	key := b.Key()
	subject := b.BuilderCandidate

	forField := formField{
		label: "for",
		input: newFormInput(true),
		hint:  "e.g. 30m, 2h, 1d · empty = until cleared",
	}
	forField.validate = func(value string) error {
		s := strings.TrimSpace(value)
		if s == "" {
			return nil
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		if d <= 0 {
			return errors.New("duration must be positive")
		}
		return nil
	}
	reasonField := formField{label: "reason", input: newFormInput(false), hint: "optional"}

	header := widget.AccentStyle.Bold(true).Render("Gate "+candidateText(b)) +
		widget.FaintStyle.Render("  (provider "+secondSegment(subject)+")")

	return openOverlay(formBox{
		kind:   "gate",
		submit: "gate",
		header: []string{header},
		note:   []string{"The pick skips it until then. Builders already running are not stopped."},
		fields: []formField{forField, reasonField},
		onSubmit: func(values []string) tea.Cmd {
			forDur, _ := time.ParseDuration(strings.TrimSpace(values[0]))
			reason := values[1]
			return runAction(env.Ctx, "gate", key, func(ctx context.Context) widget.Result {
				return env.Actions.Gate(ctx, subject, forDur, reason)
			})
		},
	})
}

// shellCmd is the o key: a shell in the binding's tree. A failure to start
// one is a notice.
func shellCmd(env widget.Env, key string) tea.Cmd {
	cmd, err := env.Actions.Shell(key)
	if err != nil {
		return notice(err.Error())
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return widget.NoticeMsg{Text: err.Error()}
		}
		return nil
	})
}

// actionKey is the action keys' one dispatch, shared by the fleet and the
// round view: the command the key returns, and whether the key
// was an action key at all. Without widget.Actions every action key does nothing; a
// second action on a binding that already carries one is refused with a
// notice. 'b' is deliberately not here: only the fleet binds (fleetActionKey).
func actionKey(env widget.Env, b view.BindingStatus, k string) (tea.Cmd, bool) {
	if env.Actions == nil {
		return nil, false
	}
	switch k {
	case "x", "D", "u", "g", "o", "s", "E", "r":
	default:
		return nil, false
	}
	if verb, ok := env.Running[b.Key()]; ok {
		return notice(fmt.Sprintf("%s: %s still running", b.Key(), verb)), true
	}
	switch k {
	case "x":
		return stopCmd(env, b), true
	case "D":
		return doneCmd(env, b), true
	case "u":
		return unbindCmd(env, b), true
	case "g":
		return gateCmd(env, b), true
	case "s":
		return sendCmd(env, b), true
	case "E":
		return editorCmd(env, b), true
	case "r":
		return retryCmd(env, b), true
	}
	return shellCmd(env, b.Key()), true
}

// fleetActionKey is actionKey plus the fleet's own 'b': a bind names no
// binding yet, so it has no row to act on and no in-flight action to collide
// with.
func fleetActionKey(env widget.Env, b view.BindingStatus, k string) (tea.Cmd, bool) {
	if k == "b" {
		if env.Actions == nil {
			return nil, false
		}
		return bindCmd(env), true
	}
	return actionKey(env, b, k)
}
