package view

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/pathscope"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// FormatActors renders the actors section for `relevo config`: one two-line
// block per actor in name order, then one line per custom or native agent
// when the agents section is non-empty.
//
// It is plain text, never an escape code: `relevo config` is read through a
// pipe. It returns "" when there are no actors, which is how the caller knows
// to fall back to the legacy roles block.
func FormatActors(L config.Loaded, reg *roles.Registry) string {
	if len(L.Actors) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, name := range slices.Sorted(maps.Keys(L.Actors)) {
		sb.WriteString(formatActor(L, reg, name, L.Actors[name]))
		sb.WriteString("\n")
	}

	if len(L.Agents) > 0 {
		sb.WriteString("\nagents\n")
		for _, name := range slices.Sorted(maps.Keys(L.Agents)) {
			sb.WriteString(formatActorAgent(name, L.Agents[name]))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// formatActor renders one actor's lines, without the trailing newline.
func formatActor(L config.Loaded, reg *roles.Registry, name string, a roles.Actor) string {
	shape := "reader"
	if reg != nil {
		if role, ok := reg.Role(name); ok && role.Shape == harness.ShapeBuilder {
			shape = "writer"
		}
	}

	kind := "custom"
	if _, shipped := roles.Shipped(a.Agent); shipped {
		kind = "shipped"
	}

	tier := a.Tier
	if tier == "" {
		tier = "-"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s  %s  %s  tier %s", name, a.Agent, shape, kind, tier)
	if shape == "writer" {
		if actorCheckOn(a.Check) {
			b.WriteString("  check on")
		} else {
			b.WriteString("  check off")
		}
	}
	b.WriteString("\n  candidates  " + actorCandidates(L, a))
	if len(a.Placement) > 0 {
		b.WriteString("\n  placement  " + strings.Join(a.Placement, ", "))
	}
	if a.Scope != nil {
		b.WriteString("\n  scope  " + ScopeText(a.Scope))
	}
	return b.String()
}

// ScopeText renders a writer's scope for the listings.
func ScopeText(scope *pathscope.Scope) string {
	parts := strings.Join(scope.Paths, ", ")
	if scope.Comments {
		if parts == "" {
			return "comments"
		}
		parts += " + comments"
	}
	return parts
}

// actorCheckOn renders an actor's check: nil means on for a writer.
func actorCheckOn(check *bool) bool {
	return check == nil || *check
}

// actorCandidates renders an actor's candidates, "(none)" when the list is
// empty.
func actorCandidates(L config.Loaded, a roles.Actor) string {
	if len(a.Candidates) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(a.Candidates))
	for _, e := range a.Candidates {
		name := e.Candidate
		if L.Candidates != nil {
			name = L.Candidates.NameOf(e.Candidate)
		}
		if e.Off {
			name += " (off)"
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

// formatActorAgent renders one agents-section entry's line, without the
// trailing newline.
func formatActorAgent(name string, entry roles.AgentEntry) string {
	if entry.Source != "" {
		if src, err := agentsrc.Parse([]byte(entry.Source)); err == nil {
			return fmt.Sprintf("  %s  %s  output %s  kinds %s",
				name, string(src.Shape), src.Output, strings.Join(agentsrc.RenderedKinds(src), ", "))
		}
		return "  " + name + "  custom"
	}

	kinds := make([]string, 0, len(entry.Native))
	for kind := range entry.Native {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return fmt.Sprintf("  %s  %s  native  kinds %s", name, entry.Shape, strings.Join(kinds, ", "))
}
