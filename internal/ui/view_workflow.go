package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// workflowSourceMsg is one workflow's source text, read off the update loop.
// shipped says the text is the shipped workflow's definition rendered as JSON
// rather than source a user wrote, so the view labels it.
type workflowSourceMsg struct {
	name    string
	text    string
	shipped bool
	err     error
}

// workflowGraphMsg is one workflow's step graph, read off the update loop.
type workflowGraphMsg struct {
	name string
	rows []relevo.GraphRow
	err  error
}

// workflowView is `workflows › <name>`: the step graph, with its source one key
// away under s. A shipped workflow has no source to show, so s shows its
// definition as JSON instead and the header says which of the two is on screen.
type workflowView struct {
	name        string
	rows        []relevo.GraphRow
	graphErr    error
	graphLoaded bool

	source       string
	shipped      bool
	sourceErr    error
	sourceLoaded bool

	src bool // the source text is showing rather than the graph
	cur int
	top int
	vp  viewport.Model
}

// newWorkflowView pushes one workflow's graph, before either read has landed.
func newWorkflowView(name string) View {
	return workflowView{name: name}
}

// workflowSourceCmd reads one workflow's source text off the update loop.
func workflowSourceCmd(env Env, name string) tea.Cmd {
	return func() tea.Msg {
		if env.Actions == nil {
			return workflowSourceMsg{name: name}
		}
		text, shipped, err := env.Actions.WorkflowSource(name)
		return workflowSourceMsg{name: name, text: text, shipped: shipped, err: err}
	}
}

// workflowGraphCmd reads one workflow's step graph off the update loop.
func workflowGraphCmd(env Env, name string) tea.Cmd {
	return func() tea.Msg {
		if env.Actions == nil {
			return workflowGraphMsg{name: name}
		}
		rows, err := env.Actions.WorkflowGraph(name)
		return workflowGraphMsg{name: name, rows: rows, err: err}
	}
}

func (v workflowView) Crumbs() []string { return []string{v.name} }

func (v workflowView) Capturing() bool { return false }

// Keys are the view's keys: the graph's own movement and the source toggle.
// On the source the same movement scrolls it, so the two differ only in what
// they say. esc back is the shell's own and is not repeated here.
func (v workflowView) Keys() []KeyHelp {
	if v.src {
		return []KeyHelp{{"↑↓", "scroll"}, {"s", "graph"}}
	}
	return []KeyHelp{{"↑↓", "move"}, {"s", "source"}}
}

// HelpKeys is the help overlay's key list: the same set.
func (v workflowView) HelpKeys() []KeyHelp { return v.Keys() }

// Context is the view's summary line: what the workflow is and how many steps
// it declares, or which of the two texts s is showing.
func (v workflowView) Context(env Env) (string, string) {
	left := "   " + textStyle.Bold(true).Render(v.name)
	if v.src {
		what := "source"
		if v.shipped {
			what = "definition (json)"
		}
		return left + mutedStyle.Render("   "+what), ""
	}
	return left + mutedStyle.Render("   "+strconv.Itoa(len(v.rows))+" steps"), ""
}

// workflowGraphLayout is the graph table's columns: STEP takes the width the
// fixed columns leave, then KIND, ACTOR and EDGES. A terminal too narrow for
// the edges drops them first, then the actor.
func workflowGraphLayout(width int) (int, []workflowCol) {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	fixed := func(cols []workflowCol) int {
		n := 0
		for i, c := range cols {
			n += c.w
			if i < len(cols)-1 {
				n += 2
			}
		}
		return n
	}
	cols := []workflowCol{
		{"kind", "KIND", 8},
		{"actor", "ACTOR", 16},
		{"edges", "EDGES", 34},
	}
	for len(cols) > 0 {
		minimum := 16
		if cols[len(cols)-1].key == "edges" {
			minimum = 24
		}
		if cw-fixed(cols) >= minimum {
			break
		}
		cols = cols[:len(cols)-1]
	}
	return cw - fixed(cols), cols
}

