package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
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

// overlay is a modal box the shell draws over the body's last rows (§3). It
// owns every key while it is set (rule 2.5, §5.2); update returns the next
// overlay, the command to run, and whether the overlay closed.
type overlay interface {
	update(tea.KeyMsg) (overlay, tea.Cmd, bool)
	view(width int) []string
}

type modalOverlay interface {
	modal(width int) (title string, rows []string, want int, danger bool)
}

// overlayKeyer is optionally implemented by an overlay that shows its own keys
// in the footer while it is up (§2.1).
type overlayKeyer interface{ keys() []KeyHelp }

// confirmBox is a one-line yes/no confirm: y runs onYes, any other key cancels
// (§3, §4.3).
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
	yStyle := kbdStyle
	if c.danger {
		yStyle = chipDangerStyle
	}
	buttons := chip(yStyle, "y") + " " + mutedStyle.Render(yes) +
		"      " + chip(kbdStyle, "n") + mutedStyle.Render(" cancel") +
		"      " + faintStyle.Render("any other key cancels")

	rows := []string{
		"",
		textStyle.Bold(true).Render(c.title),
		"",
	}
	for _, l := range c.lines {
		rows = append(rows, mutedStyle.Render(l))
	}
	rows = append(rows,
		"",
		buttons,
		"",
	)
	return kind, rows, 72, c.danger
}

func (c confirmBox) view(width int) []string {
	out := []string{fit(accentStyle.Render(c.title), width)}
	for _, l := range c.lines {
		out = append(out, fit(dimStyle.Render(l), width))
	}
	return out
}

// keys is the confirm's footer: y runs it, n cancels (§2.1).
func (c confirmBox) keys() []KeyHelp {
	yes := c.yes
	if yes == "" {
		yes = "yes"
	}
	return []KeyHelp{{"y", yes}, {"n", "cancel"}}
}

// promptBox is a single-line input, optionally with a choice list tab cycles
// through (§3). validate, when set, runs on enter: a non-nil error keeps the
// prompt open with the message under the input (§4.3). esc cancels.
//
// complete, when set, is the tab key's own source of choices: it is called
// with the value the first tab started from and returns what that value can
// become, so a path prompt lists and cycles the directory's entries (§4.5).
type promptBox struct {
	title   string
	kind    string // box title; "" means "input" (§3.6)
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
// fields (§2.4).
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
// choice list cycles as it always has (§4.5).
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
	out := []string{fit(accentStyle.Render(p.title), width), fit(p.input.View(), width)}
	if p.err != "" {
		out = append(out, fit(errorStyle.Render(p.err), width))
	}
	return out
}

// modal renders the prompt as a boxed modal (§3.6): blank, the title in text,
// blank, the input, the choices hint, the error, blank.
func (p promptBox) modal(width int) (string, []string, int, bool) {
	const want = 72
	kind := p.kind
	if kind == "" {
		kind = "input"
	}
	rows := []string{
		"",
		textStyle.Render(p.title),
		"",
		p.input.View(),
	}
	if len(p.choices) > 0 {
		rows = append(rows, faintStyle.Render("tab cycles: "+strings.Join(p.choices, " · ")))
	}
	if p.err != "" {
		rows = append(rows, errorStyle.Render(p.err))
	}
	rows = append(rows, "")
	return kind, rows, want, false
}

// keys is the prompt's footer: enter, tab when it can complete, esc (§3.6).
func (p promptBox) keys() []KeyHelp {
	keys := []KeyHelp{{"enter", "ok"}}
	if len(p.choices) > 0 || p.complete != nil {
		keys = append(keys, KeyHelp{"tab", "complete"})
	}
	keys = append(keys, KeyHelp{"esc", "cancel"})
	return keys
}

