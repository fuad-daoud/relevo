package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// actorView is `actors › <name>` (§4): the actor's candidate list, reordered,
// turned on and off, added to from a picker and removed from, its check line,
// and the edit form behind e.
type actorView struct {
	name   string
	doc    relevo.ConfigDoc
	err    error // the last ConfigDoc error; shown centred like candidates' error state
	loaded bool
	cur    int // cursor over the actor's entries
	top    int // first body line shown (page follows the cursor)
}

// newActorView pushes the actor's detail view (§4).
func newActorView(env Env, doc relevo.ConfigDoc, name string) (View, tea.Cmd) {
	v := actorView{name: name, doc: doc, loaded: true}
	return v, candDocCmd(env)
}

// actorCheckOn reports whether an actor's check is on: nil means on.
func actorCheckOn(check *bool) bool { return check == nil || *check }

// actorPickLayout is the detail table's columns (§4): the `#` column, then
// CANDIDATE taking the rest of width-6 cells, HARNESS 8, PROVIDER 10 and
// STATUS 20, every column followed by two spaces.
func actorPickLayout(width int) (int, []actorCol) {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	cols := []actorCol{
		{"harness", "HARNESS", 8},
		{"provider", "PROVIDER", 10},
		{"status", "STATUS", 20},
	}
	fixed := 4 // the two spaces after # and after CANDIDATE
	for i, c := range cols {
		fixed += c.w
		if i < len(cols)-1 {
			fixed += 2
		}
	}
	return cw - fixed, cols
}

