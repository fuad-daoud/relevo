package relevo

import (
	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// defaultOutput words a reader's prompt when its agent names no output label.
const defaultOutput = "notes"

// ActorOutput is the output label of the agent actor plays: the shipped
// agent's label, else a custom source agent's `output:`, else defaultOutput.
func ActorOutput(actors map[string]roles.Actor, agents map[string]roles.AgentEntry, actor, definition string) string {
	agent := definition
	if a, ok := actors[actor]; ok {
		agent = a.Agent
	}
	if shipped, ok := roles.Shipped(agent); ok {
		return shipped.Output
	}
	if entry, ok := agents[agent]; ok && entry.Source != "" {
		if src, err := agentsrc.Parse([]byte(entry.Source)); err == nil && src.Output != "" {
			return src.Output
		}
	}
	return defaultOutput
}

// actorOutput loads the config for ActorOutput; a missing or unreadable config
// falls back to the shipped labels, because the label only words the prompt.
func actorOutput(rt Runtime, actor, definition string) string {
	if rt.Config == nil {
		return ActorOutput(nil, nil, actor, definition)
	}
	loaded, err := rt.Config.Load()
	if err != nil {
		return ActorOutput(nil, nil, actor, definition)
	}
	return ActorOutput(loaded.Actors, loaded.Agents, actor, definition)
}

// readerOutputLabel is the output label of the agent a binding's actor plays:
// the agent definition when the role's spec resolves, else the actor name.
// The prompt that words the reader's artifact directory and the close line
// that names the artifact both call this, so the two labels cannot drift.
func readerOutputLabel(rt Runtime, b store.Binding) string {
	actor := bindingRole(b)
	definition := actor
	if spec, err := bindingSpec(rt, b, b.Builder.Kind); err == nil {
		definition = spec.Definition
	}
	return actorOutput(rt, actor, definition)
}
