package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

type candDocMsg struct {
	doc relevo.ConfigDoc
	err error
}

type candidatesView struct {
	doc     relevo.ConfigDoc
	err     error
	loaded  bool
	cur     int
	top     int
	actions bool
}

// newCandidatesView builds ':candidates' and the command that loads its doc;
// env.Actions must be non-nil, as execLine refuses the command otherwise.
func newCandidatesView(env widget.Env) (widget.View, tea.Cmd) {
	v := candidatesView{actions: env.Actions != nil}
	return v, candDocCmd(env)
}

func candDocCmd(env widget.Env) tea.Cmd {
	return func() tea.Msg {
		doc, err := env.Actions.ConfigDoc()
		return candDocMsg{doc: doc, err: err}
	}
}

func (v candidatesView) bodyLines(env widget.Env, width int) []string {
	rows := v.rows(env)
	unused := env.Report.Unused
	nameW, cols := candLayout(width)
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	lines := []string{""}
	lines = append(lines, candHeaderLine(nameW, cols, cw))
	cur := candClamp(v.cur, len(rows)+len(unused))
	for i, r := range rows {
		lines = append(lines, candDataLine(r, i == cur, nameW, cols, cw, env.Now))
	}
	if len(unused) > 0 {
		reasonW, setByW := unusedCols(width, nameW)
		lines = append(lines, "", unusedHeaderLine(nameW, reasonW, setByW, cw))
		for i, g := range unused {
			lines = append(lines, unusedDataLine(g, len(rows)+i == cur, nameW, reasonW, setByW, cw, env.Now))
		}
	}
	lines = append(lines, "", "")
	if cur < len(rows) {
		return append(lines, candDetailLines(v.doc, env, rows[cur], width)...)
	}
	return append(lines, unusedDetailLines(unused[cur-len(rows)], env.Now, width)...)
}

func (v candidatesView) rows(env widget.Env) []candRow { return candRows(v.doc, env.Report.Gated) }

func candClamp(i, n int) int {
	if n <= 0 || i < 0 {
		return 0
	}
	if i > n-1 {
		return n - 1
	}
	return i
}

func (v *candidatesView) follow(env widget.Env) {
	rows := v.rows(env)
	n := len(rows)
	if n == 0 && len(env.Report.Unused) == 0 {
		v.top = 0
		return
	}
	avail := widget.BodyHeight(env)
	if avail <= 0 {
		v.top = 0
		return
	}
	lines := v.bodyLines(env, env.Width)
	sel := 2 + v.cur // the blank line, the header row, then the rows
	if v.cur >= n {
		// Past the candidates come the unused table's blank line and header
		// before its first row.
		sel = 2 + n + 2 + (v.cur - n)
	}
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+avail {
		v.top = sel - avail + 1
	}
	v.top = widget.Clamp(v.top, 0, max(0, len(lines)-avail))
}

func (v candidatesView) Crumbs() []string { return []string{"candidates"} }

func (v candidatesView) Capturing() bool { return false }

// `↑↓ move` is left out: the footer has no room for it at 132 columns
// once `p probe` is included, so it lives in HelpKeys alone.
func (v candidatesView) Keys() []widget.KeyHelp {
	if !v.actions {
		return nil
	}
	return []widget.KeyHelp{
		{Key: "enter", Help: "edit"},
		{Key: "a", Help: "add"},
		{Key: "d", Help: "delete"},
		{Key: "g", Help: "gate"},
		{Key: "u", Help: "ungate"},
		{Key: "p", Help: "probe"},
	}
}

func (v candidatesView) HelpKeys() []widget.KeyHelp {
	if !v.actions {
		return nil
	}
	return []widget.KeyHelp{
		{Key: "↑↓", Help: "move"},
		{Key: "enter", Help: "edit"},
		{Key: "a", Help: "add"},
		{Key: "d", Help: "delete"},
		{Key: "g", Help: "gate"},
		{Key: "u", Help: "ungate"},
		{Key: "p", Help: "probe"},
	}
}

func (v candidatesView) OffKeys(env widget.Env) []string {
	if !v.actions {
		return nil
	}
	if len(env.Report.Unused) == 0 || v.cur < len(v.rows(env)) {
		return nil
	}
	return []string{"enter", "d", "g", "p"}
}

func (v candidatesView) Context(env widget.Env) (string, string) {
	rows := v.rows(env)
	total, ready, gated, off := 0, 0, 0, 0
	for _, r := range rows {
		total++
		switch {
		case !r.on:
			off++
		case r.gate != nil:
			gated++
		default:
			ready++
		}
	}
	item := func(count int, label string) string {
		return widget.TextStyle.Bold(true).Render(fmt.Sprintf("%d", count)) + " " + widget.MutedStyle.Render(label)
	}
	left := "   " + item(total, "candidates")
	left += "   " + item(ready, "ready")
	left += "   " + item(gated, "gated")
	if off > 0 {
		left += "   " + item(off, "off")
	}
	if m := len(env.Report.Unused); m > 0 {
		label := "gate on an unused provider"
		if m > 1 {
			label = "gates on unused providers"
		}
		left += "   " + item(m, label)
	}
	right := widget.FaintStyle.Render("sort ") + widget.MutedStyle.Render("pick order")
	return left, right
}

