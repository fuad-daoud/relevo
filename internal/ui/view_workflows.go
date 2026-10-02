package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// workflowsDocMsg carries the workflow list off the update loop.
type workflowsDocMsg struct {
	list []relevo.WorkflowSummary
	err  error
}

// workflowsView is ':workflows': the shipped workflow and every saved one, with
// where each came from, what it says about itself and what it takes.
type workflowsView struct {
	list    []relevo.WorkflowSummary
	err     error
	loaded  bool
	cur     int
	top     int
	actions bool
}

// newWorkflowsView builds ':workflows' and the command that loads its list.
// env.Actions must be non-nil: execLine refuses the command otherwise.
func newWorkflowsView(env Env) (View, tea.Cmd) {
	v := workflowsView{actions: env.Actions != nil}
	return v, workflowsDocCmd(env)
}

// workflowsDocCmd reads the list off the update loop.
func workflowsDocCmd(env Env) tea.Cmd {
	return func() tea.Msg {
		if env.Actions == nil {
			return workflowsDocMsg{}
		}
		list, err := env.Actions.Workflows()
		return workflowsDocMsg{list: list, err: err}
	}
}

// workflowCol is one fixed-width table column after NAME.
type workflowCol struct {
	key  string
	head string
	w    int
}

// workflowsLayout is the columns to show at width and the NAME column's room:
// the table spans width-6 cells from column 3, each column followed by two
// spaces, NAME taking the rest. While that would fall under 14 cells, columns
// leave in the order PARAMS, then DESCRIPTION.
func workflowsLayout(width int) (int, []workflowCol) {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	visible := []workflowCol{
		{"origin", "ORIGIN", 7},
		{"desc", "DESCRIPTION", 34},
		{"inputs", "INPUTS", 20},
		{"params", "PARAMS", 26},
	}
	nameW := func(cols []workflowCol) int {
		fixed := 2 // the two spaces after NAME
		for i, c := range cols {
			fixed += c.w
			if i < len(cols)-1 {
				fixed += 2
			}
		}
		return cw - fixed
	}
	for _, drop := range []string{"params", "desc"} {
		if nameW(visible) >= 14 {
			break
		}
		for i, c := range visible {
			if c.key == drop {
				visible = append(append([]workflowCol(nil), visible[:i]...), visible[i+1:]...)
				break
			}
		}
	}
	return nameW(visible), visible
}

