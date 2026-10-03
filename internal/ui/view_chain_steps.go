package ui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// chainStepsView is one chain's steps: the step in flight accented, a step
// never reached faint, and every step's last outcome as the read model
// derived it. It carries the whole read model, so a fork child reached from
// the chains list opens the same view for itself.
type chainStepsView struct {
	doc   relevo.ChainsDoc
	chain string
	cur   int
	top   int
}

// chainStepCap bounds how wide STEP grows to fit a step's id. Past it a long id
// is cut with an ellipsis, so one verbose step cannot starve the outcome
// beside it. Every step id the built-in workflows and the goldens name fits
// well inside 32 -- the longest, planning-fixes, is 14 cells -- so the cap only
// bites on an id no workflow here uses.
const chainStepCap = 32

// chainStepsLayout is the steps table's columns: STEP is as wide as the longest
// step id in the read model, capped at chainStepCap, then KIND, ACTOR and VISITS
// at their widths, then LAST OUTCOME with what is left. STEP never takes the
// greedy remainder: the outcome is the cell that says what happened in a round,
// so the width STEP does not need is the outcome's. A terminal too narrow for
// the outcome drops it first, then the actor.
func chainStepsLayout(width int, entry relevo.ChainEntry) (int, []chainCol) {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	stepW := chainStepWidth(entry.Steps)
	cols := []chainCol{
		{"kind", "KIND", 8},
		{"actor", "ACTOR", 16},
		{"visits", "VISITS", 6},
		{"outcome", "LAST OUTCOME", 30},
	}
	// LAST OUTCOME is the cell that says what happened in a round, so it is the
	// one that grows: it takes the row's width whatever STEP does not need, down
	// to the 24 cells below which an outcome reads as a fragment. Under that the
	// whole column goes, then ACTOR, so the row degrades by whole columns rather
	// than by cutting an outcome in half.
	outcomeW := func(cols []chainCol) int {
		last := cols[len(cols)-1].key == "outcome"
		rest := columnsWidth(cols)
		if last {
			rest -= cols[len(cols)-1].w
		}
		return cw - stepW - rest
	}
	for len(cols) > 0 {
		minimum := 16
		if cols[len(cols)-1].key == "outcome" {
			minimum = 24
		}
		if outcomeW(cols) >= minimum {
			break
		}
		cols = cols[:len(cols)-1]
	}
	if w := outcomeW(cols); w > 0 {
		cols[len(cols)-1].w = w
	}
	return stepW, cols
}

// chainStepWidth is the width STEP needs for the steps it renders: the longest
// step id, never less than the header.
func chainStepWidth(steps []relevo.ChainStep) int {
	w := lipgloss.Width("STEP")
	for _, s := range steps {
		if n := lipgloss.Width(sanitizeText(s.ID)); n > w {
			w = n
		}
	}
	if w > chainStepCap {
		w = chainStepCap
	}
	return w
}

