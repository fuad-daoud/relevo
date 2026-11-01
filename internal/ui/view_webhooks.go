package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// webhooksDocMsg is one ConfigDoc load's reply for the pushed webhook list: a
// type of its own, so a load started here cannot be mistaken for a settings or
// actor reload.
type webhooksDocMsg struct {
	doc relevo.ConfigDoc
	err error
}

// webhooksView is `settings › notify.webhooks`: the stored webhooks.
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
func newWebhooksView(env widget.Env, doc relevo.ConfigDoc) (widget.View, tea.Cmd) {
	v := webhooksView{doc: doc, loaded: true, actions: env.Actions != nil}
	return v, webhooksDocCmd(env)
}

func webhooksDocCmd(env widget.Env) tea.Cmd {
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

func webhooksCount(n int) string {
	if n == 1 {
		return "webhook"
	}
	return "webhooks"
}

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

func webhooksHeaderLine(urlW, cw int) string {
	style := widget.FaintStyle.Bold(true)
	cells := []candCell{
		{text: widget.Pad("URL", urlW), style: style},
		{text: widget.Pad("FORMAT", 8), style: style},
		{text: widget.Pad("EVENTS", 34), style: style},
	}
	return candLine(cells, false, cw)
}

func webhooksDataLine(h policy.Webhook, sel bool, urlW, cw int) string {
	urlStyle := widget.TextStyle
	if sel {
		urlStyle = urlStyle.Bold(true)
	}
	cells := []candCell{
		{text: widget.Pad(h.URL, urlW), style: urlStyle},
		{text: widget.Pad(webhookFormatText(h.Format), 8), style: widget.MutedStyle},
		{text: widget.Pad(webhookEventsText(h.Events), 34), style: widget.MutedStyle},
	}
	return candLine(cells, sel, cw)
}

func webhooksDetailLines(h policy.Webhook, width int) []string {
	first := "   " + widget.FaintStyle.Bold(true).Render(h.URL)
	second := "   " + widget.MutedStyle.Render("format "+webhookFormatText(h.Format)+
		"   events "+webhookEventsText(h.Events))
	return []string{widget.Fit(first, width), widget.Fit(second, width)}
}

func (v webhooksView) bodyLines(width int) []string {
	hooks := webhooksOf(v.doc)
	urlW, cw := webhooksLayout(width)

	lines := []string{"", webhooksHeaderLine(urlW, cw)}
	if len(hooks) == 0 {
		return append(lines, "   "+widget.MutedStyle.Render("none yet"))
	}
	cur := candClamp(v.cur, len(hooks))
	for i, h := range hooks {
		lines = append(lines, webhooksDataLine(h, i == cur, urlW, cw))
	}
	lines = append(lines, "")
	return append(lines, webhooksDetailLines(hooks[cur], width)...)
}

func (v *webhooksView) follow(env widget.Env) {
	avail := widget.BodyHeight(env)
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
	v.top = widget.Clamp(v.top, 0, max(0, len(lines)-avail))
}

func (v webhooksView) Crumbs() []string { return []string{"notify.webhooks"} }

func (v webhooksView) Capturing() bool { return false }

// Keys are the list's action keys, shown only when the shell has an widget.Actions
// seam; `esc back` and the rest of the global tail are the shell's own.
func (v webhooksView) Keys() []widget.KeyHelp {
	if !v.actions {
		return nil
	}
	return []widget.KeyHelp{
		{Key: "a", Help: "add"},
		{Key: "e", Help: "edit"},
		{Key: "d", Help: "delete"},
	}
}

// HelpKeys is the help overlay's key list: `↑↓ move` is left out of the footer
// so it lives here.
func (v webhooksView) HelpKeys() []widget.KeyHelp {
	return append([]widget.KeyHelp{{Key: "↑↓", Help: "move"}}, v.Keys()...)
}

// Context is the counts line on the left; the list has no right side.
func (v webhooksView) Context(env widget.Env) (string, string) {
	n := len(webhooksOf(v.doc))
	left := "   " + widget.TextStyle.Bold(true).Render(fmt.Sprintf("%d", n)) + " " + widget.MutedStyle.Render(webhooksCount(n))
	return left, ""
}

func (v webhooksView) Body(env widget.Env, width, height int) string {
	var lines []string
	if v.loaded && width > 0 && height > 0 {
		lines = v.bodyLines(width)
	}
	return pagedBody(v.loaded, v.err, lines, v.top, width, height)
}

func (v webhooksView) Update(msg tea.Msg, env widget.Env) (widget.View, tea.Cmd) {
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
// write config do nothing without the shell's widget.Actions seam.
func (v webhooksView) updateKey(k tea.KeyMsg, env widget.Env) (widget.View, tea.Cmd) {
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
		v.cur = candClamp(v.cur-widget.BodyHeight(env), n)
		v.follow(env)
	case "pgdown":
		v.cur = candClamp(v.cur+widget.BodyHeight(env), n)
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
func (v webhooksView) deleteCmd(env widget.Env, i int) tea.Cmd {
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
		onYes: runAction(env.Ctx, "delete webhook", h.URL, func(ctx context.Context) widget.Result {
			return env.Actions.ApplyConfig(ctx, edit)
		}),
	})
}
