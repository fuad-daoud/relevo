package roles

import (
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/harness"
)

// TestShippedTableMatchesHarness pins that the shipped agent table agrees
// with harness.RoleByName's Definitions for the four roles the role table
// defines, and every shipped agent is a definition on every known kind.
func TestShippedTableMatchesHarness(t *testing.T) {
	for _, role := range []string{"builder", "librarian", "reviewer", "researcher"} {
		spec, ok := harness.RoleByName(role)
		if !ok {
			t.Fatalf("harness.RoleByName(%q) not found", role)
		}
		agent, ok := Shipped(spec.Definition)
		if !ok {
			t.Fatalf("Shipped(%q) not found, want the %s role's definition", spec.Definition, role)
		}

		wantShape := agentsrc.ShapeReader
		if spec.Shape == harness.ShapeBuilder {
			wantShape = agentsrc.ShapeWriter
		}
		if agent.Shape != wantShape {
			t.Errorf("%s shape = %q, want %q", agent.Name, agent.Shape, wantShape)
		}

		var wantRequires []string
		if len(spec.Definitions) > 1 {
			wantRequires = spec.Definitions[1:]
		}
		if !reflect.DeepEqual(agent.Requires, wantRequires) {
			t.Errorf("%s requires = %v, want %v", agent.Name, agent.Requires, wantRequires)
		}
	}

	// Every shipped agent is a definition on every known kind, and its output
	// label is the one the shipped table names.
	wantOutput := map[string]string{
		"plan-executor":     "report",
		"librarian":         "report",
		"designer":          "report",
		"reviewer":          "findings",
		"security-reviewer": "findings",
		"researcher":        "notes",
		"architect":         "plan",
	}
	for name, output := range wantOutput {
		agent, ok := Shipped(name)
		if !ok {
			t.Fatalf("Shipped(%q) not found", name)
		}
		if agent.Output != output {
			t.Errorf("%s output = %q, want %q", name, agent.Output, output)
		}
		for _, h := range harness.All() {
			if !harness.IsShipped(h.Kind, name) {
				t.Errorf("%s is not shipped on %s", name, h.Kind)
			}
		}
	}

	designer, ok := Shipped("designer")
	if !ok {
		t.Fatal("Shipped(designer) not found")
	}
	if designer.Shape != agentsrc.ShapeWriter {
		t.Errorf("designer shape = %q, want %q", designer.Shape, agentsrc.ShapeWriter)
	}
	if len(designer.Requires) != 0 {
		t.Errorf("designer requires = %v, want none", designer.Requires)
	}

	if _, ok := Shipped("nonesuch"); ok {
		t.Error("Shipped(nonesuch) = true, want false")
	}
}
