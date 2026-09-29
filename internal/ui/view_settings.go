package ui

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// numCPU is runtime.NumCPU, kept as a package var so a golden test can pin the
// machine's core count and the view's defaults stay deterministic.
var numCPU = runtime.NumCPU

// settingsDocMsg is one ConfigDoc load's reply for ':settings'.
type settingsDocMsg struct {
	doc relevo.ConfigDoc
	err error
}

// settingsView is ':settings': every policy setting with its default,
// in 6 groups, the cursor row's detail block, the group forms and reset.
type settingsView struct {
	doc     relevo.ConfigDoc
	err     error // the last ConfigDoc error; shown centred like candidates' error state
	loaded  bool
	cur     int // cursor over the 18 settings, never a group rule
	top     int // first body line shown (page follows the cursor)
	actions bool
}

// newSettingsView builds ':settings' and the command that loads its doc.
// env.Actions must be non-nil: execLine refuses the command
// otherwise.
func newSettingsView(env Env) (settingsView, tea.Cmd) {
	v := settingsView{actions: env.Actions != nil}
	return v, settingsDocCmd(env)
}

// settingsDocCmd reads the stored config off the update loop.
func settingsDocCmd(env Env) tea.Cmd {
	return func() tea.Msg {
		doc, err := env.Actions.ConfigDoc()
		return settingsDocMsg{doc: doc, err: err}
	}
}

// settingHelp is the detail block's sentence(s) per key. Plain prose,
// no captions.
var settingHelp = map[string]string{
	"max_switches": "How many times relevo may move one round to the next candidate after a rate limit or a " +
		"failed start before it asks you. 0 means it asks at once.",
	"max_tier":       "The highest permission tier any actor may run at without --allow-yolo.",
	"verify.default": "When on, a plain relevo send marks the round for a read-only reviewer when it closes.",
	"gate.default": "The command relevo runs in a binding's tree when a writer round reports done; a failure " +
		"opens a repair round while gate.regate allows one.",
	"gate.timeout":       "How long that command may run before it counts as failed.",
	"gate.regate":        "How many repair rounds relevo may open after a failed check before it asks you.",
	"limit_gate_default": "How long a rate limit closes a provider when its message names no reset time.",
	"stall_after":        "A running builder whose stream is quiet this long is shown as stalled.",
	"progress_interval":  "How often relevo samples a running round's tree and stream.",
	"explore_after": "A builder whose output moves while its tree does not, for this long, is shown as " +
		"exploring.",
	"stale_after":        "A binding left in needs-you or held this long is shown as stale.",
	"scope":              "The systemd scope relevo starts each round's builder in on this machine: CPU, memory and task limits.",
	"serve.max_builders": "How many builders relevo serve runs at once; more rounds wait in its queue.",
	"serve.scope":        "The scope for rounds relevo serve runs; when set it replaces scope for them.",
	"scan_patterns":      "Extra patterns that mark a builder's line as instruction-shaped, beside the built-in list.",
	"classify":           "An optional classifier that scores a builder's lines for prompt injection, beside the pattern scan.",
	"notify.webhooks":    "Where relevo posts round and binding events over HTTP.",
}

// checkSentence is the check form's note and the sentence that follows
// gate.default's in the detail block: who has check on, and what that
// means given gate.default's current value.
func checkSentence(doc relevo.ConfigDoc) string {
	var names []string
	for name, a := range doc.Actors {
		if actorCheckOn(a.Check) && agentShapeOf(doc, a.Agent) == string(agentsrc.ShapeWriter) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "no actor has check on, so nothing runs it"
	}
	list := strings.Join(names, ", ")
	if doc.Policy.GateDefault() == "" {
		if len(names) == 1 {
			return list + " has check on, so with none set its rounds run nothing."
		}
		return list + " have check on, so with none set their rounds run nothing."
	}
	if len(names) == 1 {
		return list + " runs it after each round."
	}
	return list + " run it after each round."
}

// wrapText greedily wraps s at word boundaries so no line exceeds width
// cells.
func wrapText(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		if lipgloss.Width(cur)+1+lipgloss.Width(w) <= width {
			cur += " " + w
			continue
		}
		lines = append(lines, cur)
		cur = w
	}
	return append(lines, cur)
}

// pluralSetting is "setting" for 1, "settings" otherwise.
func pluralSetting(n int) string {
	if n == 1 {
		return "setting"
	}
	return "settings"
}

// settingsLayout is the table's columns at width: VALUE and DEFAULT are
// 22 cells each, SETTING takes the rest of width-6 cells.
func settingsLayout(width int) (settingW, valueW, defaultW, cw int) {
	cw = width - 6
	if cw < 0 {
		cw = 0
	}
	valueW, defaultW = 22, 22
	settingW = cw - valueW - defaultW - 4
	if settingW < 1 {
		settingW = 1
	}
	return
}

// settingsHeaderLine is the table's header row, in faint bold.
func settingsHeaderLine(settingW, valueW, defaultW, cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{
		{text: pad("SETTING", settingW), style: style},
		{text: pad("VALUE", valueW), style: style},
		{text: pad("DEFAULT", defaultW), style: style},
	}
	return candLine(cells, false, cw)
}

