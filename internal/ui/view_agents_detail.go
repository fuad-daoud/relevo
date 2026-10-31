package ui

import (
	"os"
	"path/filepath"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

type agentFileRow struct {
	kind  string
	path  string
	model string
	state harness.FileState
}

// Relevo never writes a custom agent, but the harness looks at the kind's
// convention path.
func customAgentRows(doc relevo.ConfigDoc, name string) []agentFileRow {
	e, ok := doc.Agents[name]
	if !ok {
		return nil
	}
	path := func(kind, agent string) (string, bool) {
		rel, ok := harness.DefinitionPath(kind, agent)
		if !ok {
			return "", false
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, rel), true
		}
		return rel, true
	}
	var out []agentFileRow
	if e.Native != nil {
		for _, kind := range nativeAgentKinds(e) {
			if p, ok := path(kind, e.Native[kind].Agent); ok {
				out = append(out, agentFileRow{kind: kind, path: p})
			}
		}
		return out
	}
	src, err := agentsrc.Parse([]byte(e.Source))
	if err != nil {
		return nil
	}
	for _, kind := range agentsrc.RenderedKinds(src) {
		if p, ok := path(kind, name); ok {
			out = append(out, agentFileRow{kind: kind, path: p})
		}
	}
	return out
}

func nativeAgentKinds(e roles.AgentEntry) []string {
	var out []string
	for _, h := range harness.All() {
		if _, ok := e.Native[h.Kind]; ok {
			out = append(out, h.Kind)
		}
	}
	return out
}

func agentFileLine(kind, path, state, model string, pathW int) string {
	line := "   " + widget.MutedStyle.Render(widget.Pad(kind, 10)) + widget.Pad(path, pathW)
	if state != "" {
		line += widget.MutedStyle.Render(widget.Pad(state, 14))
	}
	if model != "" {
		line += widget.MutedStyle.Render("model " + model)
	}
	return line
}

func agentDetailLines(doc relevo.ConfigDoc, r agentRow, width int) []string {
	meta := r.source + " · " + r.shape + " · " + agentUsedText(r)
	first := "   " + widget.FaintStyle.Bold(true).Render(r.name) + "   " + widget.MutedStyle.Render(meta)
	out := []string{widget.Fit(first, width)}
	// pathW is measured over the file lines drawn below, then held for all of
	// them.
	pathW := 46
	grow := func(path string) {
		if w := lipgloss.Width(path) + 2; w > pathW {
			pathW = w
		}
	}
	if r.source == "custom" && !relevo.IsSourceAgent(doc.Agents, r.name) {
		rows := customAgentRows(doc, r.name)
		for _, f := range rows {
			grow(tildePath(f.path))
		}
		for _, f := range rows {
			out = append(out, widget.Fit(agentFileLine(f.kind, tildePath(f.path), "", "", pathW), width))
		}
		return out
	}
	for _, f := range r.files {
		grow(tildePath(f.Path))
	}
	for _, f := range r.files {
		model := ""
		if f.Model != "" {
			model = f.Model
		}
		out = append(out, widget.Fit(agentFileLine(f.Kind, tildePath(f.Path), string(f.State), model, pathW), width))
	}
	return out
}
