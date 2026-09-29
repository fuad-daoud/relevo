package roles

import "github.com/fuad-daoud/relevo/internal/agentsrc"

// ShippedAgent is one agent relevo ships: its name, its shape and output
// label, and the helper agents it requires.
type ShippedAgent struct {
	Name     string
	Shape    agentsrc.Shape
	Output   string
	Requires []string
}

// shippedAgents is relevo's shipped agent table (cockpit spec §3.2). It must
// agree with harness.RoleByName's Definitions for the three roles the role
// table defines; TestShippedTableMatchesHarness pins that. architect is the
// mastermind's own definition: shipped, but not a roleTable entry.
var shippedAgents = []ShippedAgent{
	{Name: "plan-executor", Shape: agentsrc.ShapeWriter, Output: "report", Requires: []string{"researcher"}},
	{Name: "reviewer", Shape: agentsrc.ShapeReader, Output: "findings"},
	{Name: "researcher", Shape: agentsrc.ShapeReader, Output: "notes"},
	{Name: "architect", Shape: agentsrc.ShapeReader, Output: "plan"},
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