func (v candidatesView) Body(env widget.Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if !v.loaded && v.err != nil {
		return strings.Join(widget.Centered(v.err.Error(), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(widget.BlockLines([]string{"loading…"}, width, height), "\n")
	}
	rows := v.rows(env)
	if len(rows) == 0 {
		return strings.Join(widget.Centered("no candidates; a adds one", width, height), "\n")
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

func (v candidatesView) Update(msg tea.Msg, env widget.Env) (widget.View, tea.Cmd) {
	switch msg := msg.(type) {
	case candDocMsg:
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.doc = msg.doc
			v.err = nil
			v.loaded = true
		}
		v.cur = candClamp(v.cur, len(v.rows(env))+len(env.Report.Unused))
		return v, nil

	case statusMsg:
		// A refresh from anywhere re-reads the config, which is what
		// reloads the table after ApplyConfig's Refresh.
		return v, candDocCmd(env)

	case tea.KeyMsg:
		if !v.actions {
			return v, nil
		}
		return v.updateKey(msg, env)
	}
	return v, nil
}

func (v candidatesView) updateKey(k tea.KeyMsg, env widget.Env) (widget.View, tea.Cmd) {
	rows := v.rows(env)
	n := len(rows)
	total := n + len(env.Report.Unused)
	onUnused := v.cur >= n
	switch k.String() {
	case "up":
		v.cur = candClamp(v.cur-1, total)
		v.follow(env)
	case "down":
		v.cur = candClamp(v.cur+1, total)
		v.follow(env)
	case "home":
		v.cur = 0
		v.follow(env)
	case "end":
		v.cur = candClamp(total-1, total)
		v.follow(env)
	case "pgup":
		v.cur = candClamp(v.cur-widget.BodyHeight(env), total)
		v.follow(env)
	case "pgdown":
		v.cur = candClamp(v.cur+widget.BodyHeight(env), total)
		v.follow(env)
	case "enter":
		if n == 0 || onUnused {
			return v, nil
		}
		return v, openOverlay(newCandidateForm(env, v.doc, rows[v.cur].c.Name, servesText(rows[v.cur]), ""))
	case "a":
		if onUnused {
			return v, openOverlay(newCandidateForm(env, v.doc, "", "", ""))
		}
		if n == 0 {
			return v, nil
		}
		return v, openOverlay(newCandidateForm(env, v.doc, "", "", rows[v.cur].c.Harness))
	case "d":
		if n == 0 || onUnused {
			return v, nil
		}
		return v, v.deleteCmd(env, rows[v.cur])
	case "g":
		if n == 0 || onUnused {
			return v, nil
		}
		return v, v.gateForm(env, rows[v.cur])
	case "u":
		if n == 0 {
			return v, nil
		}
		if onUnused {
			provider := env.Report.Unused[v.cur-n].Provider
			return v, runAction(env.Ctx, "ungate", provider, func(ctx context.Context) widget.Result {
				return env.Actions.Ungate(ctx, provider)
			})
		}
		name := rows[v.cur].c.Name
		return v, runAction(env.Ctx, "ungate", name, func(ctx context.Context) widget.Result {
			return env.Actions.Ungate(ctx, name)
		})
	case "p":
		if n == 0 || onUnused {
			return v, nil
		}
		name := rows[v.cur].c.Name
		return v, runAction(env.Ctx, "probe", name, func(ctx context.Context) widget.Result {
			return env.Actions.Probe(ctx, name)
		})
	}
	return v, nil
}

func (v candidatesView) deleteCmd(env widget.Env, r candRow) tea.Cmd {
	edit, err := relevo.DeleteCandidate(v.doc, r.c.Name)
	if err != nil {
		return notice(err.Error())
	}
	name := r.c.Name
	return openOverlay(confirmBox{
		kind:   "delete",
		yes:    "delete",
		danger: true,
		title:  "Delete " + widget.AccentStyle.Bold(true).Render(name) + "?",
		lines:  candDeleteLines(v.doc, env, r),
		onYes: runAction(env.Ctx, "delete", name, func(ctx context.Context) widget.Result {
			return env.Actions.ApplyConfig(ctx, edit)
		}),
	})
}

func (v candidatesView) gateForm(env widget.Env, r candRow) tea.Cmd {
	name, provider := r.c.Name, r.c.Provider

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

	header := widget.AccentStyle.Bold(true).Render("Gate "+name) +
		widget.FaintStyle.Render("  (provider "+provider+")")

	return openOverlay(formBox{
		kind:   "gate",
		submit: "gate",
		header: []string{header},
		note:   []string{"The pick skips it until then. Builders already running are not stopped."},
		fields: []formField{forField, reasonField},
		onSubmit: func(values []string) tea.Cmd {
			forDur, _ := time.ParseDuration(strings.TrimSpace(values[0]))
			reason := values[1]
			return runAction(env.Ctx, "gate", name, func(ctx context.Context) widget.Result {
				return env.Actions.Gate(ctx, name, forDur, reason)
			})
		},
	})
}