// mastermindConfirmLine names the mastermind a confirm affects, when that mastermind
// is neither the human at the cockpit nor empty (§4.3). wait is the phrase
// that follows the name.
func mastermindConfirmLine(b view.BindingStatus, wait string) string {
	if b.MasterMindName == "" || b.MasterMindName == "you" {
		return ""
	}
	return "MasterMind " + b.MasterMindName + " " + wait
}

// stopConfirmLines is x's confirm body: who else is affected, what is being
// stopped, and the keys. Pure, so it is tested directly (§4.3, §5).
func stopConfirmLines(b view.BindingStatus, now time.Time) []string {
	var lines []string
	if l := mastermindConfirmLine(b, "is waiting on this round"); l != "" {
		lines = append(lines, l)
	}
	lines = append(lines, joinFacts(actorCell(b)+" on "+candidateText(b), nowCell(b, now), spendCell(b)))
	return lines
}

// doneConfirmLines is D's confirm body (§4.3).
func doneConfirmLines(b view.BindingStatus) []string {
	var lines []string
	if l := mastermindConfirmLine(b, "owns this binding"); l != "" {
		lines = append(lines, l)
	}
	return lines
}

// unbindConfirmLines is u's confirm body (§4.3).
func unbindConfirmLines(b view.BindingStatus) []string {
	var lines []string
	if l := mastermindConfirmLine(b, "owns this binding"); l != "" {
		lines = append(lines, l)
	}
	return lines
}

// joinFacts joins the non-empty facts with the confirm's middot.
func joinFacts(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}

// stopCmd is the x key's confirm on b (§4.3, §5).
func stopCmd(env Env, b view.BindingStatus) tea.Cmd {
	key := b.Key()
	return openOverlay(confirmBox{
		kind:   "stop",
		yes:    "stop the round",
		danger: true,
		title:  fmt.Sprintf("Stop %s round %d?", key, b.Round),
		lines:  stopConfirmLines(b, env.Now),
		onYes: runAction(env.Ctx, "stop", key, func(ctx context.Context) Result {
			return env.Actions.Stop(ctx, key)
		}),
	})
}

// doneCmd is the D key's confirm on b (§4.3).
func doneCmd(env Env, b view.BindingStatus) tea.Cmd {
	key := b.Key()
	return openOverlay(confirmBox{
		kind:   "done",
		yes:    "mark done",
		danger: false,
		title:  fmt.Sprintf("Mark %s done?", key),
		lines:  doneConfirmLines(b),
		onYes: runAction(env.Ctx, "done", key, func(ctx context.Context) Result {
			return env.Actions.Done(ctx, key)
		}),
	})
}

// unbindCmd is the u key's confirm on b (§4.3).
func unbindCmd(env Env, b view.BindingStatus) tea.Cmd {
	key := b.Key()
	return openOverlay(confirmBox{
		kind:   "unbind",
		yes:    "unbind and archive",
		danger: true,
		title:  fmt.Sprintf("Unbind %s? The binding is archived; its branch %s is kept.", key, b.Branch),
		lines:  unbindConfirmLines(b),
		onYes: runAction(env.Ctx, "unbind", key, func(ctx context.Context) Result {
			return env.Actions.Unbind(ctx, key)
		}),
	})
}

// gateCmd is the g key's one form on b (§3.3): the duration and the reason,
// validated and submitted together. The duration must parse and be positive,
// or the form stays open on that field.
func gateCmd(env Env, b view.BindingStatus) tea.Cmd {
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

	header := accentStyle.Bold(true).Render("Gate "+candidateText(b)) +
		faintStyle.Render("  (provider "+secondSegment(subject)+")")

	return openOverlay(formBox{
		kind:   "gate",
		submit: "gate",
		header: []string{header},
		note:   []string{"The pick skips it until then. Builders already running are not stopped."},
		fields: []formField{forField, reasonField},
		onSubmit: func(values []string) tea.Cmd {
			forDur, _ := time.ParseDuration(strings.TrimSpace(values[0]))
			reason := values[1]
			return runAction(env.Ctx, "gate", key, func(ctx context.Context) Result {
				return env.Actions.Gate(ctx, subject, forDur, reason)
			})
		},
	})
}