// settingsGroupRuleLine is one group's rule: the group name (dim), a
// gridStyle fill, then the row and set counts (faint). Modelled on
// dash.Model.dayRuleLine's styles (Dim/Grid/Faint = mutedStyle/gridStyle/faintStyle);
// that method lives on dash.Model and is not reusable directly.
func settingsGroupRuleLine(group string, n, set, cw int) string {
	rightText := fmt.Sprintf(" %d %s", n, pluralSetting(n))
	if set > 0 {
		rightText += fmt.Sprintf(" · %d set", set)
	}
	leftWidth := lipgloss.Width(group) + 1
	rightWidth := lipgloss.Width(rightText)
	fill := cw - leftWidth - rightWidth
	if fill < 0 {
		fill = 0
	}
	content := mutedStyle.Render(group) + " " + gridStyle.Render(strings.Repeat("┈", fill)) + faintStyle.Render(rightText)
	return "   " + fit(content, cw) + "   "
}

// settingsDataLine is one setting's table row: a set row's key and
// value are in the text colour, an unset row's are dim, and DEFAULT is always
// faint. The cursor row's key stays in the text colour and bold, as on every
// other cursor row (candLine); only VALUE and DEFAULT keep an unset row's dim
// colour.
func settingsDataLine(s relevo.Setting, sel bool, settingW, valueW, defaultW, cw int) string {
	base := textStyle
	if !s.Set {
		base = faintStyle
	}
	nameStyle := base
	if sel {
		nameStyle = textStyle.Bold(true)
	}
	cells := []candCell{
		{text: pad(s.Key, settingW), style: nameStyle},
		{text: pad(s.Value, valueW), style: base},
		{text: pad(s.Default, defaultW), style: faintStyle},
	}
	return candLine(cells, sel, cw)
}

// settingsDetailLines is the cursor row's detail block: the key and its
// value, then the row's sentence(s), wrapped, dim. gate.default's sentence is
// followed by the check sentence.
func settingsDetailLines(doc relevo.ConfigDoc, row relevo.Setting, width int) []string {
	first := "   " + faintStyle.Bold(true).Render(row.Key) + "   " + mutedStyle.Render(row.Value)
	out := []string{fit(first, width)}

	sentence := settingHelp[row.Key]
	if row.Key == "gate.default" {
		sentence += " " + checkSentence(doc)
	}
	wrapW := width - 6
	for _, l := range wrapText(sentence, wrapW) {
		out = append(out, fit("   "+mutedStyle.Render(l), width))
	}
	return out
}

// settingsLineOf is the body line setting idx sits on: the blank line,
// the header, then one rule line per group boundary reached by idx.
func settingsLineOf(settings []relevo.Setting, idx int) int {
	line := 2
	group := ""
	for i := 0; i <= idx && i < len(settings); i++ {
		if settings[i].Group != group {
			group = settings[i].Group
			line++
		}
		if i == idx {
			return line
		}
		line++
	}
	return line
}

// bodyLines is the whole body before it is windowed: a blank line, the
// header, the grouped table and one blank line, then the cursor row's detail
// block.
func (v settingsView) bodyLines(width int) []string {
	settings := relevo.Settings(v.doc, numCPU())
	settingW, valueW, defaultW, cw := settingsLayout(width)
	cur := candClamp(v.cur, len(settings))

	lines := []string{""}
	lines = append(lines, settingsHeaderLine(settingW, valueW, defaultW, cw))

	i := 0
	for i < len(settings) {
		group := settings[i].Group
		j := i
		n, set := 0, 0
		for j < len(settings) && settings[j].Group == group {
			n++
			if settings[j].Set {
				set++
			}
			j++
		}
		lines = append(lines, settingsGroupRuleLine(group, n, set, cw))
		for k := i; k < j; k++ {
			lines = append(lines, settingsDataLine(settings[k], k == cur, settingW, valueW, defaultW, cw))
		}
		i = j
	}

	lines = append(lines, "")
	return append(lines, settingsDetailLines(v.doc, settings[cur], width)...)
}

// follow scrolls the page so the cursor's table row stays visible.
func (v *settingsView) follow(env Env) {
	settings := relevo.Settings(v.doc, numCPU())
	if len(settings) == 0 {
		v.top = 0
		return
	}
	avail := bodyHeight(env)
	if avail <= 0 {
		v.top = 0
		return
	}
	lines := v.bodyLines(env.Width)
	cur := candClamp(v.cur, len(settings))
	sel := settingsLineOf(settings, cur)
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+avail {
		v.top = sel - avail + 1
	}
	// When the table and the detail block together overflow avail, prefer
	// showing the detail block's tail over the table's leading rows, as long
	// as doing so does not scroll the cursor's row out of view.
	maxTop := max(0, len(lines)-avail)
	if maxTop > v.top && sel >= maxTop {
		v.top = maxTop
	}
	v.top = clamp(v.top, 0, maxTop)
}

func (v settingsView) Crumbs() []string { return []string{"settings"} }

