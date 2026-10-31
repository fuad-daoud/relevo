package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/pathscope"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

type actorView struct {
	name     string
	doc      relevo.ConfigDoc
	err      error
	loaded   bool
	cur      int
	placeCur int
	focus    int
	top      int
}

func newActorView(env widget.Env, doc relevo.ConfigDoc, name string) (widget.View, tea.Cmd) {
	v := actorView{name: name, doc: doc, loaded: true}
	return v, candDocCmd(env)
}

// A nil check counts as on.
func actorCheckOn(check *bool) bool { return check == nil || *check }

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

func actorPickHeaderLine(nameW int, cols []actorCol, cw int) string {
	style := widget.FaintStyle.Bold(true)
	cells := []candCell{
		{text: fmt.Sprintf("%2s", "#"), style: style},
		{text: widget.Pad("CANDIDATE", nameW), style: style},
	}
	for _, c := range cols {
		cells = append(cells, candCell{text: widget.Pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

func actorPickLine(doc relevo.ConfigDoc, env widget.Env, name string, i int, sel bool, nameW int, cols []actorCol, cw int) string {
	a := doc.Actors[name]
	e := a.Candidates[i]
	base, meta := widget.TextStyle, widget.MutedStyle
	if e.Off {
		base, meta = widget.FaintStyle, widget.FaintStyle
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
		{text: widget.Pad(cname, nameW), style: nameStyle},
		{text: widget.Pad(c.Harness, 8), style: meta},
		{text: widget.Pad(c.Provider, 10), style: meta},
		{text: widget.Pad(text, 20), style: style},
	}
	return candLine(cells, sel, cw)
}

func actorCheckLine(doc relevo.ConfigDoc, name string, width int) (string, bool) {
	a := doc.Actors[name]
	if agentShapeOf(doc, a.Agent) != string(agentsrc.ShapeWriter) {
		return "", false
	}
	if !actorCheckOn(a.Check) {
		return "   " + widget.MutedStyle.Render("no check after each round"), true
	}
	if cmd := doc.Policy.GateDefault(); cmd != "" {
		return "   " + widget.MutedStyle.Render("runs ") + widget.TextStyle.Render(cmd) +
			widget.MutedStyle.Render(" after each round"), true
	}
	return "   " + widget.MutedStyle.Render("check after each round is on, but no check command is set; set one in ") +
		widget.TextStyle.Render(":settings"), true
}

func actorScopeText(scope *pathscope.Scope) string {
	parts := strings.Join(scope.Paths, ", ")
	if scope.Comments {
		if parts == "" {
			return "comments"
		}
		parts += " + comments"
	}
	return parts
}

func (v actorView) bodyLines(env widget.Env, width int) []string {
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
	lines = append(lines, "")
	lines = append(lines, actorPlacementLines(v.doc, v.name, v.placementFocus(), v.placeCur, width)...)
	lines = append(lines, "", "")
	if check, ok := actorCheckLine(v.doc, v.name, width); ok {
		lines = append(lines, widget.Fit(check, width))
	}
	if scope := a.Scope; scope != nil {
		lines = append(lines, widget.Fit("   "+widget.MutedStyle.Render("scope ")+widget.TextStyle.Render(actorScopeText(scope)), width))
	}
	return lines
}

func (v *actorView) follow(env widget.Env) {
	avail := widget.BodyHeight(env)
	if avail <= 0 {
		v.top = 0
		return
	}
	lines := v.bodyLines(env, env.Width)
	// The selected row is the focused pane's: the candidate rows start after
	// the blank line and the header, the placement rows after those rows, a
	// blank, and the PLACEMENT header.
	sel := 2 + v.cur
	if v.placementFocus() {
		sel = len(v.doc.Actors[v.name].Candidates) + 4 + v.placeCur
	}
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+avail {
		v.top = sel - avail + 1
	}
	v.top = widget.Clamp(v.top, 0, max(0, len(lines)-avail))
}

func (v actorView) Crumbs() []string { return []string{v.name} }

// The view owns no text input of its own.
func (v actorView) Capturing() bool { return false }

// A placement entry has no on/off, so the placement pane offers no space key.
func (v actorView) Keys() []widget.KeyHelp {
	tab := widget.KeyHelp{Key: "tab", Help: "placement"}
	if v.placementFocus() {
		tab = widget.KeyHelp{Key: "tab", Help: "candidates"}
	}
	keys := []widget.KeyHelp{
		{Key: "↑↓", Help: "move"},
		{Key: "shift+↑↓", Help: "reorder"},
	}
	if !v.placementFocus() {
		keys = append(keys, widget.KeyHelp{Key: "space", Help: "on / off"})
	}
	return append(keys,
		widget.KeyHelp{Key: "a", Help: "add"},
		widget.KeyHelp{Key: "d", Help: "remove"},
		widget.KeyHelp{Key: "e", Help: "edit"},
		tab,
		widget.KeyHelp{Key: "esc", Help: "back"},
	)
}

// Both panes' keys, so the overlay documents the pane that does not own them right now too.
func (v actorView) HelpKeys() []widget.KeyHelp {
	return []widget.KeyHelp{
		{Key: "↑↓", Help: "move"},
		{Key: "shift+↑↓", Help: "reorder"},
		{Key: "space", Help: "on / off"},
		{Key: "a", Help: "add"},
		{Key: "d", Help: "remove"},
		{Key: "e", Help: "edit"},
		{Key: "tab", Help: "switch list"},
		{Key: "esc", Help: "back"},
	}
}

func (v actorView) Context(env widget.Env) (string, string) {
	a := v.doc.Actors[v.name]
	tier, tierStyle := a.Tier, widget.TextStyle
	if tier == "" {
		tier, tierStyle = "—", widget.FaintStyle
	}
	np, nextStyle := nextPick(v.doc, v.name, env.Report.Gated), widget.GreenStyle
	if np == "" {
		np, nextStyle = "—", widget.FaintStyle
	}
	left := "   " + widget.TextStyle.Render(a.Agent) + widget.MutedStyle.Render(" · "+agentShapeOf(v.doc, a.Agent)+"   ") +
		widget.FaintStyle.Render("tier ") + tierStyle.Render(tier) +
		"   " + widget.TextStyle.Bold(true).Render(fmt.Sprintf("%d", len(a.Candidates))) +
		widget.MutedStyle.Render(" candidates   ") + widget.FaintStyle.Render("next pick ") + nextStyle.Render(np)
	return left, ""
}

func (v actorView) Body(env widget.Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if !v.loaded && v.err != nil {
		return strings.Join(widget.Centered(v.err.Error(), width, height), "\n")
	}
	if !v.loaded {
		return strings.Join(widget.BlockLines([]string{"loading…"}, width, height), "\n")
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

func (v actorView) Update(msg tea.Msg, env widget.Env) (widget.View, tea.Cmd) {
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
		v.placeCur = candClamp(v.placeCur, len(v.doc.Actors[v.name].Placement))
		return v, nil

	case statusMsg:
		// A refresh from anywhere re-reads the config, which is what
		// reloads the table after ApplyConfig's Refresh.
		return v, candDocCmd(env)

	case tea.KeyMsg:
		return v.updateKey(msg, env)
	}
	return v, nil
}

// While an edit of this actor is in flight, every change key is ignored -- in
// both panes, since either one would build a second edit on a list the first
// has not written yet. tab changes nothing on its own, so it still moves the
// focus.
func (v actorView) updateKey(k tea.KeyMsg, env widget.Env) (widget.View, tea.Cmd) {
	entries := v.doc.Actors[v.name].Candidates
	n := len(entries)

	if env.Running["actor:"+v.name] != "" {
		switch k.String() {
		case "shift+up", "shift+down", " ", "space", "d", "a", "e":
			return v, nil
		}
	}

	if k.String() == "tab" {
		if v.placementFocus() {
			v.focus = focusCandidates
		} else {
			v.focus = focusPlacement
		}
		v.follow(env)
		return v, nil
	}
	if v.placementFocus() {
		return v.updatePlacementKey(k, env)
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
		v.cur = candClamp(v.cur-widget.BodyHeight(env), n)
		v.follow(env)
	case "pgdown":
		v.cur = candClamp(v.cur+widget.BodyHeight(env), n)
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

// A change builds its own list, so the doc the view holds is never mutated in
// place.
func copyActorEntries(entries []roles.Entry) []roles.Entry {
	return append([]roles.Entry(nil), entries...)
}

// A copied map, so the next key builds on the list this one wrote.
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

func (v actorView) change(env widget.Env, next []roles.Entry) (widget.View, tea.Cmd) {
	edit, err := relevo.SetActorEntries(v.doc, v.name, next)
	if err != nil {
		return v, notice(err.Error())
	}
	v.doc = withActorEntries(v.doc, v.name, next)
	return v, runAction(env.Ctx, "edit actor", "actor:"+v.name, func(ctx context.Context) widget.Result {
		return env.Actions.ApplyConfig(ctx, edit)
	})
}

// A listBox of the doc's candidates not already in this actor's list, in
// stored order.
func (v actorView) pickCmd(env widget.Env) tea.Cmd {
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

func actorPickItems(doc relevo.ConfigDoc, name string, env widget.Env) []listItem {
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
