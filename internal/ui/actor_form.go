package ui

import (
	"context"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// actorTierNames is the tier row's chip order (§3).
var actorTierNames = []string{"read", "edit", "yolo", "harness"}

// actorAgents is the agents a form offers (§4): for a builtin actor name, only
// the agents whose shape equals the builtin's; for a custom actor, every
// agent. The order is the shipped table's order, then custom agents by name.
func actorAgents(doc relevo.ConfigDoc, name string) []string {
	shape := ""
	if role, ok := harness.RoleByName(name); ok {
		shape = "reader"
		if role.Shape == harness.ShapeBuilder {
			shape = "writer"
		}
	}
	var out []string
	seen := make(map[string]bool)
	for _, a := range roles.ShippedAgents() {
		if shape != "" && string(a.Shape) != shape {
			continue
		}
		seen[a.Name] = true
		out = append(out, a.Name)
	}
	custom := make([]string, 0, len(doc.Agents))
	for n := range doc.Agents {
		if seen[n] {
			continue
		}
		custom = append(custom, n)
	}
	sort.Strings(custom)
	return append(out, custom...)
}

// actorForm is the e key's overlay (§3, §4): the actor's agent, tier and
// check, each on a chip row. The check row is disabled for a reader agent.
type actorForm struct {
	ctx     context.Context
	actions Actions
	doc     relevo.ConfigDoc
	actor   string
	agents  []string
	asel    int
	tiers   []string
	tsel    int // -1 = unset
	check   bool
	builtin bool
	err     string
	focus   int // 0 agent, 1 tier, 2 check
}

// newActorForm builds the edit actor overlay (§4).
func newActorForm(env Env, doc relevo.ConfigDoc, name string) actorForm {
	a := doc.Actors[name]
	_, builtin := harness.RoleByName(name)
	f := actorForm{
		ctx:     env.Ctx,
		actions: env.Actions,
		doc:     doc,
		actor:   name,
		agents:  actorAgents(doc, name),
		tiers:   actorTierNames,
		tsel:    -1,
		check:   actorCheckOn(a.Check),
		builtin: builtin,
	}
	f.asel = candIndex(f.agents, a.Agent)
	if f.asel < 0 {
		f.asel = 0
	}
	if i := candIndex(f.tiers, a.Tier); i >= 0 {
		f.tsel = i
	}
	return f
}

// selectedAgent is the agent the agent row has selected.
func (f actorForm) selectedAgent() string {
	if f.asel < 0 || f.asel >= len(f.agents) {
		return ""
	}
	return f.agents[f.asel]
}

// checkDisabled reports whether the check row is off limits: the selected
// agent is a reader (§4).
func (f actorForm) checkDisabled() bool {
	return agentShapeOf(f.doc, f.selectedAgent()) == string(agentsrc.ShapeReader)
}

// checkSel is the check row's selected chip: 0 on, 1 off.
func (f actorForm) checkSel() int {
	if f.check {
		return 0
	}
	return 1
}

// keys are the form's own keys, shown in the footer through overlayKeyer (§4).
func (f actorForm) keys() []KeyHelp {
	return []KeyHelp{{"enter", "save"}, {"tab", "next field"}, {"esc", "cancel"}}
}

// setFocus moves focus to i (0 agent, 1 tier, 2 check), without the disabled
// check in the way: the callers pick the next focusable row themselves.
func (f actorForm) setFocus(i int) actorForm {
	f.focus = i
	return f
}

// nextFocus is tab's focus move, skipping the check row when it is disabled
// (§4).
func (f actorForm) nextFocus() actorForm {
	switch f.focus {
	case 0:
		return f.setFocus(1)
	case 1:
		if f.checkDisabled() {
			return f.setFocus(0)
		}
		return f.setFocus(2)
	}
	return f.setFocus(0)
}

// prevFocus is shift+tab's focus move, skipping the check row when it is
// disabled.
func (f actorForm) prevFocus() actorForm {
	switch f.focus {
	case 0:
		if f.checkDisabled() {
			return f.setFocus(1)
		}
		return f.setFocus(2)
	case 1:
		return f.setFocus(0)
	}
	return f.setFocus(1)
}

// moveSelection is left/right on the focused row (§4): the agent and tier
// chips wrap, the check row's selection moves between on and off.
func (f actorForm) moveSelection(delta int) actorForm {
	switch f.focus {
	case 0:
		if len(f.agents) > 0 {
			f.asel = ((f.asel+delta)%len(f.agents) + len(f.agents)) % len(f.agents)
		}
	case 1:
		if f.tsel < 0 {
			if delta < 0 {
				f.tsel = len(f.tiers) - 1
			} else {
				f.tsel = 0
			}
		} else {
			f.tsel = ((f.tsel+delta)%len(f.tiers) + len(f.tiers)) % len(f.tiers)
		}
	case 2:
		f.check = delta < 0
	}
	return f
}

// update handles one key (§4): esc cancels, tab moves focus, left/right move
// the focused row's selection, enter submits.
func (f actorForm) update(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
	switch k.String() {
	case "esc":
		return f, nil, true
	case "tab":
		return f.nextFocus(), nil, false
	case "shift+tab", "back_tab":
		return f.prevFocus(), nil, false
	case "enter":
		return f.submit()
	case "left":
		return f.moveSelection(-1), nil, false
	case "right":
		return f.moveSelection(1), nil, false
	}
	return f, nil, false
}

// submit is enter (§4): an unchanged actor closes with a notice, an invalid
// edit keeps the form open on its error, and a valid one closes and applies
// it.
func (f actorForm) submit() (overlay, tea.Cmd, bool) {
	a := f.doc.Actors[f.actor]
	tier := ""
	if f.tsel >= 0 && f.tsel < len(f.tiers) {
		tier = f.tiers[f.tsel]
	}
	agent := f.selectedAgent()
	if agent == a.Agent && tier == a.Tier && f.check == actorCheckOn(a.Check) {
		return f, notice("nothing changed"), true
	}

	edit, err := relevo.EditActor(f.doc, f.actor, agent, tier, f.check)
	if err != nil {
		f.err = err.Error()
		return f, nil, false
	}
	return f, runAction(f.ctx, "edit actor", "actor:"+f.actor, func(ctx context.Context) Result {
		return f.actions.ApplyConfig(ctx, edit)
	}), true
}

func (f actorForm) view(width int) []string {
	_, rows, _, _ := f.modal(width)
	return rows
}

// modal draws the form box (§4): title `edit <name>`, want 90, rows in the
// order §4 lists.
func (f actorForm) modal(width int) (string, []string, int, bool) {
	const want = 90
	_ = modalInnerW(want, width)

	rows := []string{""}
	rows = append(rows, formLabel("name", false)+mutedStyle.Render(f.actor))
	rows = append(rows, "")
	rows = append(rows, formLabel("agent", f.focus == 0)+formChips(f.agents, f.asel, false))
	rows = append(rows, strings.Repeat(" ", 11)+mutedStyle.Render(agentShapeOf(f.doc, f.selectedAgent())))
	rows = append(rows, "")
	rows = append(rows, formLabel("tier", f.focus == 1)+formChips(f.tiers, f.tsel, false))
	rows = append(rows, "")
	rows = append(rows, formLabel("check", f.focus == 2)+
		formChips([]string{"on", "off"}, f.checkSel(), f.checkDisabled()))
	rows = append(rows, "")
	if f.err != "" {
		rows = append(rows, formError(f.err))
	}
	rows = append(rows, "")
	rows = append(rows, formKeys(f.keys(), nil))
	return "edit " + f.actor, rows, want, false
}

// addActorForm is the list's a key's overlay (§3, §4): the new actor's name
// and agent.
type addActorForm struct {
	ctx     context.Context
	actions Actions
	doc     relevo.ConfigDoc
	nameIn  textinput.Model
	agents  []string
	asel    int
	focus   int // 0 name, 1 agent
	tried   bool
	err     string
}

// newAddActorForm builds the add actor overlay (§4). Every agent is offered:
// the name is new, so no builtin shape filters the list.
func newAddActorForm(env Env, doc relevo.ConfigDoc) addActorForm {
	in := newFormInput(true)
	in.Placeholder = "lowercase, e.g. security-review"
	return addActorForm{
		ctx:     env.Ctx,
		actions: env.Actions,
		doc:     doc,
		nameIn:  in,
		agents:  actorAgents(doc, ""),
	}
}

// selectedAgent is the agent the agent row has selected.
func (f addActorForm) selectedAgent() string {
	if f.asel < 0 || f.asel >= len(f.agents) {
		return ""
	}
	return f.agents[f.asel]
}

// keys are the form's own keys, shown in the footer through overlayKeyer (§4).
func (f addActorForm) keys() []KeyHelp {
	return []KeyHelp{{"enter", "add"}, {"tab", "next field"}, {"esc", "cancel"}}
}

// setFocus moves focus to i (0 name, 1 agent), focusing that row's field.
func (f addActorForm) setFocus(i int) addActorForm {
	f.focus = ((i % 2) + 2) % 2
	if f.focus == 0 {
		f.nameIn.Focus()
	} else {
		f.nameIn.Blur()
	}
	return f
}

// update handles one key (§4): esc cancels, tab moves focus, the name row
// takes every key, the agent row takes left/right, enter adds.
func (f addActorForm) update(k tea.KeyMsg) (overlay, tea.Cmd, bool) {
	switch k.String() {
	case "esc":
		return f, nil, true
	case "tab", "shift+tab", "back_tab":
		return f.setFocus(1 - f.focus), nil, false
	case "enter":
		return f.submit()
	}
	if f.focus == 0 {
		var cmd tea.Cmd
		f.nameIn, cmd = f.nameIn.Update(k)
		return f, cmd, false
	}
	switch k.String() {
	case "left":
		if len(f.agents) > 0 {
			f.asel = (f.asel - 1 + len(f.agents)) % len(f.agents)
		}
	case "right":
		if len(f.agents) > 0 {
			f.asel = (f.asel + 1) % len(f.agents)
		}
	}
	return f, nil, false
}

// submit is enter (§4): AddActor validates the name and agent. A FieldError
// shows red and keeps the form open; success closes and adds the actor.
func (f addActorForm) submit() (overlay, tea.Cmd, bool) {
	f.tried = true
	name := strings.TrimSpace(f.nameIn.Value())
	edit, err := relevo.AddActor(f.doc, name, f.selectedAgent())
	if err != nil {
		f.err = err.Error()
		return f, nil, false
	}
	return f, runAction(f.ctx, "add actor", name, func(ctx context.Context) Result {
		return f.actions.ApplyConfig(ctx, edit)
	}), true
}

func (f addActorForm) view(width int) []string {
	_, rows, _, _ := f.modal(width)
	return rows
}

// modal draws the form box (§4): title `add actor`, want 90, the name row, the
// agent chips, the error when there is one, and the keys.
func (f addActorForm) modal(width int) (string, []string, int, bool) {
	const want = 90
	innerW := modalInnerW(want, width)
	fieldW := innerW - 11
	if fieldW < 1 {
		fieldW = 1
	}

	rows := []string{""}
	rows = append(rows, formLabel("name", f.focus == 0)+formInput(f.nameIn, f.focus == 0, "", fieldW))
	rows = append(rows, "")
	rows = append(rows, formLabel("agent", f.focus == 1)+formChips(f.agents, f.asel, false))
	rows = append(rows, "")
	if f.err != "" {
		rows = append(rows, formError(f.err))
	}
	rows = append(rows, "")
	rows = append(rows, formKeys(f.keys(), nil))
	return "add actor", rows, want, false
}
