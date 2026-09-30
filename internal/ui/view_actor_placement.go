package ui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// Which pane of the actor detail view owns the keys: the candidate table, or
// the placement list under it.
const (
	focusCandidates = 0
	focusPlacement  = 1
)

// placementLocal is the reserved sentinel for this machine: it is always a
// valid placement entry and needs no servers section to name it.
const placementLocal = "local"

// placementFocus reports whether the placement pane owns the keys.
func (v actorView) placementFocus() bool { return v.focus == focusPlacement }

// actorPlacementLines is the placement pane: the header and one row per entry,
// or the single line that stands for an absent list. The list is never empty
// as far as the actor is concerned, so an absent field reads as the default
// rather than as a zero-row table.
func actorPlacementLines(doc relevo.ConfigDoc, name string, focus bool, cur, width int) []string {
	entries := doc.Actors[name].Placement
	lines := []string{actorPlacementHeaderLine(width)}
	if len(entries) == 0 {
		return append(lines, actorPlacementDefaultLine(width))
	}
	cur = candClamp(cur, len(entries))
	for i := range entries {
		lines = append(lines, actorPlacementLine(doc, name, i, focus && i == cur, width))
	}
	return lines
}

// actorPlacementHeaderLine is the pane's header row, in faint bold, over the
// same two columns as its rows.
func actorPlacementHeaderLine(width int) string {
	style := faintStyle.Bold(true)
	cells := []candCell{
		{text: fmt.Sprintf("%2s", "#"), style: style},
		{text: "PLACEMENT", style: style},
	}
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	return candLine(cells, false, cw)
}

// actorPlacementLine is one entry's row: a faint number column and the name,
// the sentinel carrying the note that says what it means. Only the focused
// pane's cursor row is bold, so the pane that does not own the keys still
// shows where its cursor sits.
func actorPlacementLine(doc relevo.ConfigDoc, name string, i int, sel bool, width int) string {
	entry := doc.Actors[name].Placement[i]
	nameStyle := textStyle
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	cells := []candCell{
		{text: fmt.Sprintf("%2d", i+1), style: faintStyle},
		{text: entry, style: nameStyle},
	}
	if entry == placementLocal {
		cells = append(cells, candCell{text: "this machine", style: mutedStyle})
	}
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	return candLine(cells, false, cw)
}

// actorPlacementDefaultLine is the one line an absent placement list draws:
// the value the store reads back, marked as the default, muted throughout so
// it does not read as a row the keys act on.
func actorPlacementDefaultLine(width int) string {
	return fit("   "+mutedStyle.Render(placementLocal)+"  "+faintStyle.Render("default"), width)
}

// copyPlacement is a fresh copy of an actor's placement list: a change builds
// its own list, so the doc the view holds is never mutated in place.
func copyPlacement(entries []string) []string {
	return append([]string(nil), entries...)
}

// withActorPlacement is doc with the actor's placement list replaced: a copied
// map, so the next key builds on the list this one wrote.
func withActorPlacement(doc relevo.ConfigDoc, name string, entries []string) relevo.ConfigDoc {
	acts := make(map[string]roles.Actor, len(doc.Actors))
	for k, a := range doc.Actors {
		acts[k] = a
	}
	a := acts[name]
	a.Placement = copyPlacement(entries)
	acts[name] = a
	doc.Actors = acts
	return doc
}

// changePlacement is every placement change's one path, the candidate table's
// path over the placement list: validate next, update the local doc, then
// apply the edit, so the write, the in-flight guard and the log row are the
// same for both panes. A refused edit is a notice.
func (v actorView) changePlacement(env Env, next []string) (View, tea.Cmd) {
	edit, err := relevo.SetActorPlacement(v.doc, v.name, next)
	if err != nil {
		return v, notice(err.Error())
	}
	v.doc = withActorPlacement(v.doc, v.name, next)
	return v, runAction(env.Ctx, "edit actor", "actor:"+v.name, func(ctx context.Context) Result {
		return env.Actions.ApplyConfig(ctx, edit)
	})
}

// updatePlacementKey is the placement pane's keys. The list is ordered and
// most preferred first, so reorder is the edit that matters most; removing the
// last entry is allowed and clears the list back to the default, unlike the
// candidate table where an actor needs one candidate.
func (v actorView) updatePlacementKey(k tea.KeyMsg, env Env) (View, tea.Cmd) {
	entries := v.doc.Actors[v.name].Placement
	n := len(entries)

	switch k.String() {
	case "up":
		v.placeCur = candClamp(v.placeCur-1, n)
		v.follow(env)
	case "down":
		v.placeCur = candClamp(v.placeCur+1, n)
		v.follow(env)
	case "home":
		v.placeCur = 0
		v.follow(env)
	case "end":
		v.placeCur = candClamp(n-1, n)
		v.follow(env)
	case "pgup":
		v.placeCur = candClamp(v.placeCur-bodyHeight(env), n)
		v.follow(env)
	case "pgdown":
		v.placeCur = candClamp(v.placeCur+bodyHeight(env), n)
		v.follow(env)
	case "shift+up":
		if n < 2 || v.placeCur <= 0 {
			return v, nil
		}
		next := copyPlacement(entries)
		next[v.placeCur-1], next[v.placeCur] = next[v.placeCur], next[v.placeCur-1]
		v.placeCur--
		v.follow(env)
		return v.changePlacement(env, next)
	case "shift+down":
		if n < 2 || v.placeCur >= n-1 {
			return v, nil
		}
		next := copyPlacement(entries)
		next[v.placeCur], next[v.placeCur+1] = next[v.placeCur+1], next[v.placeCur]
		v.placeCur++
		v.follow(env)
		return v.changePlacement(env, next)
	case "d":
		if n == 0 {
			return v, nil
		}
		next := make([]string, 0, n-1)
		next = append(next, entries[:v.placeCur]...)
		next = append(next, entries[v.placeCur+1:]...)
		v.placeCur = candClamp(v.placeCur, len(next))
		v.follow(env)
		return v.changePlacement(env, next)
	case "a":
		return v, v.placementPickCmd(env)
	case "e":
		return v, openOverlay(newActorForm(env, v.doc, v.name))
	}
	return v, nil
}

// placementPickCmd is the a key here: a listBox of the placements the actor
// does not list yet. With nothing left to add a listBox would open on its
// candidates wording, so an empty list is a notice instead.
func (v actorView) placementPickCmd(env Env) tea.Cmd {
	items := placementPickItems(v.doc, v.name)
	if len(items) == 0 {
		return notice("every server is already in " + v.name + "'s placement")
	}
	return openOverlay(listBox{
		kind:   "placement",
		submit: "add",
		items:  items,
		sel:    0,
		onPick: func(entry string) tea.Cmd {
			next := append(copyPlacement(v.doc.Actors[v.name].Placement), entry)
			_, cmd := v.changePlacement(env, next)
			return cmd
		},
	})
}

// placementPickItems is the picker's rows: the local sentinel first, then the
// configured servers in name order, minus what the actor already lists. A
// server's only identification is its url, so that is its note.
func placementPickItems(doc relevo.ConfigDoc, name string) []listItem {
	listed := make(map[string]bool, len(doc.Actors[name].Placement))
	for _, entry := range doc.Actors[name].Placement {
		listed[entry] = true
	}
	var out []listItem
	if !listed[placementLocal] {
		out = append(out, listItem{name: placementLocal, note: "this machine"})
	}
	for _, server := range serverNames(doc.Servers) {
		if listed[server] {
			continue
		}
		out = append(out, listItem{name: server, note: doc.Servers[server].URL})
	}
	return out
}
