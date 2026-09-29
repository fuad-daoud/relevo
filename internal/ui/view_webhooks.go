package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// webhooksDocMsg is one ConfigDoc load's reply for the pushed webhook list: a
// type of its own, so a load started here cannot be mistaken for a settings or
// actor reload.
type webhooksDocMsg struct {
	doc relevo.ConfigDoc
	err error
}

// webhooksView is `settings › notify.webhooks`: the stored webhooks, the cursor
// row's detail block, and the add, edit and delete keys behind them.
type webhooksView struct {
	doc     relevo.ConfigDoc
	err     error // the last ConfigDoc error; shown centred like candidates' error state
	loaded  bool
	cur     int // cursor over the stored webhooks
	top     int // first body line shown (page follows the cursor)
	actions bool
}

// newWebhooksView pushes the webhook list over the doc the settings view holds,
// so the first frame draws before the reload's reply lands.
func newWebhooksView(env Env, doc relevo.ConfigDoc) (View, tea.Cmd) {
	v := webhooksView{doc: doc, loaded: true, actions: env.Actions != nil}
	return v, webhooksDocCmd(env)
}

// webhooksDocCmd reads the stored config off the update loop.
func webhooksDocCmd(env Env) tea.Cmd {
	return func() tea.Msg {
		doc, err := env.Actions.ConfigDoc()
		return webhooksDocMsg{doc: doc, err: err}
	}
}

// webhooksOf is doc's stored webhook list; a doc with no notify block has none.
func webhooksOf(doc relevo.ConfigDoc) []policy.Webhook {
	if doc.Policy.Notify == nil {
		return nil
	}
	return doc.Policy.Notify.Webhooks
}

// webhookFormatText is a hook's format as display text: the stored "" is json's
// default.
func webhookFormatText(format string) string {
	if format == "" {
		return "json"
	}
	return format
}

// webhookEventsText is a hook's events as display text: every event when the
// filter is empty.
func webhookEventsText(events []string) string {
	if len(events) == 0 {
		return "every event"
	}
	return strings.Join(events, ", ")
}

// webhooksCount is the context line's label: "webhook" for one.
func webhooksCount(n int) string {
	if n == 1 {
		return "webhook"
	}
	return "webhooks"
}

// webhooksLayout is the table's fixed columns after URL: FORMAT 8 cells and
// EVENTS 34, URL taking the rest of width-6 cells.
func webhooksLayout(width int) (urlW, cw int) {
	cw = width - 6
	if cw < 0 {
		cw = 0
	}
	urlW = cw - 2 - 8 - 2 - 34
	if urlW < 1 {
		urlW = 1
	}
	return urlW, cw
}

// webhooksHeaderLine is the table's header row, in faint bold.
func webhooksHeaderLine(urlW, cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{
		{text: pad("URL", urlW), style: style},
		{text: pad("FORMAT", 8), style: style},
		{text: pad("EVENTS", 34), style: style},
	}
	return candLine(cells, false, cw)
}

// webhooksDataLine is one webhook's row: the URL in the text colour, its format
// and events muted, and the cursor row's URL bold on the selection band.
func webhooksDataLine(h policy.Webhook, sel bool, urlW, cw int) string {
	urlStyle := textStyle
	if sel {
		urlStyle = urlStyle.Bold(true)
	}
	cells := []candCell{
		{text: pad(h.URL, urlW), style: urlStyle},
		{text: pad(webhookFormatText(h.Format), 8), style: mutedStyle},
		{text: pad(webhookEventsText(h.Events), 34), style: mutedStyle},
	}
	return candLine(cells, sel, cw)
}

// webhooksDetailLines is the cursor row's detail block: the URL in faint bold,
// then its format and events, dim.
func webhooksDetailLines(h policy.Webhook, width int) []string {
	first := "   " + faintStyle.Bold(true).Render(h.URL)
	second := "   " + mutedStyle.Render("format "+webhookFormatText(h.Format)+
		"   events "+webhookEventsText(h.Events))
	return []string{fit(first, width), fit(second, width)}
}

