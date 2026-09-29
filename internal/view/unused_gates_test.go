package view

import (
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestHideDoneKeepsUnusedProviderGates(t *testing.T) {
	gate := ProviderGate{Provider: "antigravity", Since: baseTime, Source: "relevo", Binding: "oc-tui-a"}
	rep := Report{
		Bindings: []BindingStatus{
			{Name: "done-one", State: string(store.StateDone)},
			{Name: "live-one", State: string(store.StateActive)},
		},
		Gated:  []availability.Gate{{Token: "test/m"}},
		Unused: []ProviderGate{gate},
	}

	out := HideDone(rep)
	if out.DoneHidden != 1 || len(out.Bindings) != 1 {
		t.Fatalf("HideDone hid %d rows, kept %d, want 1 and 1", out.DoneHidden, len(out.Bindings))
	}
	if !reflect.DeepEqual(out.Unused, []ProviderGate{gate}) {
		t.Errorf("HideDone Unused = %+v, want %+v", out.Unused, []ProviderGate{gate})
	}
}
