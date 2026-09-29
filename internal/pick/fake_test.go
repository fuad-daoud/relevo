package pick

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

type sentKeys struct{ Target, Keys string }

// testBinding is a bound, active binding with a named builder.
func testBinding(name string) store.Binding {
	return store.Binding{
		Name:             name,
		CWD:              "/tmp/" + name,
		MasterMind:       store.Endpoint{PaneID: "w1:p1", SessionID: "mastermind-session", Kind: "claude"},
		Builder:          store.Endpoint{AgentName: name + "-builder", PaneID: "w1:p2", Kind: "opencode"},
		BuilderCandidate: "agy",
		Round:            2,
		State:            store.StateActive,
	}
}

// testRuntime seeds a store with the given bindings and returns a runtime
// over it. Now is set because the verb stamps its log entry with it; a nil
// clock panics.
func testRuntime(t *testing.T, bindings ...store.Binding) relevo.Runtime {
	t.Helper()
	st := store.New(t.TempDir())
	for _, b := range bindings {
		if err := st.Save(b); err != nil {
			t.Fatalf("Save %s: %v", b.Name, err)
		}
	}
	return relevo.Runtime{Store: st, Now: time.Now}
}

// rowsMsg builds the statusMsg the list would receive for these rows.
func rowsMsg(rows ...view.BindingStatus) statusMsg {
	return statusMsg{report: view.Report{Bindings: rows}}
}

func row(name, display, builderStatus string) view.BindingStatus {
	return view.BindingStatus{Name: name, Display: display, Round: 2, BuilderCandidate: "agy", BuilderStatus: builderStatus}
}

// update runs one message through the model and returns the Model back.
func update(t *testing.T, m Model, msg interface{}) (Model, func() interface{}) {
	t.Helper()
	nm, cmd := m.Update(msg)
	out, ok := nm.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", nm)
	}
	if cmd == nil {
		return out, nil
	}
	return out, func() interface{} { return cmd() }
}
