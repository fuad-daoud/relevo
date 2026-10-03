package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/view"
)

// chainsDocMsg carries the read model of all chains off the update loop.
type chainsDocMsg struct {
	doc relevo.ChainsDoc
	err error
}

// chainsView is ':chains': every chain in the read model, running and halted
// first, with fork children nested under their parents.
type chainsView struct {
	doc     relevo.ChainsDoc
	err     error
	loaded  bool
	cur     int
	top     int
	actions bool
}

// newChainsView builds ':chains' and the command that loads its doc.
func newChainsView(env Env) (View, tea.Cmd) {
	v := chainsView{actions: env.Actions != nil}
	return v, chainsDocCmd(env)
}

// chainsDocCmd reads chains off the update loop.
func chainsDocCmd(env Env) tea.Cmd {
	return func() tea.Msg {
		if env.Actions == nil {
			return chainsDocMsg{}
		}
		doc, err := env.Actions.Chains(env.Ctx)
		return chainsDocMsg{doc: doc, err: err}
	}
}

// chainCol is one fixed-width table column after NAME.
type chainCol struct {
	key  string
	head string
	w    int
}

// chainsLayout calculates column widths and handles degradation across widths.
func chainsLayout(width int) (int, []chainCol) {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	visible := []chainCol{
		{"status", "STATUS", 8},
		{"step", "STEP", 14},
		{"plans", "PLANS", 7},
		{"age", "AGE", 6},
		{"where", "WHERE", 16},
	}
	nameW := func(cols []chainCol) int {
		fixed := 2
		for i, c := range cols {
			fixed += c.w
			if i < len(cols)-1 {
				fixed += 2
			}
		}
		return cw - fixed
	}
	for _, drop := range []string{"where", "age"} {
		if nameW(visible) >= 36 {
			break
		}
		for i, c := range visible {
			if c.key == drop {
				visible = append(append([]chainCol(nil), visible[:i]...), visible[i+1:]...)
				break
			}
		}
	}
	return nameW(visible), visible
}

