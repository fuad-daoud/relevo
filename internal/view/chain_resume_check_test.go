package view

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// TestChainHaltNextNamesOnlyTheCheck pins the short next move: a halt on a
// repair arm names --from the feeding check, never the arm itself.
func TestChainHaltNextNamesOnlyTheCheck(t *testing.T) {
	t.Parallel()

	got := chainHaltNext("shop", "repair has no budget left", "check")
	if got == nil {
		t.Fatal("Next is nil, want the halt's next move")
	}
	for _, want := range []string{
		"relevo chain --resume --name shop --from check",
		"`relevo done shop` closes it",
	} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("Next text %q does not carry %q", got.Text, want)
		}
	}
	if strings.Contains(got.Text, "--from repair") {
		t.Errorf("Next text %q names the arm: the statusline stays short with only the check", got.Text)
	}

	plain := chainHaltNext("shop", "reviewer wants changes", "")
	if plain == nil || !strings.Contains(plain.Text, "relevo chain --resume --name shop`") {
		t.Errorf("Next = %+v, want today's resume without --from", plain)
	}
	if strings.Contains(plain.Text, "--from") {
		t.Errorf("Next text %q names --from without a feeding check", plain.Text)
	}
}

// TestFactsOfComputesResumeCheck pins the fact the next move reads: a halted
// repair arm with one feeding check carries it, every other chain carries
// none.
func TestFactsOfComputesResumeCheck(t *testing.T) {
	t.Parallel()

	def, err := json.Marshal(workflow.Default())
	if err != nil {
		t.Fatal(err)
	}
	haltedRepair := func() db.ChainRow {
		st := workflow.State{
			Status:   workflow.StatusHalted,
			At:       "repair",
			Awaiting: workflow.Awaiting{Step: "repair", Member: "builder", Round: 2},
			Iter:     map[string]workflow.Iter{"plans": {Index: 0, Items: []string{"a"}}},
		}
		raw, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		return db.ChainRow{Name: "w", Status: string(chain.StatusHalted), Builder: "w", WorkflowJSON: def, StateJSON: raw}
	}

	s := store.New(t.TempDir())
	row := putChain(t, s, haltedRepair(), store.Binding{Name: "w"})
	if got := FactsOf(s, row); got.ResumeCheck != "check" {
		t.Errorf("ResumeCheck = %q, want check for a halted repair arm", got.ResumeCheck)
	}

	running := haltedRepair()
	running.Status = string(chain.StatusRunning)
	running = putChain(t, s, db.ChainRow{Name: "r", Status: string(chain.StatusRunning), Builder: "r", WorkflowJSON: def, StateJSON: running.StateJSON}, store.Binding{Name: "r"})
	if got := FactsOf(s, running); got.ResumeCheck != "" {
		t.Errorf("ResumeCheck = %q on a running chain, want empty", got.ResumeCheck)
	}

	legacy := putChain(t, s, db.ChainRow{Name: "l", Status: string(chain.StatusHalted), Builder: "l"}, store.Binding{Name: "l"})
	if got := FactsOf(s, legacy); got.ResumeCheck != "" {
		t.Errorf("ResumeCheck = %q without a workflow, want empty", got.ResumeCheck)
	}
}

// TestFactsOfLeavesAmbiguousArmWithoutCheck pins the multi-check fallback: an
// arm two checks reach carries no resume check.
func TestFactsOfLeavesAmbiguousArmWithoutCheck(t *testing.T) {
	t.Parallel()

	const body = `name: twogates
inputs: { plans: optional }
params: { gate: make check, budget: 0 }
start: build
steps:
  build: { run: builder, seed: "build it", on: { done: gate-a } }
  gate-a: { check: "{{params.gate}}", on: { green: gate-b, red: arm } }
  gate-b: { check: "{{params.gate}}", on: { green: done, red: arm } }
  arm: { run: builder, seed: "fix it",
         budget: { max: "{{params.budget}}", per: build, then: gate-a },
         on: { done: gate-a } }
`
	def, err := workflow.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	rawDef, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	st := workflow.State{
		Status:   workflow.StatusHalted,
		At:       "arm",
		Awaiting: workflow.Awaiting{Step: "arm", Member: "builder", Round: 1},
		Iter:     map[string]workflow.Iter{"plans": {Index: 0, Items: []string{"a"}}},
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	s := store.New(t.TempDir())
	row := putChain(t, s, db.ChainRow{Name: "m", Status: string(chain.StatusHalted), Builder: "m", WorkflowJSON: rawDef, StateJSON: raw}, store.Binding{Name: "m"})
	if got := FactsOf(s, row); got.ResumeCheck != "" {
		t.Errorf("ResumeCheck = %q with two feeding checks, want empty", got.ResumeCheck)
	}
}