// sendCmd is the s key: the plan picker, then the send confirm (§3.4). The
// picker keeps the prompt's pre-fill (plansDir(b)), tab completion and file
// validation; it adds a list of the directory's recent plans. root is the
// binding's tree, which relative values resolve against (§2.5).
func sendCmd(env Env, b view.BindingStatus) tea.Cmd {
	p := sendPicker{env: env, b: b, input: newPromptInput(), pick: -1, root: b.CWD}
	if d := plansDir(b); d != "" {
		p.input.SetValue(d)
		p.input.CursorEnd()
	}
	return openOverlay(p)
}

// plansDir is the send picker's pre-fill: the binding tree's docs/plans/
// directory, made relative to that tree ("docs/plans/"), when it exists, ""
// when it does not (§4.5, §2.5).
func plansDir(b view.BindingStatus) string {
	if b.CWD == "" {
		return ""
	}
	dir := filepath.Join(b.CWD, "docs", "plans")
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	return filepath.Join("docs", "plans") + string(filepath.Separator)
}

// pathCompletion is the plan prompt's tab: the entries of the directory the
// value names, filtered by its last segment, so tab lists a directory and
// cycles what is in it (§4.5). A value ending in the separator lists that
// directory's entries whole.
func pathCompletion(value string) []string {
	if value == "" {
		return nil
	}
	dir, prefix := value, ""
	if !strings.HasSuffix(value, string(filepath.Separator)) {
		dir, prefix = filepath.Dir(value), filepath.Base(value)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || (prefix != "" && !strings.HasPrefix(name, prefix)) {
			continue
		}
		full := filepath.Join(dir, name)
		if e.IsDir() {
			full += string(filepath.Separator)
		}
		out = append(out, full)
	}
	sort.Strings(out)
	return out
}

// sendConfirmTitle is the send confirm's question: the file, the binding, the
// round the send opens, and nothing else (§4.5). The round is
// BindingStatus.Round: the daemon advances b.Round when a round closes
// (internal/relevo/reconcile.go), so relevo.Send opens round b.Round, not
// b.Round+1 (W3). A binding with a round open is refused by Send itself, so
// the label stays and the refusal comes back as the action's error.
func sendConfirmTitle(b view.BindingStatus, file string) string {
	return fmt.Sprintf("Send %s to %s as round %d?", file, b.Key(), b.Round)
}

// sendConfirmLines names who else is affected, then the keys (§4.3, §4.5).
func sendConfirmLines(b view.BindingStatus) []string {
	var lines []string
	if l := mastermindConfirmLine(b, "is waiting on this round"); l != "" {
		lines = append(lines, l)
	}
	return lines
}