// chainsHeaderLine renders the table header row.
func chainsHeaderLine(nameW int, cols []chainCol, cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{{text: pad("NAME", nameW), style: style}}
	for _, c := range cols {
		cells = append(cells, candCell{text: pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

// chainStatusText returns the status display text and style, marking stale server chains.
func chainStatusText(c relevo.ChainEntry) (string, lipgloss.Style) {
	if c.Stale != "" {
		return "stale", warnStyle
	}
	switch c.Status {
	case string(chain.StatusRunning):
		return "running", greenStyle
	case string(chain.StatusHalted):
		return "halted", redStyle
	case string(chain.StatusStopped):
		return "stopped", mutedStyle
	case string(chain.StatusDone):
		return "done", faintStyle
	default:
		return sanitizeText(c.Status), mutedStyle
	}
}

// chainDataLine renders one chain's table row with indentation for depth.
func chainDataLine(c relevo.ChainEntry, sel bool, nameW int, cols []chainCol, cw int) string {
	nameStyle := textStyle
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	indent := strings.Repeat("  ", c.Depth)
	displayName := indent + c.Name
	cells := []candCell{{text: pad(displayName, nameW), style: nameStyle}}

	statusText, statusStyle := chainStatusText(c)

	for _, col := range cols {
		text := ""
		style := mutedStyle
		switch col.key {
		case "status":
			text, style = statusText, statusStyle
		case "step":
			text = sanitizeText(c.Step)
			if text == "" {
				text, style = "—", faintStyle
			}
		case "plans":
			text = fmt.Sprintf("%d/%d", c.PlanPos, c.PlanTotal)
		case "age":
			text = view.AgeText(c.Elapsed)
			if text == "" {
				text, style = "—", faintStyle
			}
		case "where":
			text = c.Where
			if text == "" {
				text = "local"
			}
		}
		cells = append(cells, candCell{text: pad(text, col.w), style: style})
	}
	return candLine(cells, sel, cw)
}

// chainReasonLine renders a halted chain's reason on a dim second line.
func chainReasonLine(c relevo.ChainEntry, sel bool, cw int) string {
	indent := strings.Repeat("  ", c.Depth)
	text := indent + "  " + sanitizeText(c.Reason)
	cells := []candCell{{text: text, style: faintStyle}}
	return candLine(cells, sel, cw)
}

// bodyLines builds all rendered table lines before viewport windowing.
func (v chainsView) bodyLines(env Env, width int) []string {
	nameW, cols := chainsLayout(width)
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	lines := []string{""}
	lines = append(lines, chainsHeaderLine(nameW, cols, cw))
	cur := candClamp(v.cur, len(v.doc.Chains))
	for i, c := range v.doc.Chains {
		isSel := i == cur
		lines = append(lines, chainDataLine(c, isSel, nameW, cols, cw))
		if c.Status == string(chain.StatusHalted) && c.Reason != "" {
			lines = append(lines, chainReasonLine(c, isSel, cw))
		}
	}
	return lines
}

// follow scrolls the viewport to keep the selected chain visible.
func (v *chainsView) follow(env Env) {
	if len(v.doc.Chains) == 0 {
		v.top = 0
		return
	}
	avail := bodyHeight(env)
	if avail <= 0 {
		v.top = 0
		return
	}
	lines := v.bodyLines(env, env.Width)
	sel := 2
	for i := 0; i < v.cur && i < len(v.doc.Chains); i++ {
		sel++
		if v.doc.Chains[i].Status == string(chain.StatusHalted) && v.doc.Chains[i].Reason != "" {
			sel++
		}
	}
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+avail {
		v.top = sel - avail + 1
	}
	v.top = clamp(v.top, 0, max(0, len(lines)-avail))
}

func (v chainsView) Crumbs() []string { return []string{"chains"} }

func (v chainsView) Capturing() bool { return false }

func (v chainsView) Keys() []KeyHelp {
	if !v.actions {
		return nil
	}
	return []KeyHelp{
		{"↑↓", "move"},
		{"enter", "steps"},
		{"t", "trace"},
		{"esc", "back"},
	}
}

func (v chainsView) HelpKeys() []KeyHelp { return v.Keys() }

func pluralChain(n int) string {
	if n == 1 {
		return "chain"
	}
	return "chains"
}

func (v chainsView) Context(env Env) (string, string) {
	running, halted, stopped, done := 0, 0, 0, 0
	for _, c := range v.doc.Chains {
		switch c.Status {
		case string(chain.StatusRunning):
			running++
		case string(chain.StatusHalted):
			halted++
		case string(chain.StatusStopped):
			stopped++
		case string(chain.StatusDone):
			done++
		}
	}
	part := func(n int, label string, style lipgloss.Style) string {
		return textStyle.Bold(true).Render(fmt.Sprintf("%d", n)) + " " + style.Render(label)
	}
	left := "   " + part(len(v.doc.Chains), pluralChain(len(v.doc.Chains)), mutedStyle)
	if running > 0 {
		left += "   " + part(running, "running", greenStyle)
	}
	if halted > 0 {
		left += "   " + part(halted, "halted", redStyle)
	}
	if stopped > 0 {
		left += "   " + part(stopped, "stopped", mutedStyle)
	}
	if done > 0 {
		left += "   " + part(done, "done", faintStyle)
	}
	return left, ""
}

func (v chainsView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if !v.loaded && v.err != nil {
		return strings.Join(statsCentered(v.err.Error(), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	if len(v.doc.Chains) == 0 {
		return strings.Join(statsCentered("no chains", width, height), "\n")
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

func (v chainsView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case chainsDocMsg:
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.doc = msg.doc
			v.err = nil
			v.loaded = true
		}
		v.cur = candClamp(v.cur, len(v.doc.Chains))
		return v, nil

	case statusMsg:
		return v, chainsDocCmd(env)

	case tickMsg:
		return v, chainsDocCmd(env)

	case tea.KeyMsg:
		if !v.actions {
			return v, nil
		}
		return v.updateKey(msg, env)
	}
	return v, nil
}

func (v chainsView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	n := len(v.doc.Chains)
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
		v, cmd := v.open(env)
		return v, cmd
	case "t":
		return v, v.openTrace(env)
	case "esc":
		return v, root(newFleetView(true).withActions(v.actions))
	}
	return v, nil
}

// open pushes the cursor's chain's steps, carrying the read model it already
// holds so the steps view needs no second read. A chain with no steps of its
// own says so rather than opening an empty table.
func (v chainsView) open(env Env) (View, tea.Cmd) {
	c := v.doc.Chains[candClamp(v.cur, len(v.doc.Chains))]
	if len(c.Steps) == 0 {
		return v, notice(c.Name + " has no steps: it ran on the fixed state machine")
	}
	return v, push(newChainStepsView(v.doc, c.Name), nil)
}

// openTrace pushes the cursor's chain's trace, read off the update loop.
func (v chainsView) openTrace(env Env) tea.Cmd {
	c := v.doc.Chains[candClamp(v.cur, len(v.doc.Chains))]
	if env.Actions == nil {
		return notice("a chain's trace needs relevo ui on this machine")
	}
	return tea.Batch(push(newChainTraceView(env, c.Name), nil), chainTraceCmd(env, c.Name))
}