// workflowGraphHeaderLine is the graph table's header row, in faint bold.
func workflowGraphHeaderLine(stepW int, cols []workflowCol, cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{{text: pad("STEP", stepW), style: style}}
	for _, c := range cols {
		cells = append(cells, candCell{text: pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

// workflowStepLine is one step's graph row: the step in the text colour, its
// kind and the actors it names muted, and its edges in their own colour so the
// paths read apart from the node names. A step that declares no edge shows a
// dash, which is a step that ends its path.
func workflowStepLine(row relevo.GraphRow, sel bool, stepW int, cols []workflowCol, cw int) string {
	nameStyle := textStyle
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	cells := []candCell{{text: pad(row.ID, stepW), style: nameStyle}}
	for _, col := range cols {
		text := ""
		style := mutedStyle
		switch col.key {
		case "kind":
			text = row.Kind
			if text == "" {
				text, style = "—", faintStyle
			}
		case "actor":
			text = row.Actors
			if text == "" {
				text, style = "—", faintStyle
			}
		case "edges":
			text = row.EdgeText
			if text == "" {
				text, style = "—", faintStyle
			} else {
				style = greenStyle
			}
		}
		cells = append(cells, candCell{text: pad(text, col.w), style: style})
	}
	return candLine(cells, sel, cw)
}

// graphBodyLines is the whole graph body before the viewport windows it.
func (v workflowView) graphBodyLines(width int) []string {
	stepW, cols := workflowGraphLayout(width)
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	lines := []string{""}
	lines = append(lines, workflowGraphHeaderLine(stepW, cols, cw))
	cur := candClamp(v.cur, len(v.rows))
	for i, row := range v.rows {
		lines = append(lines, workflowStepLine(row, i == cur, stepW, cols, cw))
	}
	return lines
}

// follow scrolls the graph so the cursor's step stays visible.
func (v *workflowView) follow(env Env) {
	if len(v.rows) == 0 {
		v.top = 0
		return
	}
	avail := bodyHeight(env)
	if avail <= 0 {
		v.top = 0
		return
	}
	sel := 2 + candClamp(v.cur, len(v.rows))
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+avail {
		v.top = sel - avail + 1
	}
	v.top = clamp(v.top, 0, max(0, len(v.rows)+2-avail))
}

func (v workflowView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if v.src {
		return v.sourceBody(env, width, height)
	}
	if v.graphErr != nil && !v.graphLoaded {
		return strings.Join(statsCentered(v.graphErr.Error(), width, height), "\n")
	}
	if !v.graphLoaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	if len(v.rows) == 0 {
		return strings.Join(statsCentered("no steps: this workflow has an empty graph", width, height), "\n")
	}
	lines := v.graphBodyLines(width)
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

// sourceBody is the scrolled source text, sized to the shell's body box.
func (v workflowView) sourceBody(env Env, width, height int) string {
	if v.sourceErr != nil && !v.sourceLoaded {
		return strings.Join(statsCentered(v.sourceErr.Error(), width, height), "\n")
	}
	if !v.sourceLoaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	if strings.TrimSpace(v.source) == "" {
		return strings.Join(statsCentered("no source: a shipped workflow shows its definition under s", width, height), "\n")
	}
	vp := v.vp
	vp.Width = maxInt(width-6, 20)
	vp.Height = height
	body := fitLines(strings.Split(vp.View(), "\n"), width-6, height)
	for i, l := range body {
		body[i] = "   " + l
	}
	return strings.Join(body, "\n")
}

func (v workflowView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case workflowSourceMsg:
		if msg.name != v.name {
			return v, nil
		}
		if msg.err != nil {
			v.sourceErr = msg.err
			return v, nil
		}
		v.sourceErr = nil
		v.sourceLoaded = true
		v.shipped = msg.shipped
		v.source = sanitizeText(msg.text)
		if v.vp.Width == 0 {
			v.vp = viewport.New(maxInt(env.Width-6, 20), maxInt(bodyHeight(env), 1))
		}
		v.vp.SetContent(wrapBody(v.source, v.vp.Width))
		return v, nil

	case workflowGraphMsg:
		if msg.name != v.name {
			return v, nil
		}
		if msg.err != nil {
			v.graphErr = msg.err
			return v, nil
		}
		v.graphErr = nil
		v.graphLoaded = true
		v.rows = msg.rows
		v.cur = candClamp(v.cur, len(v.rows))
		return v, nil

	case tea.WindowSizeMsg:
		if v.vp.Width == 0 {
			v.vp = viewport.New(maxInt(env.Width-6, 20), maxInt(bodyHeight(env), 1))
		} else {
			v.vp.Width = maxInt(env.Width-6, 20)
			v.vp.Height = maxInt(bodyHeight(env), 1)
			v.vp.SetContent(wrapBody(v.source, v.vp.Width))
		}
		return v, nil

	case tea.KeyMsg:
		return v.updateKey(msg, env)
	}
	return v, nil
}

// updateKey is the view's own keys. s toggles between the graph and the source;
// on the source every other key scrolls it, so the same key set works in both.
func (v workflowView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	if k.String() == "s" && !v.src {
		// The source was not read yet when the graph opened, so the first s
		// asks for it rather than showing an empty pane.
		if !v.sourceLoaded && v.sourceErr == nil {
			return v, workflowSourceCmd(env, v.name)
		}
		v.src = true
		return v, nil
	}
	if v.src {
		if k.String() == "s" {
			v.src = false
			return v, nil
		}
		if k.String() == "esc" {
			return v, pop()
		}
		var cmd tea.Cmd
		v.vp, cmd = v.vp.Update(k)
		return v, cmd
	}

	n := len(v.rows)
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
	case "esc":
		return v, pop()
	}
	return v, nil
}
