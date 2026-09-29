package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// actorsView is ':actors' (§4): every actor with its agent, shape, tier and
// candidate count, the candidate the pick would take next, the cursor row's
// detail block, and the a/d keys.
type actorsView struct {
	doc     relevo.ConfigDoc
	err     error // the last ConfigDoc error; shown centred like candidates' error state
	loaded  bool
	cur     int // cursor over actorOrder(doc.Actors)
	top     int // first body line shown (page follows the cursor)
	actions bool
}

// newActorsView builds ':actors' and the command that loads its doc (§4).
// env.Actions must be non-nil: execLine refuses the command otherwise.
func newActorsView(env Env) (View, tea.Cmd) {
	v := actorsView{actions: env.Actions != nil}
	return v, candDocCmd(env)
}

// agentShapeOf is the agent's shape word ("writer"/"reader"), "" when the
// agent cannot be resolved (§3). It reuses relevo.AgentShape, which is the
// one place custom sources are parsed.
func agentShapeOf(doc relevo.ConfigDoc, agent string) string {
	shape, err := relevo.AgentShape(doc, agent)
	if err != nil {
		return ""
	}
	return shape
}

// actorToken is the canonical token of the candidate named name, "" when no
// candidate has that name.
func actorToken(doc relevo.ConfigDoc, name string) string {
	for _, c := range doc.Candidates {
		if c.Name == name {
			return c.Ref().String()
		}
	}
	return ""
}

// gateOn is the gate on token, ok false when none is.
func gateOn(gated []availability.Gate, token string) (availability.Gate, bool) {
	if token == "" {
		return availability.Gate{}, false
	}
	for i := range gated {
		if gated[i].Token == token {
			return gated[i], true
		}
	}
	return availability.Gate{}, false
}

// nextPick is the actor's next pick (§3): the first entry, in order, that is on
// and whose candidate token has no gate. "" when none.
func nextPick(doc relevo.ConfigDoc, actor string, gated []availability.Gate) string {
	for _, e := range doc.Actors[actor].Candidates {
		if e.Off {
			continue
		}
		name := candName(doc, e.Candidate)
		if name == "" {
			continue
		}
		if _, ok := gateOn(gated, actorToken(doc, name)); ok {
			continue
		}
		return name
	}
	return ""
}

// entryStatus is one entry's STATUS text and colour (§3): off in faint, else
// the gate's time left in red, else ready in green.
func entryStatus(doc relevo.ConfigDoc, e roles.Entry, gated []availability.Gate, now time.Time) (string, lipgloss.Style) {
	if e.Off {
		return "off", faintStyle
	}
	name := candName(doc, e.Candidate)
	if g, ok := gateOn(gated, actorToken(doc, name)); ok {
		return statsGateLeft(g, now), redStyle
	}
	return "ready", greenStyle
}

// actorCol is one fixed-width table column after ACTOR.
type actorCol struct {
	key  string
	head string
	w    int
}

// actorListLayout is the columns to show at width and the ACTOR column's room
// (§4): the table spans width-6 cells from column 3, each column followed by
// two spaces, ACTOR taking the rest. While that would fall under 12 cells,
// columns leave in the order SHAPE, AGENT.
func actorListLayout(width int) (int, []actorCol) {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	visible := []actorCol{
		{"agent", "AGENT", 13},
		{"shape", "SHAPE", 6},
		{"tier", "TIER", 5},
		{"cands", "CANDIDATES", 10},
		{"next", "NEXT PICK", 21},
	}
	nameW := func(cols []actorCol) int {
		fixed := 2 // the two spaces after ACTOR
		for i, c := range cols {
			fixed += c.w
			if i < len(cols)-1 {
				fixed += 2
			}
		}
		return cw - fixed
	}
	for _, drop := range []string{"shape", "agent"} {
		if nameW(visible) >= 12 {
			break
		}
		for i, c := range visible {
			if c.key == drop {
				visible = append(append([]actorCol(nil), visible[:i]...), visible[i+1:]...)
				break
			}
		}
	}
	return nameW(visible), visible
}

