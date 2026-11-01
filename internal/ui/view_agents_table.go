package ui

import (
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

type agentRow struct {
	name   string
	shape  string
	source string
	usedBy []string
	files  []harness.AgentFile
}

func agentNames(doc relevo.ConfigDoc) []string {
	shipped := roles.ShippedAgents()
	names := make([]string, 0, len(shipped)+len(doc.Agents))
	for _, s := range shipped {
		names = append(names, s.Name)
	}
	custom := make([]string, 0, len(doc.Agents))
	for name := range doc.Agents {
		custom = append(custom, name)
	}
	sort.Strings(custom)
	return append(names, custom...)
}

func agentRows(doc relevo.ConfigDoc, files map[string][]harness.AgentFile) []agentRow {
	use := agentUse(doc)
	names := agentNames(doc)
	out := make([]agentRow, 0, len(names))
	for _, name := range names {
		r := agentRow{name: name, usedBy: use[name]}
		if s, ok := roles.Shipped(name); ok {
			r.shape, r.source, r.files = string(s.Shape), "shipped", files[name]
		} else {
			r.shape, r.source = agentShapeOf(doc, name), "custom"
			if relevo.IsSourceAgent(doc.Agents, name) {
				r.files = files[name]
			}
		}
		out = append(out, r)
	}
	return out
}

func agentUse(doc relevo.ConfigDoc) map[string][]string {
	seen := make(map[string]map[string]bool, len(doc.Actors))
	add := func(agent, actor string) {
		if agent == "" {
			return
		}
		if seen[agent] == nil {
			seen[agent] = map[string]bool{}
		}
		seen[agent][actor] = true
	}
	for _, actor := range actorOrder(doc.Actors) {
		agent := doc.Actors[actor].Agent
		add(agent, actor)
		if s, ok := roles.Shipped(agent); ok {
			for _, req := range s.Requires {
				add(req, actor)
			}
		}
	}
	out := make(map[string][]string, len(seen))
	for agent, users := range seen {
		names := make([]string, 0, len(users))
		for actor := range users {
			names = append(names, actor)
		}
		sort.Strings(names)
		out[agent] = names
	}
	return out
}

func agentKinds(files map[string][]harness.AgentFile) []string {
	present := map[string]bool{}
	for _, fs := range files {
		for _, f := range fs {
			present[f.Kind] = true
		}
	}
	var out []string
	for _, h := range harness.All() {
		if present[h.Kind] {
			out = append(out, h.Kind)
		}
	}
	return out
}

func agentListLayout(width int, kinds []string) (int, []candCol) {
	cw := width - 6
	if cw < 0 {
		cw = 0
	}
	visible := []candCol{
		{"shape", "SHAPE", 6},
		{"source", "SOURCE", 7},
		{"used", "USED BY", 19},
	}
	for _, kind := range kinds {
		visible = append(visible, candCol{"kind:" + kind, strings.ToUpper(kind), 8})
	}
	agentW := func(cols []candCol) int {
		fixed := 2 // the two spaces after AGENT
		for i, c := range cols {
			fixed += c.w
			if i < len(cols)-1 {
				fixed += 2
			}
		}
		return cw - fixed
	}
	for _, drop := range []string{"used", "source"} {
		if agentW(visible) >= 14 {
			break
		}
		for i, c := range visible {
			if c.key == drop {
				visible = append(append([]candCol(nil), visible[:i]...), visible[i+1:]...)
				break
			}
		}
	}
	return agentW(visible), visible
}

func agentHeaderLine(nameW int, cols []candCol, cw int) string {
	style := widget.FaintStyle.Bold(true)
	cells := []candCell{{text: widget.Pad("AGENT", nameW), style: style}}
	for _, c := range cols {
		cells = append(cells, candCell{text: widget.Pad(c.head, c.w), style: style})
	}
	return candLine(cells, false, cw)
}

func agentStateCell(r agentRow, kind string) (string, lipgloss.Style) {
	for _, f := range r.files {
		if f.Kind != kind {
			continue
		}
		switch f.State {
		case harness.FileUpToDate:
			return "ok", widget.GreenStyle
		case harness.FileStale:
			return "stale", widget.WarnStyle
		case harness.FileEdited, harness.FileEditedNewer:
			return "edited", widget.WarnStyle
		}
		break
	}
	return "·", widget.FaintStyle
}

func agentUsedCell(r agentRow) string {
	if len(r.usedBy) == 0 {
		return "no actor"
	}
	return strings.Join(r.usedBy, ", ")
}

func agentUsedText(r agentRow) string {
	if len(r.usedBy) == 0 {
		return "no actor uses it"
	}
	return "used by " + strings.Join(r.usedBy, ", ")
}

func agentDataLine(r agentRow, sel bool, nameW int, cols []candCol, cw int) string {
	base, meta := widget.TextStyle, widget.MutedStyle
	if len(r.usedBy) == 0 {
		base, meta = widget.FaintStyle, widget.FaintStyle
	}
	nameStyle := base
	if sel {
		nameStyle = nameStyle.Bold(true)
	}
	cells := []candCell{{text: widget.Pad(r.name, nameW), style: nameStyle}}
	for _, col := range cols {
		text, style := "", meta
		switch col.key {
		case "shape":
			text = r.shape
		case "source":
			text = r.source
		case "used":
			text = agentUsedCell(r)
		default:
			text, style = agentStateCell(r, strings.TrimPrefix(col.key, "kind:"))
		}
		cells = append(cells, candCell{text: widget.Pad(text, col.w), style: style})
	}
	return candLine(cells, sel, cw)
}

func tildePath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return strings.Replace(path, home, "~", 1)
	}
	return path
}