// editorCmd is the E key: draft a plan in the state directory's tui-plans/,
// open $EDITOR on it with the terminal released, and, when the human left
// something new and non-empty in it, ask the same send confirm s asks (§4.5).
func editorCmd(env Env, b view.BindingStatus) tea.Cmd {
	key := b.Key()
	dir, ok := tuiPlansDir(env)
	if !ok {
		return notice("no state directory to draft a plan in; write one and use s")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return notice(err.Error())
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.md", key, env.Now.Unix()))
	header := []byte(editorPlanHeader(key))
	if err := os.WriteFile(path, header, 0o644); err != nil {
		return notice(err.Error())
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	return tea.ExecProcess(exec.Command(editor, path), func(err error) tea.Msg {
		if err != nil {
			return noticeMsg{text: err.Error()}
		}
		after, rerr := os.ReadFile(path)
		if rerr != nil {
			return noticeMsg{text: rerr.Error()}
		}
		if !planSent(header, after) {
			return noticeMsg{text: "nothing sent"}
		}
		return openOverlayMsg{ov: confirmBox{
			kind:   "send",
			yes:    "send the plan",
			danger: false,
			title:  sendConfirmTitle(b, path),
			lines:  sendConfirmLines(b),
			onYes: runAction(env.Ctx, "send", key, func(ctx context.Context) Result {
				return env.Actions.Send(ctx, key, path)
			}),
		}}
	})
}

// tuiPlansDir is <state dir>/tui-plans, where the E key drafts a plan. The
// state dir is the store's root: the database sits inside it.
func tuiPlansDir(env Env) (string, bool) {
	st := env.Src.Base().Store
	if st == nil {
		return "", false
	}
	return filepath.Join(filepath.Dir(st.DBPath()), "tui-plans"), true
}

// editorPlanHeader is the one-line comment the draft file starts with, so a
// human sees what the file is for before writing in it (§4.5).
func editorPlanHeader(name string) string {
	return fmt.Sprintf("<!-- relevo: plan for %s -->\n", name)
}

// planSent reports whether an edited draft is worth sending: something
// non-empty was written and the file is no longer the header alone (§4.5).
func planSent(before, after []byte) bool {
	return len(bytes.TrimSpace(after)) > 0 && !bytes.Equal(before, after)
}

// bindCmd is the b key: name, candidate and feature, in that order, then the
// bind confirm (§4.5). It takes no binding: the key creates one.
func bindCmd(env Env) tea.Cmd {
	name := promptBox{kind: "bind", title: "name:", input: newPromptInput()}
	name.validate = func(value string) error {
		v := strings.TrimSpace(value)
		if v == "" {
			return errors.New("name is required")
		}
		return store.ValidName(v)
	}
	name.onEnter = func(value string) tea.Cmd {
		binding := strings.TrimSpace(value)
		cand := promptBox{
			kind:    "bind",
			title:   "candidate (tab: …; empty = policy pick):",
			input:   newPromptInput(),
			choices: env.Actions.Candidates("builder"),
		}
		cand.onEnter = func(c string) tea.Cmd {
			feature := promptBox{kind: "bind", title: "feature (optional):", input: newPromptInput()}
			feature.onEnter = func(f string) tea.Cmd {
				in := BindInput{Name: binding, Candidate: strings.TrimSpace(c), Feature: strings.TrimSpace(f)}
				return openOverlay(confirmBox{
					kind:   "bind",
					yes:    "bind",
					danger: false,
					title:  bindConfirmTitle(in),
					onYes: runAction(env.Ctx, "bind", binding, func(ctx context.Context) Result {
						return env.Actions.Bind(ctx, in)
					}),
				})
			}
			return openOverlay(feature)
		}
		return openOverlay(cand)
	}
	return openOverlay(name)
}

// bindConfirmTitle is the bind confirm's question: the name, the repo the
// worktree is cut from, and the candidate or the policy pick (§4.5).
func bindConfirmTitle(in BindInput) string {
	on := in.Candidate
	if on == "" {
		on = "the policy pick"
	}
	return fmt.Sprintf("Bind %s on a new worktree of %s as builder on %s?", in.Name, getwd(), on)
}

// retryCmd is the r key: a candidate list, then the confirm (§3.5). The list
// is the role's candidates in their order, including the current one; the
// current candidate and any gated one are disabled.
func retryCmd(env Env, b view.BindingStatus) tea.Cmd {
	key := b.Key()
	current := candidateText(b)

	var items []listItem
	for _, name := range env.Actions.Candidates(actorCell(b)) {
		switch {
		case name == current:
			note := "last used"
			if roundOpen(b) {
				note = fmt.Sprintf("running round %d now", b.Round)
			}
			items = append(items, listItem{
				name: name, status: "current", note: note,
				statusStyle: mutedStyle, disabled: true,
			})
		case gatedBy(env, name) != nil:
			g := gatedBy(env, name)
			status := "gated"
			if !g.Until.IsZero() {
				status = "gated " + ago(env.Now, g.Until)
			}
			items = append(items, listItem{
				name: name, status: status, note: gateProvider(g.Token),
				statusStyle: faintStyle, disabled: true,
			})
		default:
			items = append(items, listItem{name: name, status: "ready", statusStyle: greenStyle})
		}
	}

	sel := -1
	for i, item := range items {
		if !item.disabled {
			sel = i
			break
		}
	}

	header := accentStyle.Bold(true).Render("Retry "+key) +
		textStyle.Render(fmt.Sprintf(" round %d on another candidate", b.Round))

	var note []string
	if roundOpen(b) {
		note = append(note, fmt.Sprintf("Stops round %d, then sends its plan again on the chosen candidate.", b.Round))
	} else {
		note = append(note, "Sends the last plan again as a new round on the chosen candidate.")
	}
	note = append(note, "The binding stays on it for later rounds.")

	return openOverlay(listBox{
		kind:   "retry on…",
		submit: "retry",
		header: []string{header},
		note:   note,
		items:  items,
		sel:    sel,
		onPick: func(name string) tea.Cmd {
			return openOverlay(confirmBox{
				kind:   "retry",
				yes:    "stop and retry",
				danger: true,
				title:  retryConfirmTitle(b, name),
				lines:  retryConfirmLines(b, name),
				onYes: runAction(env.Ctx, "retry", key, func(ctx context.Context) Result {
					return env.Actions.Retry(ctx, key, name)
				}),
			})
		},
	})
}

// gatedBy is the live gate on candidate name, matched by the gate's short
// name (§3.5), or nil.
func gatedBy(env Env, name string) *availability.Gate {
	for i := range env.Report.Gated {
		if env.Report.Gated[i].Name == name {
			return &env.Report.Gated[i]
		}
	}
	return nil
}

// retryConfirmTitle is the retry confirm's question (§4.5): an open round is
// stopped first and the question names it; with nothing open the last plan is
// simply resent as a new round.
func retryConfirmTitle(b view.BindingStatus, candidate string) string {
	if roundOpen(b) {
		return fmt.Sprintf("Stop %s round %d and resend its plan on %s?", b.Key(), b.Round, candidate)
	}
	return fmt.Sprintf("Resend %s's last plan as a new round on %s?", b.Key(), candidate)
}

// retryConfirmLines names who else is affected, that the candidate persists as
// the binding's builder, then the keys (§4.5).
func retryConfirmLines(b view.BindingStatus, candidate string) []string {
	var lines []string
	if l := mastermindConfirmLine(b, "is waiting on this round"); l != "" {
		lines = append(lines, l)
	}
	lines = append(lines, "the binding keeps "+candidate+" for later rounds")
	return lines
}

// roundOpen reports whether the row has a round in flight, the fact that
// decides which retry confirm is shown (§4.5). ACTIVE is the one display word
// that means a round is open.
func roundOpen(b view.BindingStatus) bool { return b.Display == "ACTIVE" }

// shellCmd is the o key: a shell in the binding's tree, with the terminal
// released and restored. A failure to start one is a notice (§4.3, §6).
func shellCmd(env Env, key string) tea.Cmd {
	cmd, err := env.Actions.Shell(key)
	if err != nil {
		return notice(err.Error())
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return noticeMsg{text: err.Error()}
		}
		return nil
	})
}

// actionKey is the action keys' one dispatch, shared by the fleet and the
// round view (§4.3, §4.5): the command the key returns, and whether the key
// was an action key at all. Without Actions every action key does nothing; a
// second action on a binding that already carries one is refused with a
// notice. 'b' is deliberately not here: only the fleet binds (fleetActionKey).
func actionKey(env Env, b view.BindingStatus, k string) (tea.Cmd, bool) {
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
// with (§4.5).
func fleetActionKey(env Env, b view.BindingStatus, k string) (tea.Cmd, bool) {
	if k == "b" {
		if env.Actions == nil {
			return nil, false
		}
		return bindCmd(env), true
	}
	return actionKey(env, b, k)
}
