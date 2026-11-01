package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

type agentsMsg struct {
	doc   relevo.ConfigDoc
	files map[string][]harness.AgentFile
	err   error
}

type agentsView struct {
	doc     relevo.ConfigDoc
	files   map[string][]harness.AgentFile
	err     error
	loaded  bool
	cur     int
	top     int
	actions bool
}

// newAgentsView builds ':agents' and the command that loads its doc and file
// states; env.Actions must be non-nil, as execLine refuses the command otherwise.
func newAgentsView(env widget.Env) (widget.View, tea.Cmd) {
	v := agentsView{actions: env.Actions != nil}
	return v, agentsCmd(env)
}

// File reads are cheap, so they happen here, not in Body.
func agentsCmd(env widget.Env) tea.Cmd {
	return func() tea.Msg {
		doc, err := env.Actions.ConfigDoc()
		if err != nil {
			return agentsMsg{err: err}
		}
		files := make(map[string][]harness.AgentFile)
		for _, name := range agentNames(doc) {
			fs, err := env.Actions.AgentFiles(name)
			if err != nil {
				return agentsMsg{err: err}
			}
			files[name] = fs
		}
		return agentsMsg{doc: doc, files: files}
	}
}

func (v agentsView) bodyLines(env widget.Env, width int) []string {
	rows := v.rows()
	nameW, cols := agentListLayout(width, agentKinds(v.files))
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	lines := []string{""}
	lines = append(lines, agentHeaderLine(nameW, cols, cw))
	cur := candClamp(v.cur, len(rows))
	for i, r := range rows {
		lines = append(lines, agentDataLine(r, i == cur, nameW, cols, cw))
	}
	if len(rows) == 0 {
		return lines
	}
	lines = append(lines, "", "")
	return append(lines, agentDetailLines(v.doc, rows[cur], width)...)
}

func (v agentsView) rows() []agentRow { return agentRows(v.doc, v.files) }

// followCursor scrolls top so the cursor's table row stays visible. The
// cursor's line counts the blank line and the header row above the rows.
func followCursor(top, cur, lineCount, avail int) int {
	sel := 2 + cur
	if sel < top {
		top = sel
	}
	if sel >= top+avail {
		top = sel - avail + 1
	}
	return widget.Clamp(top, 0, max(0, lineCount-avail))
}

func (v *agentsView) follow(env widget.Env) {
	rows := v.rows()
	if len(rows) == 0 {
		v.top = 0
		return
	}
	avail := widget.BodyHeight(env)
	if avail <= 0 {
		v.top = 0
		return
	}
	v.top = followCursor(v.top, v.cur, len(v.bodyLines(env, env.Width)), avail)
}

func (v agentsView) Crumbs() []string { return []string{"agents"} }

func (v agentsView) Capturing() bool { return false }

// `d delete` is listed even on a shipped row: the footer has no dimmed key, so
// a shipped row answers a press with a notice instead.
func (v agentsView) Keys() []widget.KeyHelp {
	if !v.actions {
		return nil
	}
	return []widget.KeyHelp{
		{Key: "↑↓", Help: "move"},
		{Key: "enter", Help: "open"},
		{Key: "d", Help: "delete"},
	}
}

func (v agentsView) HelpKeys() []widget.KeyHelp { return v.Keys() }

func (v agentsView) Context(env widget.Env) (string, string) {
	rows := v.rows()
	writers, readers, custom := 0, 0, 0
	for _, r := range rows {
		if r.shape == string(agentsrc.ShapeWriter) {
			writers++
		} else {
			readers++
		}
		if r.source == "custom" {
			custom++
		}
	}
	word := func(n int, label string) string {
		if n != 1 {
			label += "s"
		}
		return widget.TextStyle.Bold(true).Render(fmt.Sprintf("%d", n)) + " " + widget.MutedStyle.Render(label)
	}
	left := "   " + word(len(rows), "agent")
	left += "   " + word(writers, "writer")
	left += "   " + word(readers, "reader")
	if custom > 0 {
		left += "   " + widget.TextStyle.Bold(true).Render(fmt.Sprintf("%d", custom)) + " " + widget.MutedStyle.Render("custom")
	}
	return left, ""
}

func (v agentsView) Body(env widget.Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if !v.loaded && v.err != nil {
		return strings.Join(widget.Centered(v.err.Error(), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(widget.BlockLines([]string{"loading…"}, width, height), "\n")
	}
	if len(v.rows()) == 0 {
		return strings.Join(widget.Centered("no agents", width, height), "\n")
	}
	lines := v.bodyLines(env, width)
	top := widget.Clamp(v.top, 0, max(0, len(lines)-height))
	if top > len(lines) {
		top = len(lines)
	}
	end := top + height
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(widget.FitLines(lines[top:end], width, height), "\n")
}

func (v agentsView) Update(msg tea.Msg, env widget.Env) (widget.View, tea.Cmd) {
	switch msg := msg.(type) {
	case agentsMsg:
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.doc = msg.doc
			v.files = msg.files
			v.err = nil
			v.loaded = true
		}
		v.cur = candClamp(v.cur, len(v.rows()))
		return v, nil

	case statusMsg:
		// A refresh from anywhere re-reads the doc and the file states, which is
		// what reloads the table after a delete or a reset.
		return v, agentsCmd(env)

	case tea.KeyMsg:
		if !v.actions {
			return v, nil
		}
		return v.updateKey(msg, env)
	}
	return v, nil
}

func (v agentsView) updateKey(k tea.KeyMsg, env widget.Env) (widget.View, tea.Cmd) {
	rows := v.rows()
	n := len(rows)
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
	case "enter":
		if n == 0 {
			return v, nil
		}
		return v, openAgent(env, v.doc, rows[v.cur])
	case "d":
		if n == 0 {
			return v, nil
		}
		return v, v.deleteCmd(env, rows[v.cur])
	}
	return v, nil
}

func openAgent(env widget.Env, doc relevo.ConfigDoc, r agentRow) tea.Cmd {
	v, init := newAgentView(env, doc, r)
	return push(v, init)
}

func (v agentsView) deleteCmd(env widget.Env, r agentRow) tea.Cmd {
	if r.source == "shipped" {
		return notice(r.name + " ships with relevo; it can't be deleted")
	}
	edit, err := relevo.DeleteAgent(v.doc, r.name)
	if err != nil {
		return notice(err.Error())
	}
	name := r.name
	return openOverlay(confirmBox{
		kind:   "delete",
		yes:    "delete",
		danger: true,
		title:  "Delete agent " + widget.AccentStyle.Bold(true).Render(name) + "?",
		lines: []string{
			widget.Pad("used by", 11) + "no actor",
			widget.Pad("removes", 11) + "the agent from relevo's config",
		},
		onYes: runAction(env.Ctx, "delete agent", name, func(ctx context.Context) widget.Result {
			return env.Actions.ApplyConfig(ctx, edit)
		}),
	})
}
