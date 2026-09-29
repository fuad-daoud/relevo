package roles

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/harness"
)

// FromActors converts the agents and actors sections into today's File,
// one row per actor (A2 §4.1). It is the bridge round 1 uses to keep every
// consumer of *Registry working unchanged.
//
// Errors wrap ErrBadRoles, so config.Load handles them exactly as it
// treats a bad roles file. The warnings name every agents entry no actor uses.
func FromActors(agents map[string]AgentEntry, actors map[string]Actor) (*File, []string, error) {
	rows := make(map[string]Row, len(actors))
	used := make(map[string]bool, len(agents))

	for _, name := range sortedKeys(actors) {
		row, err := rowFor(name, actors[name], agents)
		if err != nil {
			return nil, nil, err
		}
		used[actors[name].Agent] = true
		rows[name] = row
	}

	var warnings []string
	for _, name := range sortedKeys(agents) {
		if !used[name] {
			warnings = append(warnings, fmt.Sprintf("agent %s is not used by any actor", name))
		}
	}
	return &File{Rows: rows, Source: SourceActors}, warnings, nil
}

// rowFor builds one actor's Row.
func rowFor(name string, actor Actor, agents map[string]AgentEntry) (Row, error) {
	shape, defs, err := agentFor(actor.Agent, agents)
	if err != nil {
		return Row{}, fmt.Errorf("actor %s: %v: %w", name, err, ErrBadRoles)
	}

	// A builtin actor name keeps its builtin shape; a shape that would change
	// what relevo's loop does with the actor is refused.
	if builtin, ok := harness.RoleByName(name); ok {
		if want := shapeWordFor(builtin.Shape); shape != want {
			return Row{}, fmt.Errorf("actor %s must run a %s agent: %w", name, want, ErrBadRoles)
		}
	}

	// check is a writer's gate; a reader has none.
	if actor.Check != nil && *actor.Check && shape == string(agentsrc.ShapeReader) {
		return Row{}, fmt.Errorf("actor %s: check is only for a writer agent: %w", name, ErrBadRoles)
	}

	rowShape := shape
	row := Row{
		Shape:       &rowShape,
		Definitions: defs,
	}
	for _, e := range actor.Candidates {
		row.Candidates = append(row.Candidates, e.Candidate)
		if e.Off {
			row.Off = append(row.Off, e.Candidate)
		}
	}
	if actor.Tier != "" {
		tier := actor.Tier
		row.Tier = &tier
	}
	if actor.Check != nil {
		row.Check = actor.Check
	}
	return row, nil
}

// agentFor resolves one agent name to its shape word ("writer"/"reader") and
// its definition for every kind it renders. A shipped agent uses the shipped
// table, a source agent its agentsrc shape and rendered kinds, and a native
// agent its own map.
func agentFor(name string, agents map[string]AgentEntry) (string, map[string]DefRow, error) {
	if shipped, ok := Shipped(name); ok {
		defs := make(map[string]DefRow, len(harness.All()))
		for _, h := range harness.All() {
			defs[h.Kind] = DefRow{
				Agent:    shipped.Name,
				Requires: append([]string(nil), shipped.Requires...),
			}
		}
		return string(shipped.Shape), defs, nil
	}

	entry, ok := agents[name]
	if !ok {
		return "", nil, fmt.Errorf("agent %q is not shipped and not in agents", name)
	}

	if entry.Source != "" {
		src, err := agentsrc.Parse([]byte(entry.Source))
		if err != nil {
			return "", nil, fmt.Errorf("agent %q: %v", name, err)
		}
		defs := make(map[string]DefRow)
		for _, kind := range agentsrc.RenderedKinds(src) {
			defs[kind] = DefRow{
				Agent:    src.Name,
				Requires: append([]string(nil), src.Requires...),
			}
		}
		return string(src.Shape), defs, nil
	}

	defs := make(map[string]DefRow, len(entry.Native))
	for kind, d := range entry.Native {
		defs[kind] = DefRow{
			Agent:    d.Agent,
			Requires: append([]string(nil), d.Requires...),
		}
	}
	return entry.Shape, defs, nil
}

// shapeWordFor renders a harness role shape as the word roles.json uses, the
// same mapping internal/roles applies.
func shapeWordFor(shape harness.RoleShape) string {
	switch shape {
	case harness.ShapeBuilder:
		return "writer"
	case harness.ShapeConsult:
		return "reader"
	}
	return string(shape)
}