// Capturing is always false: the view owns no text input of its own.
func (v settingsView) Capturing() bool { return false }

// Keys are the view's own keys, shown only when the shell has an
// Actions seam; the shell appends the global tail (`: command`, `? all keys`,
// `q quit`/`esc back`).
func (v settingsView) Keys() []KeyHelp {
	if !v.actions {
		return nil
	}
	return []KeyHelp{
		{"enter", "edit"},
		{"r", "reset"},
	}
}

// HelpKeys is the help overlay's key list: the same set plus ↑↓ move.
func (v settingsView) HelpKeys() []KeyHelp {
	if !v.actions {
		return nil
	}
	return []KeyHelp{
		{"↑↓", "move"},
		{"enter", "edit"},
		{"r", "reset"},
	}
}

// Context is the counts line on the left; the right side is empty this
// round (round 2 adds the revision).
func (v settingsView) Context(env Env) (string, string) {
	settings := relevo.Settings(v.doc, numCPU())
	set := 0
	for _, s := range settings {
		if s.Set {
			set++
		}
	}
	item := func(count int, label string) string {
		return textStyle.Bold(true).Render(fmt.Sprintf("%d", count)) + " " + mutedStyle.Render(label)
	}
	left := "   " + item(len(settings), "settings") + "   " + item(set, "set")
	return left, ""
}

func (v settingsView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if !v.loaded && v.err != nil {
		return strings.Join(statsCentered(v.err.Error(), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	lines := v.bodyLines(width)
	top := clamp(v.top, 0, max(0, len(lines)-height))
	if top > len(lines) {
		top = len(lines)
	}
	end := top + height
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(fitLines(lines[top:end], width, height), "\n")
}

func (v settingsView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case settingsDocMsg:
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.doc = msg.doc
			v.err = nil
			v.loaded = true
		}
		v.cur = candClamp(v.cur, len(relevo.Settings(v.doc, numCPU())))
		return v, nil

	case statusMsg:
		// A refresh from anywhere re-reads the config, which is what reloads
		// the table after ApplyConfig's Refresh.
		return v, settingsDocCmd(env)

	case tea.KeyMsg:
		if !v.actions {
			return v, nil
		}
		return v.updateKey(msg, env)
	}
	return v, nil
}

// updateKey is the view's own keys.
func (v settingsView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	settings := relevo.Settings(v.doc, numCPU())
	n := len(settings)
	switch k.String() {
	case "up":
		v.cur = candClamp(v.cur-1, n)
		v.follow(env)
	case "down":
		v.cur = candClamp(v.cur+1, n)
		v.follow(env)
	case "home":
		v.cur = 0
		v.follow(env)
	case "end":
		v.cur = candClamp(n-1, n)
		v.follow(env)
	case "pgup":
		v.cur = candClamp(v.cur-bodyHeight(env), n)
		v.follow(env)
	case "pgdown":
		v.cur = candClamp(v.cur+bodyHeight(env), n)
		v.follow(env)
	case "enter":
		if n == 0 {
			return v, nil
		}
		row := settings[candClamp(v.cur, n)]
		switch row.Form {
		case "rounds", "check", "timing", "max_builders", "scope", "serve.scope", "classify":
			return v, openOverlay(newSettingsForm(env, v.doc, row.Form, row.Key))
		case "webhooks":
			return v, push(newWebhooksView(env, v.doc))
		case "scan_patterns":
			return v, notice("scan_patterns is edited with relevo config set policy")
		}
		return v, nil
	case "r":
		if n == 0 {
			return v, nil
		}
		return v, v.resetCmd(env, settings[candClamp(v.cur, n)])
	}
	return v, nil
}

// resetCmd is the r key: a not-Set row is a notice. A Set row's reset runs
// at once, before any confirm opens, so a reset that cannot pass is reported
// right away rather than after a confirmed y. An ErrNoChange from
// ResetSetting becomes the same "already the default" notice; any other
// failure (a FieldError from a stale doc, or an actor's tier above the new
// max_tier) becomes a "can't reset" notice, and opens no confirm. Only a
// successful reset opens the confirm, whose y applies that precomputed edit
// through ApplyConfig.
func (v settingsView) resetCmd(env Env, row relevo.Setting) tea.Cmd {
	if !row.Set {
		return notice(row.Key + " is already the default")
	}
	edit, err := relevo.ResetSetting(v.doc, row.Key)
	if err != nil {
		if errors.Is(err, relevo.ErrNoChange) {
			return notice(row.Key + " is already the default")
		}
		return notice("can't reset " + row.Key + ": " + relevo.HumanPolicyError(err))
	}
	return openOverlay(confirmBox{
		kind:   "reset",
		yes:    "reset",
		danger: false,
		title:  "Reset " + accentStyle.Bold(true).Render(row.Key) + " to its default?",
		lines:  []string{row.Value + " → " + row.Default},
		onYes: runAction(env.Ctx, "reset", row.Key, func(ctx context.Context) Result {
			return env.Actions.ApplyConfig(ctx, edit)
		}),
	})
}
