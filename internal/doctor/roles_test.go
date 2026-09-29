package doctor

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestBindingRoleChecks pins that a binding whose role roles.json no longer
// defines gets one FAIL row naming the binding and the role, a builder
// binding ("") is always defined, and a DONE binding is not reported.
func TestBindingRoleChecks(t *testing.T) {
	known := func(role string) bool { return role == "builder" || role == "ui-builder" }
	bindings := []store.Binding{
		{Name: "a"}, // Role "" -> builder, known
		{Name: "b", Role: "ui-builder"},
		{Name: "c", Role: "gone"},
		{Name: "d", Role: "gone", State: store.StateDone},
		{Name: "e", Role: "gone", Builder: store.Endpoint{Mode: store.ModeRemote}},
	}

	got := BindingRoleChecks(bindings, known)
	if len(got) != 1 {
		t.Fatalf("BindingRoleChecks = %d rows (%+v), want exactly 1", len(got), got)
	}
	row := got[0]
	if row.Severity != SevFail {
		t.Errorf("row.Severity = %v, want SevFail", row.Severity)
	}
	if row.Name != "binding actor" {
		t.Errorf("row.Name = %q, want binding actor", row.Name)
	}
	if !strings.Contains(row.Detail, `binding c runs actor "gone"`) {
		t.Errorf("row.Detail = %q, want it to name binding c and actor gone", row.Detail)
	}
}