// workflowsHeaderLine is the list's header row, in faint bold.
func workflowsHeaderLine(nameW int, cols []workflowCol, cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{{text: pad("NAME", nameW), style: style}}
	for _, c := range cols {
		cells = append(cells, candCell{text: pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

// workflowsInputsText is a workflow's two chain inputs as one cell: each input
// the workflow takes, with the mode it declares it in. A workflow that takes
// neither says so.
func workflowsInputsText(in workflow.Inputs) string {
	var parts []string
	for _, f := range []struct {
		name string
		mode workflow.InputMode
	}{{"plans", in.Plans}, {"task", in.Task}} {
		switch f.mode {
		case workflow.InputRequired:
			parts = append(parts, f.name+" required")
		case workflow.InputOptional:
			parts = append(parts, f.name+" optional")
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " · ")
}

// workflowsParamsText is a workflow's params as one cell: every param by name
// with the value it ships with, in the order the list carries them. A workflow
// that declares none says so.
func workflowsParamsText(params []relevo.WorkflowParam) string {
	if len(params) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(params))
	for _, p := range params {
		parts = append(parts, p.Name+"="+p.Value)
	}
	return strings.Join(parts, ", ")
}

// workflowOriginText is where a workflow came from, in the colour that says so:
// the shipped one accented, because it is the one the remove key refuses.
func workflowOriginText(origin string) (string, lipgloss.Style) {
	if origin == relevo.WorkflowOriginShipped {
		return origin, accentStyle
	}
	return origin, mutedStyle
}

// workflowsDataLine is one workflow's row. The cursor row's name is bold and
// the shipped row's origin accented, so the row the d key refuses stands out.
func workflowsDataLine(w relevo.WorkflowSummary, sel bool, nameW int, cols []workflowCol, cw int) string {
	nameStyle := textStyle
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	cells := []candCell{{text: pad(w.Name, nameW), style: nameStyle}}
	for _, col := range cols {
		text := ""
		style := mutedStyle
		switch col.key {
		case "origin":
			text, style = workflowOriginText(w.Origin)
		case "desc":
			text = w.Description
			if text == "" {
				text, style = "—", faintStyle
			}
		case "inputs":
			text = workflowsInputsText(w.Inputs)
		case "params":
			text = workflowsParamsText(w.Params)
		}
		cells = append(cells, candCell{text: pad(text, col.w), style: style})
	}
	return candLine(cells, sel, cw)
}

// workflowDetailLines is the cursor row's detail block: the name in faint bold
// and the origin beside it, then the inputs and the params in full, since the
// table truncates them.
func workflowDetailLines(w relevo.WorkflowSummary, width int) []string {
	origin, originStyle := workflowOriginText(w.Origin)
	first := "   " + faintStyle.Bold(true).Render(w.Name) + "   " + originStyle.Render(origin)
	second := "   " + mutedStyle.Render("inputs ") + textStyle.Render(workflowsInputsText(w.Inputs))
	third := "   " + mutedStyle.Render("params ") + textStyle.Render(workflowsParamsText(w.Params))
	return []string{fit(first, width), fit(second, width), fit(third, width)}
}

// bodyLines is the whole list body before it is windowed: a blank line under
// the context row, the table, two blank lines and the cursor row's detail block.
func (v workflowsView) bodyLines(env Env, width int) []string {
	nameW, cols := workflowsLayout(width)
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	lines := []string{""}
	lines = append(lines, workflowsHeaderLine(nameW, cols, cw))
	cur := candClamp(v.cur, len(v.list))
	for i, w := range v.list {
		lines = append(lines, workflowsDataLine(w, i == cur, nameW, cols, cw))
	}
	if len(v.list) == 0 {
		return lines
	}
	lines = append(lines, "", "")
	return append(lines, workflowDetailLines(v.list[cur], width)...)
}

// follow scrolls the page so the cursor's row stays visible.
func (v *workflowsView) follow(env Env) {
	if len(v.list) == 0 {
		v.top = 0
		return
	}
	avail := bodyHeight(env)
	if avail <= 0 {
		v.top = 0
		return
	}
	lines := v.bodyLines(env, env.Width)
	sel := 2 + candClamp(v.cur, len(v.list)) // the blank line, the header, then the rows
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+avail {
		v.top = sel - avail + 1
	}
	v.top = clamp(v.top, 0, max(0, len(lines)-avail))
}

func (v workflowsView) Crumbs() []string { return []string{"workflows"} }

// Capturing is always false: the view owns no text input of its own. The add
// form is an overlay.
func (v workflowsView) Capturing() bool { return false }

// Keys are the list's keys, shown only when the shell has an Actions seam.
func (v workflowsView) Keys() []KeyHelp {
	if !v.actions {
		return nil
	}
	return []KeyHelp{
		{"↑↓", "move"},
		{"enter", "view"},
		{"a", "add"},
		{"d", "remove"},
		{"e", "edit"},
	}
}

// HelpKeys is the help overlay's key list: the same set.
func (v workflowsView) HelpKeys() []KeyHelp { return v.Keys() }

// Context is the counts line on the left; the list has no right side.
func (v workflowsView) Context(env Env) (string, string) {
	saved, shipped := 0, 0
	for _, w := range v.list {
		if w.Origin == relevo.WorkflowOriginShipped {
			shipped++
			continue
		}
		saved++
	}
	// "saved" and "shipped" are already plural, so only the total takes an s.
	count := func(n int, label string) string {
		return textStyle.Bold(true).Render(fmt.Sprintf("%d", n)) + " " + mutedStyle.Render(label)
	}
	total := "workflow"
	if len(v.list) != 1 {
		total += "s"
	}
	left := "   " + count(len(v.list), total)
	left += "   " + count(saved, "saved")
	left += "   " + count(shipped, "shipped")
	return left, ""
}

func (v workflowsView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if !v.loaded && v.err != nil {
		return strings.Join(statsCentered(v.err.Error(), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	if len(v.list) == 0 {
		return strings.Join(statsCentered("no workflows; a adds one", width, height), "\n")
	}
	lines := v.bodyLines(env, width)
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

func (v workflowsView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case workflowsDocMsg:
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.list = msg.list
			v.err = nil
			v.loaded = true
		}
		v.cur = candClamp(v.cur, len(v.list))
		return v, nil

	case statusMsg:
		// A refresh from anywhere re-reads the list, which is what reloads the
		// table after a write's Refresh.
		return v, workflowsDocCmd(env)

	case workflowEditAfterMsg:
		return v, v.editAfter(env, msg)

	case tea.KeyMsg:
		if !v.actions {
			return v, nil
		}
		return v.updateKey(msg, env)
	}
	return v, nil
}

// updateKey is the list's own keys.
func (v workflowsView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	n := len(v.list)
	switch k.String() {
	case "up", "k":
		v.cur = candClamp(v.cur-1, n)
		v.follow(env)
	case "down", "j":
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
		return v, v.openCmd(env)
	case "a":
		return v, openOverlay(newAddWorkflowForm(env))
	case "d":
		if n == 0 {
			return v, nil
		}
		return v, v.removeCmd(env, candClamp(v.cur, n))
	case "e":
		if n == 0 {
			return v, nil
		}
		return v, v.editCmd(env, v.list[candClamp(v.cur, n)])
	}
	return v, nil
}

// openCmd pushes the cursor workflow's graph, with the two reads that fill it.
func (v workflowsView) openCmd(env Env) tea.Cmd {
	name := v.list[candClamp(v.cur, len(v.list))].Name
	return tea.Batch(push(newWorkflowView(name), nil), workflowSourceCmd(env, name), workflowGraphCmd(env, name))
}

// removeCmd is the d key. A shipped workflow is not saved, so there is nothing
// here to remove: that is a notice, and no confirm opens.
func (v workflowsView) removeCmd(env Env, i int) tea.Cmd {
	w := v.list[i]
	if w.Origin == relevo.WorkflowOriginShipped {
		return notice("shipped workflows cannot be removed")
	}
	return openOverlay(confirmBox{
		kind:   "remove workflow",
		yes:    "remove",
		danger: true,
		title:  "Remove workflow " + accentStyle.Bold(true).Render(w.Name) + "?",
		lines: []string{
			pad("source", 11) + "goes with it",
			pad("others", 11) + "are kept",
		},
		onYes: runAction(env.Ctx, "remove workflow", workflowWriteKey("remove", w.Name), func(ctx context.Context) Result {
			return env.Actions.WorkflowRemove(ctx, w.Name)
		}),
	})
}
