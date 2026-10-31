package relevo

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// CandidateSlots returns every actor slot that references the candidate named
// name, by name or token, sorted by actor name.
func CandidateSlots(d ConfigDoc, name string) []ActorSlot {
	token := candidateToken(d, name)
	var slots []ActorSlot
	for _, actorName := range sortedActorNames(d.Actors) {
		for i, e := range d.Actors[actorName].Candidates {
			if e.Candidate == name || (token != "" && e.Candidate == token) {
				slots = append(slots, ActorSlot{Actor: actorName, Position: i + 1, Off: e.Off})
			}
		}
	}
	return slots
}

// SetActorEntries replaces actor's candidate list with entries. It covers
// reorder, on/off, add and remove in one call.
func SetActorEntries(d ConfigDoc, actor string, entries []roles.Entry) (ConfigEdit, error) {
	a, ok := d.Actors[actor]
	if !ok {
		return ConfigEdit{}, &FieldError{"", "no actor named " + actor}
	}
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !resolvesToCandidate(d, e.Candidate) {
			return ConfigEdit{}, &FieldError{"", "no candidate named " + e.Candidate}
		}
		if seen[e.Candidate] {
			return ConfigEdit{}, &FieldError{"", "duplicate candidate " + e.Candidate}
		}
		seen[e.Candidate] = true
	}

	a.Candidates = append([]roles.Entry(nil), entries...)
	acts := copyActors(d.Actors)
	acts[actor] = a
	return actorEdit(d, acts, actor, "edit actor "+actor+" candidates")
}

// SetActorPlacement replaces actor's ordered placement list with entries. It
// covers reorder, add and remove in one call.
//
// Empty entries clear the field rather than storing ["local"]: the absent
// field is what means ["local"] on the next load, so clearing leaves the
// actor at the default instead of pinning that default into the config.
//
// The unknown-server refusal lives here rather than in the store's own
// cross-check (internal/config/placement.go) so the cockpit can show a human
// notice; the cross-check stays the backstop on every write path.
func SetActorPlacement(d ConfigDoc, actor string, entries []string) (ConfigEdit, error) {
	a, ok := d.Actors[actor]
	if !ok {
		return ConfigEdit{}, &FieldError{"", "no actor named " + actor}
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry == "" {
			return ConfigEdit{}, &FieldError{"", "placement entry is empty"}
		}
		if seen[entry] {
			return ConfigEdit{}, &FieldError{"", "duplicate placement " + entry}
		}
		seen[entry] = true
		if entry == "local" {
			continue
		}
		if _, ok := d.Servers[entry]; !ok {
			return ConfigEdit{}, &FieldError{"", "no server named " + entry}
		}
	}

	a.Placement = append([]string(nil), entries...)
	acts := copyActors(d.Actors)
	acts[actor] = a
	return actorEdit(d, acts, actor, "edit actor "+actor+" placement")
}

// EditActor sets actor's agent, tier and check. A reader agent stores no
// check, whatever check says.
func EditActor(d ConfigDoc, actor, agent, tier string, check bool) (ConfigEdit, error) {
	a, ok := d.Actors[actor]
	if !ok {
		return ConfigEdit{}, &FieldError{"", "no actor named " + actor}
	}
	if _, shipped := roles.Shipped(agent); !shipped {
		if _, ok := d.Agents[agent]; !ok {
			return ConfigEdit{}, &FieldError{"agent", "no agent named " + agent}
		}
	}
	if tier != "" {
		if _, err := harness.ParseTier(tier); err != nil {
			return ConfigEdit{}, &FieldError{"tier", err.Error()}
		}
	}
	shape, err := AgentShape(d, agent)
	if err != nil {
		return ConfigEdit{}, err
	}

	a.Agent = agent
	a.Tier = tier
	if shape == string(agentsrc.ShapeReader) {
		a.Check = nil
		a.Scope = nil
	} else {
		a.Check = &check
	}
	acts := copyActors(d.Actors)
	acts[actor] = a
	return actorEdit(d, acts, actor, "edit actor "+actor)
}