// chainStepsHeaderLine renders the steps table's header row.
func chainStepsHeaderLine(stepW int, cols []chainCol, cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{{text: pad("STEP", stepW), style: style}}
	for _, c := range cols {
		cells = append(cells, candCell{text: pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

// stepReached reports whether a step has run: a step the engine awaits has,
// one with visits or a recorded result has, and a step with neither never has.
func stepReached(s relevo.ChainStep) bool {
	return s.InFlight || s.Visits > 0 || s.Round > 0
}

// chainStepLine renders one step's row. A step never reached is faint
// throughout, so an unopened plan reads as unopened; the step in flight is
// accented and the cursor row carries the selection band.
func chainStepLine(s relevo.ChainStep, sel bool, stepW int, cols []chainCol, cw int) string {
	base := textStyle
	if !stepReached(s) {
		base = faintStyle
	}
	nameStyle := base
	if s.InFlight {
		nameStyle = accentStyle
	}
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	cells := []candCell{{text: pad(sanitizeText(s.ID), stepW), style: nameStyle}}
	for _, col := range cols {
		text := ""
		style := base
		switch col.key {
		case "kind":
			text = sanitizeText(s.Kind)
			if text == "" {
				text, style = "—", faintStyle
			}
		case "actor":
			text = sanitizeText(s.Actor)
			if text == "" {
				text, style = "—", faintStyle
			}
		case "visits":
			text = "—"
			if s.Visits > 0 {
				text = strconv.Itoa(s.Visits)
			} else {
				style = faintStyle
			}
		case "outcome":
			text = sanitizeText(s.LastOutcome)
			if text == "" {
				text, style = "—", faintStyle
			}
		}
		cells = append(cells, candCell{text: pad(text, col.w), style: style})
	}
	return candLine(cells, sel, cw)
}

// chainStepsBodyLines is the whole steps body before the viewport windows it.
func (v chainStepsView) bodyLines(width int) []string {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	entry, ok := v.entry()
	if !ok {
		return []string{""}
	}
	stepW, cols := chainStepsLayout(width, entry)
	lines := []string{""}
	lines = append(lines, chainStepsHeaderLine(stepW, cols, cw))
	for i, s := range entry.Steps {
		lines = append(lines, chainStepLine(s, i == candClamp(v.cur, len(entry.Steps)), stepW, cols, cw))
	}
	return lines
}

// entry is this view's own chain in the read model, or the zero entry when
// the model no longer holds it.
func (v chainStepsView) entry() (relevo.ChainEntry, bool) {
	for _, c := range v.doc.Chains {
		if c.Name == v.chain {
			return c, true
		}
	}
	return relevo.ChainEntry{}, false
}

// follow scrolls the viewport so the cursor's step stays visible.
func (v *chainStepsView) follow(env Env) {
	entry, ok := v.entry()
	if !ok {
		v.top = 0
		return
	}
	avail := bodyHeight(env)
	if avail <= 0 {
		v.top = 0
		return
	}
	sel := 2 + candClamp(v.cur, len(entry.Steps))
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+avail {
		v.top = sel - avail + 1
	}
	v.top = clamp(v.top, 0, max(0, len(entry.Steps)+2-avail))
}

// newChainStepsView pushes one chain's steps, out of a read model the chains
// view already holds, so no second read is issued.
func newChainStepsView(doc relevo.ChainsDoc, name string) View {
	return chainStepsView{doc: doc, chain: name}
}

func (v chainStepsView) Crumbs() []string { return []string{v.chain} }

func (v chainStepsView) Capturing() bool { return false }

func (v chainStepsView) Keys() []KeyHelp {
	return []KeyHelp{
		{"↑↓", "move"},
		{"enter", "open round"},
		{"t", "trace"},
	}
}

func (v chainStepsView) HelpKeys() []KeyHelp { return v.Keys() }

// Context is the steps context row: the chain's status word and the step the
// engine is on, or its step count when no step is in flight.
func (v chainStepsView) Context(env Env) (string, string) {
	entry, ok := v.entry()
	if !ok {
		return "   " + mutedStyle.Render(v.chain+" is gone"), ""
	}
	statusText, statusStyle := chainStatusText(entry)
	left := "   " + statusStyle.Bold(true).Render(statusText)
	right := ""
	if entry.Step != "" {
		right = mutedStyle.Render(sanitizeText(entry.Step)) + "   "
	} else {
		right = mutedStyle.Render(pluralStep(len(entry.Steps))) + "   "
	}
	if lipgloss.Width(left)+1+lipgloss.Width(right) > env.Width {
		right = ""
	}
	return left, right
}

// pluralStep names a step count.
func pluralStep(n int) string {
	if n == 1 {
		return "1 step"
	}
	return strconv.Itoa(n) + " steps"
}

func (v chainStepsView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	entry, ok := v.entry()
	if !ok {
		return strings.Join(statsCentered(v.chain+" is gone", width, height), "\n")
	}
	if len(entry.Steps) == 0 {
		return strings.Join(statsCentered("no steps: this chain ran on the fixed state machine", width, height), "\n")
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

func (v chainStepsView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return v.updateKey(msg, env)
	}
	return v, nil
}

func (v chainStepsView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	entry, ok := v.entry()
	if !ok {
		return v, notice(v.chain + " is gone")
	}
	n := len(entry.Steps)
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
		return v, v.openStep(env, entry, candClamp(v.cur, n))
	case "t":
		if env.Actions == nil {
			return v, notice("a chain's trace needs relevo ui on this machine")
		}
		return v, tea.Batch(push(newChainTraceView(env, v.chain, "trace"), nil), chainTraceCmd(env, v.chain))
	case "esc":
		return v, pop()
	}
	return v, nil
}

// openStep is the enter on a step: the member's own round at the step's
// round. A step that never ran has no round to open, and a check step has a
// run rather than a member round, so it says so instead of guessing.
func (v chainStepsView) openStep(env Env, entry relevo.ChainEntry, i int) tea.Cmd {
	s := entry.Steps[i]
	if !stepReached(s) {
		return notice("no round yet: " + s.ID + " has not run")
	}
	if s.Member == "" {
		return notice("check runs have no round: " + s.ID)
	}
	next, cmd := newChainRoundView(env, s.Member, s.Round, entry.Members, []string{s.ID})
	return push(next, cmd)
}