// actorPickHeaderLine is the detail table's header row (§4), in faint bold.
func actorPickHeaderLine(nameW int, cols []actorCol, cw int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{
		{text: fmt.Sprintf("%2s", "#"), style: style},
		{text: pad("CANDIDATE", nameW), style: style},
	}
	for _, c := range cols {
		cells = append(cells, candCell{text: pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

// actorPickLine is one entry's row (§4). An off row is faint throughout; the
// cursor row's candidate name is bold; the entry that would be picked next
// carries the `next pick` marker in its STATUS cell.
func actorPickLine(doc relevo.ConfigDoc, env Env, name string, i int, sel bool, nameW int, cols []actorCol, cw int) string {
	a := doc.Actors[name]
	e := a.Candidates[i]
	base, meta := textStyle, mutedStyle
	if e.Off {
		base, meta = faintStyle, faintStyle
	}
	nameStyle := base
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	cname := candName(doc, e.Candidate)
	c, _ := candByName(doc, cname)
	text, style := entryStatus(doc, e, env.Report.Gated, env.Now)
	if np := nextPick(doc, name, env.Report.Gated); np != "" && cname == np {
		text += "  next pick"
	}
	cells := []candCell{
		{text: fmt.Sprintf("%2d", i+1), style: meta},
		{text: pad(cname, nameW), style: nameStyle},
		{text: pad(c.Harness, 8), style: meta},
		{text: pad(c.Provider, 10), style: meta},
		{text: pad(text, 20), style: style},
	}
	return candLine(cells, sel, cw)
}

// actorCheckLine is the check line under the table (§4), for writer agents
// only: ""
// when the actor's agent is a reader.
func actorCheckLine(doc relevo.ConfigDoc, name string, width int) (string, bool) {
	a := doc.Actors[name]
	if agentShapeOf(doc, a.Agent) != string(agentsrc.ShapeWriter) {
		return "", false
	}
	if !actorCheckOn(a.Check) {
		return "   " + mutedStyle.Render("no check after each round"), true
	}
	if cmd := doc.Policy.GateDefault(); cmd != "" {
		return "   " + mutedStyle.Render("runs ") + textStyle.Render(cmd) +
			mutedStyle.Render(" after each round"), true
	}
	return "   " + mutedStyle.Render("check after each round is on, but no check command is set; set one in ") +
		textStyle.Render(":settings"), true
}

// bodyLines is the whole detail body before it is windowed: a blank line under
// the context row, the table, two blank lines and the check line (§4).
func (v actorView) bodyLines(env Env, width int) []string {
	a := v.doc.Actors[v.name]
	nameW, cols := actorPickLayout(width)
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	lines := []string{""}
	lines = append(lines, actorPickHeaderLine(nameW, cols, cw))
	cur := candClamp(v.cur, len(a.Candidates))
	for i := range a.Candidates {
		lines = append(lines, actorPickLine(v.doc, env, v.name, i, i == cur, nameW, cols, cw))
	}
	lines = append(lines, "", "")
	if check, ok := actorCheckLine(v.doc, v.name, width); ok {
		lines = append(lines, fit(check, width))
	}
	return lines
}

// follow scrolls the page so the cursor's table row stays visible (§4).
func (v *actorView) follow(env Env) {
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

func (v actorView) Crumbs() []string { return []string{v.name} }

// Capturing is always false: the view owns no text input of its own (§4).
func (v actorView) Capturing() bool { return false }

// Keys are the detail view's keys (§4).
func (v actorView) Keys() []KeyHelp {
	return []KeyHelp{
		{"↑↓", "move"},
		{"shift+↑↓", "reorder"},
		{"space", "on / off"},
		{"a", "add"},
		{"d", "remove"},
		{"e", "edit"},
		{"esc", "back"},
	}
}

// HelpKeys is the help overlay's key list (§2.2): the same set.
func (v actorView) HelpKeys() []KeyHelp { return v.Keys() }

// Context is the actor's summary line (§4): the agent and shape, the tier, the
// candidate count and the next pick.
func (v actorView) Context(env Env) (string, string) {
	a := v.doc.Actors[v.name]
	tier, tierStyle := a.Tier, textStyle
	if tier == "" {
		tier, tierStyle = "—", faintStyle
	}
	np, nextStyle := nextPick(v.doc, v.name, env.Report.Gated), greenStyle
	if np == "" {
		np, nextStyle = "—", faintStyle
	}
	left := "   " + textStyle.Render(a.Agent) + mutedStyle.Render(" · "+agentShapeOf(v.doc, a.Agent)+"   ") +
		faintStyle.Render("tier ") + tierStyle.Render(tier) +
		"   " + textStyle.Bold(true).Render(fmt.Sprintf("%d", len(a.Candidates))) +
		mutedStyle.Render(" candidates   ") + faintStyle.Render("next pick ") + nextStyle.Render(np)
	return left, ""
}

func (v actorView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if !v.loaded && v.err != nil {
		return strings.Join(statsCentered(v.err.Error(), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(blockLines([]string{"loading…"}, width, height), "\n")
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

func (v actorView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case candDocMsg:
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.doc = msg.doc
			v.err = nil
			v.loaded = true
		}
		v.cur = candClamp(v.cur, len(v.doc.Actors[v.name].Candidates))
		return v, nil

	case statusMsg:
		// A refresh from anywhere re-reads the config, which is what
		// reloads the table after ApplyConfig's Refresh (§4).
		return v, candDocCmd(env)

	case tea.KeyMsg:
		return v.updateKey(msg, env)
	}
	return v, nil
}

// updateKey is the detail view's own keys (§4). While an edit of this actor is
// in flight, every change key is ignored, so two fast presses cannot build a
// second edit on a list the first has not written yet.
func (v actorView) updateKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	entries := v.doc.Actors[v.name].Candidates
	n := len(entries)

	if env.Running["actor:"+v.name] != "" {
		switch k.String() {
		case "shift+up", "shift+down", " ", "space", "d", "a", "e":
			return v, nil
		}
	}

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
	case "shift+up":
		if n < 2 || v.cur <= 0 {
			return v, nil
		}
		next := copyActorEntries(entries)
		next[v.cur-1], next[v.cur] = next[v.cur], next[v.cur-1]
		v.cur--
		return v.change(env, next)
	case "shift+down":
		if n < 2 || v.cur >= n-1 {
			return v, nil
		}
		next := copyActorEntries(entries)
		next[v.cur], next[v.cur+1] = next[v.cur+1], next[v.cur]
		v.cur++
		return v.change(env, next)
	case " ", "space":
		if n == 0 {
			return v, nil
		}
		next := copyActorEntries(entries)
		next[v.cur].Off = !next[v.cur].Off
		return v.change(env, next)
	case "d":
		if n == 0 {
			return v, nil
		}
		if n == 1 {
			return v, notice(v.name + " needs at least one candidate")
		}
		next := make([]roles.Entry, 0, n-1)
		next = append(next, entries[:v.cur]...)
		next = append(next, entries[v.cur+1:]...)
		v.cur = candClamp(v.cur, len(next))
		return v.change(env, next)
	case "a":
		return v, v.pickCmd(env)
	case "e":
		return v, openOverlay(newActorForm(env, v.doc, v.name))
	}
	return v, nil
}

// copyActorEntries is a fresh copy of one actor's entry list: a change builds
// its own list, so the doc the view holds is never mutated in place.
func copyActorEntries(entries []roles.Entry) []roles.Entry {
	return append([]roles.Entry(nil), entries...)
}

// withActorEntries is doc with the actor's candidate list replaced: a copied
// map, so the next key builds on the list this one wrote (§4).
func withActorEntries(doc relevo.ConfigDoc, name string, entries []roles.Entry) relevo.ConfigDoc {
	acts := make(map[string]roles.Actor, len(doc.Actors))
	for k, a := range doc.Actors {
		acts[k] = a
	}
	a := acts[name]
	a.Candidates = entries
	acts[name] = a
	doc.Actors = acts
	return doc
}

// change is every entry change's one path (§4): validate next with
// SetActorEntries, update the local doc, then apply the edit. A refused edit
// is a notice.
func (v actorView) change(env Env, next []roles.Entry) (View, tea.Cmd) {
	edit, err := relevo.SetActorEntries(v.doc, v.name, next)
	if err != nil {
		return v, notice(err.Error())
	}
	v.doc = withActorEntries(v.doc, v.name, next)
	return v, runAction(env.Ctx, "edit actor", "actor:"+v.name, func(ctx context.Context) Result {
		return env.Actions.ApplyConfig(ctx, edit)
	})
}

// pickCmd is the a key (§4): a listBox of the doc's candidates not already in
// this actor's list, in stored order.
func (v actorView) pickCmd(env Env) tea.Cmd {
	items := actorPickItems(v.doc, v.name, env)
	if len(items) == 0 {
		return notice("every candidate is already in " + v.name + "'s list")
	}
	n := len(v.doc.Actors[v.name].Candidates)
	return openOverlay(listBox{
		kind:   "add to " + v.name,
		submit: "add as " + actorOrdinal(n+1),
		items:  items,
		sel:    0,
		onPick: func(name string) tea.Cmd {
			next := append(copyActorEntries(v.doc.Actors[v.name].Candidates), roles.Entry{Candidate: name})
			_, cmd := v.change(env, next)
			return cmd
		},
	})
}

// actorPickItems is the picker's rows (§4): each candidate not already in the
// actor's list, its status the entry's own, its note `harness · provider`.
func actorPickItems(doc relevo.ConfigDoc, name string, env Env) []listItem {
	inList := make(map[string]bool, len(doc.Actors[name].Candidates))
	for _, e := range doc.Actors[name].Candidates {
		if n := candName(doc, e.Candidate); n != "" {
			inList[n] = true
		}
	}
	var out []listItem
	for _, c := range doc.Candidates {
		if inList[c.Name] {
			continue
		}
		text, style := entryStatus(doc, roles.Entry{Candidate: c.Name}, env.Report.Gated, env.Now)
		out = append(out, listItem{
			name:        c.Name,
			status:      text,
			statusStyle: style,
			note:        c.Harness + " · " + c.Provider,
		})
	}
	return out
}

// actorOrdinal is an ordinal word with its suffix (§4): 1st, 2nd, 3rd, 7th,
// 11th, 21st.
func actorOrdinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}
