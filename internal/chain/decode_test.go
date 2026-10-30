package chain

import (
	"encoding/json"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

func TestWorkflowDefReadsTheStoredDefinition(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(workflow.Default())
	if err != nil {
		t.Fatal(err)
	}
	def, err := WorkflowDef(db.ChainRow{Name: "c", WorkflowJSON: raw})
	if err != nil || def.Name != workflow.Default().Name {
		t.Fatalf("WorkflowDef = %q, %v; want the default", def.Name, err)
	}
	if _, err := WorkflowDef(db.ChainRow{Name: "c"}); err == nil || err.Error() != "chain c carries no workflow" {
		t.Errorf("empty: %v", err)
	}
	if _, err := WorkflowDef(db.ChainRow{Name: "c", WorkflowJSON: []byte("{")}); err == nil {
		t.Error("malformed: nil error")
	}
}

func TestWorkflowStateDecodesTheEngineState(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(workflow.State{Awaiting: workflow.Awaiting{Member: "builder", Round: 2}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := WorkflowState(db.ChainRow{Name: "c", StateJSON: raw})
	if err != nil || st.Awaiting.Member != "builder" || st.Awaiting.Round != 2 {
		t.Fatalf("WorkflowState = %+v, %v", st.Awaiting, err)
	}
	if _, err := WorkflowState(db.ChainRow{Name: "c"}); err == nil || err.Error() != "chain c has no engine state" {
		t.Errorf("empty: %v", err)
	}
	if _, err := WorkflowState(db.ChainRow{Name: "c", StateJSON: []byte("[")}); err == nil {
		t.Error("malformed: nil error")
	}
}

func TestFlowActorMatchTakesAnEmptyRoleAsTheBuilder(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		stored, actor string
		want          bool
	}{
		{"", "builder", true},
		{"builder", "builder", true},
		{"reviewer", "reviewer", true},
		{"", "reviewer", false},
		{"reviewer", "builder", false},
	} {
		if got := FlowActorMatch(tc.stored, tc.actor); got != tc.want {
			t.Errorf("FlowActorMatch(%q, %q) = %v, want %v", tc.stored, tc.actor, got, tc.want)
		}
	}
}