// actorHeaderLine is the list's header row (§4), in faint bold.
func actorHeaderLine(nameW int, cols []actorCol, cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{{text: pad("ACTOR", nameW), style: style}}
	for _, c := range cols {
		cells = append(cells, candCell{text: pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

// actorDataLine is one actor's table row (§4). The cursor row's name is bold;
// NEXT PICK is green, and `—` faint when there is none.
func actorDataLine(doc relevo.ConfigDoc, env Env, name string, sel bool, nameW int, cols []actorCol, cw int) string {
	a := doc.Actors[name]
	nameStyle := textStyle
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	cells := []candCell{{text: pad(name, nameW), style: nameStyle}}
	for _, col := range cols {
		text := ""
		style := mutedStyle
		switch col.key {
		case "agent":
			text = a.Agent
		case "shape":
			text = agentShapeOf(doc, a.Agent)
		case "tier":
			text = a.Tier
			if text == "" {
				text, style = "—", faintStyle
			}
		case "cands":
			text = fmt.Sprintf("%*d", col.w, len(a.Candidates))
		case "next":
			if np := nextPick(doc, name, env.Report.Gated); np != "" {
				text, style = np, greenStyle
			} else {
				text, style = "—", faintStyle
			}
		}
		cells = append(cells, candCell{text: pad(text, col.w), style: style})
	}
	return candLine(cells, sel, cw)
}

// actorTierText is an actor's tier, or `—` when it has none.
func actorTierText(tier string) string {
	if tier == "" {
		return "—"
	}
	return tier
}

// actorMetaText is the detail block's `<agent> · <shape> · tier <tier>` line
// (§4).
func actorMetaText(doc relevo.ConfigDoc, a roles.Actor) string {
	return a.Agent + " · " + agentShapeOf(doc, a.Agent) + " · tier " + actorTierText(a.Tier)
}

// actorDetailLines is the cursor row's detail block (§4): two blanks are added
// by the caller, then the name and its triple, then one line per entry
// `" %2d  %-23s%-10s"` with the `next pick` marker.
func actorDetailLines(doc relevo.ConfigDoc, env Env, name string, width int) []string {
	a := doc.Actors[name]
	first := "   " + faintStyle.Bold(true).Render(name) + "   " + mutedStyle.Render(actorMetaText(doc, a))
	out := []string{fit(first, width)}

	np := nextPick(doc, name, env.Report.Gated)
	for i, e := range a.Candidates {
		text, style := entryStatus(doc, e, env.Report.Gated, env.Now)
		line := "  " + faintStyle.Render(fmt.Sprintf(" %2d", i+1)) + "  " +
			textStyle.Render(pad(candName(doc, e.Candidate), 23)) + style.Render(pad(text, 10))
		if p := candName(doc, e.Candidate); np != "" && p == np {
			line += "  " + greenStyle.Bold(true).Render("next pick")
		}
		out = append(out, fit(line, width))
	}
	return out
}

// bodyLines is the whole list body before it is windowed: a blank line under
// the context row, the table, two blank lines and the cursor row's detail
// block (§4).
func (v actorsView) bodyLines(env Env, width int) []string {
	names := actorOrder(v.doc.Actors)
	nameW, cols := actorListLayout(width)
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	lines := []string{""}
	lines = append(lines, actorHeaderLine(nameW, cols, cw))
	cur := candClamp(v.cur, len(names))
	for i, name := range names {
		lines = append(lines, actorDataLine(v.doc, env, name, i == cur, nameW, cols, cw))
	}
	if len(names) == 0 {
		return lines
	}
	lines = append(lines, "", "")
	return append(lines, actorDetailLines(v.doc, env, names[cur], width)...)
}

// follow scrolls the page so the cursor's table row stays visible (§4).
func (v *actorsView) follow(env Env) {
	names := actorOrder(v.doc.Actors)
	if len(names) == 0 {
		v.top = 0
		return
	}
	avail := bodyHeight(env)
	if avail <= 0 {
		v.top = 0
		return
	}
	lines := v.bodyLines(env, env.Width)
	sel := 2 + v.cur // the blank line, the header row, then the rows
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+avail {
		v.top = sel - avail + 1
	}
	v.top = clamp(v.top, 0, max(0, len(lines)-avail))
}

func (v actorsView) Crumbs() []string { return []string{"actors"} }

// Capturing is always false: the view owns no text input of its own (§4).
func (v actorsView) Capturing() bool { return false }

// Keys are the list's keys (§4), shown only when the shell has an Actions
// seam.
func (v actorsView) Keys() []KeyHelp {
	if !v.actions {
		return nil
	}
	return []KeyHelp{
		{"↑↓", "move"},
		{"enter", "open"},
		{"a", "add"},
		{"d", "delete"},
	}
}

// HelpKeys is the help overlay's key list (§2.2): the same set.
func (v actorsView) HelpKeys() []KeyHelp { return v.Keys() }

// Context is the counts line on the left (§4); the list has no right side.
func (v actorsView) Context(env Env) (string, string) {
	names := actorOrder(v.doc.Actors)
	writers, readers := 0, 0
	for _, name := range names {
		if agentShapeOf(v.doc, v.doc.Actors[name].Agent) == string(agentsrc.ShapeWriter) {
			writers++
		} else {
			readers++
		}
	}
	word := func(n int, label string) string {
		if n != 1 {
			label += "s"
		}
		return textStyle.Bold(true).Render(fmt.Sprintf("%d", n)) + " " + mutedStyle.Render(label)
	}
	left := "   " + word(len(names), "actor")
	left += "   " + word(writers, "writer")
	left += "   " + word(readers, "reader")
	return left, ""
}

func (v actorsView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if !v.loaded && v.err != nil {
		return strings.Join(statsCentered(v.err.Error(), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
	}
	if len(actorOrder(v.doc.Actors)) == 0 {
		return strings.Join(statsCentered("no actors; a adds one", width, height), "\n")
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

func (v actorsView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case candDocMsg:
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.doc = msg.doc
			v.err = nil
			v.loaded = true
		}
		v.cur = candClamp(v.cur, len(actorOrder(v.doc.Actors)))
		return v, nil

	case statusMsg:
		// A refresh from anywhere re-reads the config, which is what
		// reloads the table after ApplyConfig's Refresh (§4).
		return v, candDocCmd(env)

	case tea.KeyMsg:
		if !v.actions {
			return v, nil
		}
		return v.updateKey(msg, env)
	}
	return v, nil
}

// updateKey is the list's own keys (§4).
func (v actorsView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	names := actorOrder(v.doc.Actors)
	n := len(names)
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
		return v, openActor(v.doc, names[v.cur], env)
	case "a":
		return v, openOverlay(newAddActorForm(env, v.doc))
	case "d":
		if n == 0 {
			return v, nil
		}
		return v, v.deleteCmd(env, names[v.cur])
	}
	return v, nil
}

// openActor pushes the cursor actor's detail view (§4).
func openActor(doc relevo.ConfigDoc, name string, env Env) tea.Cmd {
	v, init := newActorView(env, doc, name)
	return push(v, init)
}

// deleteCmd is the list's d key (§4): validate the delete, then confirm it as
// a danger box. A refused delete (a builtin actor) is a notice.
func (v actorsView) deleteCmd(env Env, name string) tea.Cmd {
	edit, err := relevo.DeleteActor(v.doc, name)
	if err != nil {
		return notice(err.Error())
	}
	a := v.doc.Actors[name]
	return openOverlay(confirmBox{
		kind:   "delete",
		yes:    "delete",
		danger: true,
		title:  "Delete actor " + accentStyle.Bold(true).Render(name) + "?",
		lines: []string{
			pad("agent", 11) + a.Agent,
			pad("candidates", 11) + fmt.Sprintf("%d", len(a.Candidates)),
			pad("kept", 11) + "the candidates themselves",
		},
		onYes: runAction(env.Ctx, "delete actor", name, func(ctx context.Context) Result {
			return env.Actions.ApplyConfig(ctx, edit)
		}),
	})
}
