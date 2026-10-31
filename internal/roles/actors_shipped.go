package roles

import "github.com/fuad-daoud/relevo/internal/agentsrc"

// ShippedAgent is one agent relevo ships: its name, its shape and output
// label, and the helper agents it requires.
type ShippedAgent struct {
	Name     string
	Shape    agentsrc.Shape
	Output   string
	Requires []string
	// Skills are the skills an actor over this agent needs unless it declares
	// its own.
	Skills []string
}

// shippedAgents is relevo's shipped agent table. It must agree with
// harness.RoleByName's Definitions for the four roles the role table
// defines; TestShippedTableMatchesHarness pins that. architect is the
// mastermind's own definition: shipped, but not a roleTable entry.
var shippedAgents = []ShippedAgent{
	{Name: "plan-executor", Shape: agentsrc.ShapeWriter, Output: "report", Requires: []string{"researcher"}},
	{Name: "librarian", Shape: agentsrc.ShapeWriter, Output: "report"},
	{Name: "designer", Shape: agentsrc.ShapeWriter, Output: "report", Skills: []string{"design-runbooks"}},
	{Name: "reviewer", Shape: agentsrc.ShapeReader, Output: "findings"},
	{Name: "security-reviewer", Shape: agentsrc.ShapeReader, Output: "findings"},
	{Name: "researcher", Shape: agentsrc.ShapeReader, Output: "notes"},
	{Name: "architect", Shape: agentsrc.ShapeReader, Output: "plan"},
}

// EffectiveSkills returns an actor's effective skills: its declared skills when
// present, or its shipped agent's default. Both are copies.
func EffectiveSkills(agent string, declared []string) []string {
	if len(declared) > 0 {
		return append([]string(nil), declared...)
	}
	if a, ok := Shipped(agent); ok && len(a.Skills) > 0 {
		return append([]string(nil), a.Skills...)
	}
	return nil
}

// ShippedAgents returns relevo's shipped agents in table order, a copy the
// caller may keep.
func ShippedAgents() []ShippedAgent {
	return append([]ShippedAgent(nil), shippedAgents...)
}

// Shipped returns the shipped agent named name. ok is false for a name relevo
// does not ship.
func Shipped(name string) (ShippedAgent, bool) {
	for _, a := range shippedAgents {
		if a.Name == name {
			return a, true
		}
	}
	return ShippedAgent{}, false
}