// bodyLines is the whole body before it is windowed: a blank line, the header,
// the rows (or one dim line when there are none), then the cursor row's detail
// block under one blank.
func (v webhooksView) bodyLines(width int) []string {
	hooks := webhooksOf(v.doc)
	urlW, cw := webhooksLayout(width)

	lines := []string{"", webhooksHeaderLine(urlW, cw)}
	if len(hooks) == 0 {
		return append(lines, "   "+mutedStyle.Render("none yet"))
	}
	cur := candClamp(v.cur, len(hooks))
	for i, h := range hooks {
		lines = append(lines, webhooksDataLine(h, i == cur, urlW, cw))
	}
	lines = append(lines, "")
	return append(lines, webhooksDetailLines(hooks[cur], width)...)
}

// follow scrolls the page so the cursor's row stays visible.
func (v *webhooksView) follow(env Env) {
	avail := bodyHeight(env)
	if avail <= 0 {
		v.top = 0
		return
	}
	lines := v.bodyLines(env.Width)
	sel := 2 + v.cur // the blank line, the header row, then the rows
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+avail {
		v.top = sel - avail + 1
	}
	v.top = clamp(v.top, 0, max(0, len(lines)-avail))
}

func (v webhooksView) Crumbs() []string { return []string{"notify.webhooks"} }

// Capturing is always false: the view owns no text input of its own.
func (v webhooksView) Capturing() bool { return false }

// Keys are the list's action keys, shown only when the shell has an Actions
// seam; `esc back` and the rest of the global tail are the shell's own.
func (v webhooksView) Keys() []KeyHelp {
	if !v.actions {
		return nil
	}
	return []KeyHelp{
		{"a", "add"},
		{"e", "edit"},
		{"d", "delete"},
	}
}

// HelpKeys is the help overlay's key list: `↑↓ move` is left out of the footer
// so it lives here.
func (v webhooksView) HelpKeys() []KeyHelp {
	return append([]KeyHelp{{"↑↓", "move"}}, v.Keys()...)
}

// Context is the counts line on the left; the list has no right side.
func (v webhooksView) Context(env Env) (string, string) {
	n := len(webhooksOf(v.doc))
	left := "   " + textStyle.Bold(true).Render(fmt.Sprintf("%d", n)) + " " + mutedStyle.Render(webhooksCount(n))
	return left, ""
}

func (v webhooksView) Body(env Env, width, height int) string {
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

func (v webhooksView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case webhooksDocMsg:
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.doc = msg.doc
			v.err = nil
			v.loaded = true
		}
		v.cur = candClamp(v.cur, len(webhooksOf(v.doc)))
		return v, nil

	case statusMsg:
		// A refresh from anywhere re-reads the config, which is what reloads
		// the list after ApplyConfig's Refresh.
		return v, webhooksDocCmd(env)

	case tea.KeyMsg:
		return v.updateKey(msg, env)
	}
	return v, nil
}

// updateKey is the list's own keys. Movement and esc always work; the keys that
// write config do nothing without the shell's Actions seam.
func (v webhooksView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	hooks := webhooksOf(v.doc)
	n := len(hooks)
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
	case "esc":
		return v, pop()
	case "a":
		if !v.actions {
			return v, nil
		}
		return v, openOverlay(newWebhookForm(env, v.doc, -1))
	case "e", "enter":
		if !v.actions || n == 0 {
			return v, nil
		}
		return v, openOverlay(newWebhookForm(env, v.doc, candClamp(v.cur, n)))
	case "d":
		if !v.actions || n == 0 {
			return v, nil
		}
		return v, v.deleteCmd(env, candClamp(v.cur, n))
	}
	return v, nil
}

// deleteCmd is the d key: validate the shorter list, then confirm the loss as a
// danger box. A refused edit is a notice, and no confirm opens.
func (v webhooksView) deleteCmd(env Env, i int) tea.Cmd {
	hooks := webhooksOf(v.doc)
	if i < 0 || i >= len(hooks) {
		return nil
	}
	next := make([]policy.Webhook, 0, len(hooks)-1)
	next = append(next, hooks[:i]...)
	next = append(next, hooks[i+1:]...)
	h := hooks[i]

	edit, err := relevo.SetWebhooks(v.doc, next, "delete webhook "+h.URL)
	if err != nil {
		return notice(relevo.HumanPolicyError(err))
	}
	return openOverlay(confirmBox{
		kind:   "delete",
		yes:    "delete",
		danger: true,
		title:  "delete",
		lines:  []string{"Delete this webhook?", h.URL},
		onYes: runAction(env.Ctx, "delete webhook", h.URL, func(ctx context.Context) Result {
			return env.Actions.ApplyConfig(ctx, edit)
		}),
	})
}