// AddActor creates an actor with no candidates, no tier and no check.
func AddActor(d ConfigDoc, name, agent string) (ConfigEdit, error) {
	body, err := roles.EncodeActors(map[string]roles.Actor{name: {Agent: agent}})
	if err != nil {
		return ConfigEdit{}, err
	}
	if _, _, err := roles.ParseActors(body); err != nil {
		return ConfigEdit{}, &FieldError{"", err.Error()}
	}
	if _, taken := d.Actors[name]; taken {
		return ConfigEdit{}, &FieldError{"", "actor " + name + " already exists"}
	}
	if _, shipped := roles.Shipped(agent); !shipped {
		if _, ok := d.Agents[agent]; !ok {
			return ConfigEdit{}, &FieldError{"agent", "no agent named " + agent}
		}
	}

	acts := copyActors(d.Actors)
	acts[name] = roles.Actor{Agent: agent}
	return actorEdit(d, acts, name, "add actor "+name)
}

// DeleteActor removes the actor named name. A builtin actor name is refused.
func DeleteActor(d ConfigDoc, name string) (ConfigEdit, error) {
	if _, ok := harness.RoleByName(name); ok {
		return ConfigEdit{}, &FieldError{"", name + " is built in; it can't be deleted"}
	}
	if _, ok := d.Actors[name]; !ok {
		return ConfigEdit{}, &FieldError{"", "no actor named " + name}
	}

	acts := copyActors(d.Actors)
	delete(acts, name)
	return actorEdit(d, acts, name, "delete actor "+name)
}

// DeleteAgent removes the custom agent named name. A shipped agent and an
// agent any actor uses are both refused. Only the agents section changes.
func DeleteAgent(d ConfigDoc, name string) (ConfigEdit, error) {
	if _, shipped := roles.Shipped(name); shipped {
		return ConfigEdit{}, &FieldError{"", name + " ships with relevo; it can't be deleted"}
	}
	var users []string
	for _, actorName := range sortedActorNames(d.Actors) {
		if d.Actors[actorName].Agent == name {
			users = append(users, actorName)
		}
	}
	if len(users) > 0 {
		return ConfigEdit{}, &FieldError{"", "used by " + strings.Join(users, ", ") + "; point it at another agent in :actors first"}
	}
	if _, ok := d.Agents[name]; !ok {
		return ConfigEdit{}, &FieldError{"", "no agent named " + name}
	}

	agents := make(map[string]roles.AgentEntry, len(d.Agents))
	for k, v := range d.Agents {
		if k == name {
			continue
		}
		agents[k] = v
	}
	body, err := roles.EncodeAgents(agents)
	if err != nil {
		return ConfigEdit{}, err
	}
	sections := map[config.Section]json.RawMessage{config.Agents: body}
	if err := dryRun(d.Candidates, d.Actors, agents, d.Policy); err != nil {
		return ConfigEdit{}, err
	}
	return ConfigEdit{Sections: sections, Message: "delete agent " + name, Name: name}, nil
}

// actorEdit builds an actors-only edit from acts and dry-runs it.
func actorEdit(d ConfigDoc, acts map[string]roles.Actor, name, message string) (ConfigEdit, error) {
	body, err := roles.EncodeActors(acts)
	if err != nil {
		return ConfigEdit{}, err
	}
	sections := map[config.Section]json.RawMessage{config.Actors: body}
	if err := dryRun(d.Candidates, acts, d.Agents, d.Policy); err != nil {
		return ConfigEdit{}, err
	}
	return ConfigEdit{Sections: sections, Message: message, Name: name}, nil
}

// copyActors returns a shallow copy of m: the map is fresh, the Actor values
// are shared until a caller replaces one.
func copyActors(m map[string]roles.Actor) map[string]roles.Actor {
	out := make(map[string]roles.Actor, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// sortedActorNames returns m's keys, sorted.
func sortedActorNames(m map[string]roles.Actor) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
