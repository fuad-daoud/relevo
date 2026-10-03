package relevo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// flowThreeActorsWorkflow reaches three actors, so a start creates three
// members and a forced failure on one leaves none.
const flowThreeActorsWorkflow = `name: three
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: review } }
  review: { run: assistant, seed: "review", on: { verdict=pass: done, verdict=changes: scan } }
  scan: { run: researcher, seed: "scan", on: { done: done } }
`

// flowTwoChecksWorkflow carries two different non-empty check commands, which
// one placed writer cannot answer from its single pulled gate record.
const flowTwoChecksWorkflow = `name: twochecks
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: first } }
  first: { check: "make check", on: { green: second, red: { halt: "red" } } }
  second: { check: "make test", on: { green: done, red: { halt: "red" } } }
`

// flowPlacedCheckWorkflow gives one writer and one check command, the shape a
// placed writer's pulled gate can answer.
const flowPlacedCheckWorkflow = `name: placedcheck
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: check } }
  check: { check: "make check", on: { green: done, red: { halt: "red" } } }
`

// TestWorkflowChainStartCreatesMembersAllOrNone pins the all-or-none create: a
// forced failure of the third member leaves no chain row, no member binding and
// the worktree the start had cut rolled back.
func TestWorkflowChainStartCreatesMembersAllOrNone(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	// The third member's binding directory is a file, so its prepareSave is
	// refused after the worktree was cut and the first two members prepared.
	if err := os.WriteFile(rt.Store.Dir("shop-researcher"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("plant the blocking file: %v", err)
	}

	_, err := ChainStart(context.Background(), rt, ChainOptions{
		Name: "shop", Feature: "auth", MasterMindID: testMasterMindName,
		Plans:    []string{writePlan(t, "build it")},
		Workflow: writeWorkflowFile(t, flowThreeActorsWorkflow),
	})
	if err == nil {
		t.Fatal("ChainStart = nil, want the third member's failure")
	}

	if _, cerr := rt.Store.Chain("shop"); !errors.Is(cerr, store.ErrNotFound) {
		t.Errorf("chain row after the failure = %v, want ErrNotFound", cerr)
	}
	for _, name := range []string{"shop-assistant", "shop", "shop-researcher"} {
		if _, lerr := rt.Store.Load(name); !errors.Is(lerr, store.ErrNotFound) {
			t.Errorf("binding %q after the failure = %v, want ErrNotFound", name, lerr)
		}
	}
	if len(fg.addWorktreeCalls) != 1 {
		t.Errorf("AddWorktree calls = %d, want the one cut", len(fg.addWorktreeCalls))
	}
	if len(fg.removeWorktreeCalls) != 1 {
		t.Errorf("RemoveWorktree calls = %+v, want the one rollback", fg.removeWorktreeCalls)
	}
}

// TestPlacedStartRefusesWhenServerLacksCheck pins the placed-writer start
// refusal: a workflow with a check step places a writer on a server, and a
// server that cannot run that check has nowhere to answer it from, so the start
// refuses and names the server and the feature it lacks.
func TestPlacedStartRefusesWhenServerLacksCheck(t *testing.T) {
	t.Parallel()

	rt, _ := chainPlacedRuntime(t, chainRemoteFake(), chainRemoteRows())
	_, err := ChainStart(context.Background(), rt, ChainOptions{
		Name: "shop", Feature: "auth", MasterMindID: testMasterMindName,
		Plans:    []string{writePlan(t, "build it")},
		Workflow: writeWorkflowFile(t, flowTwoChecksWorkflow),
	})
	if err == nil {
		t.Fatal("ChainStart = nil, want the placed-writer check refusal")
	}
	if !strings.Contains(err.Error(), "zen") || !strings.Contains(err.Error(), `"check"`) {
		t.Errorf("refusal = %q, want it to name server zen and feature check", err)
	}
}

// TestDefaultChainStartsOnWorkflowEngine pins that a start with no --workflow
// runs the shipped default on the engine: the row stores the definition and the
// start state, and its member rows map the actors that definition runs.
func TestDefaultChainStartsOnWorkflowEngine(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	res := startedChain(t, rt, ChainOptions{})

	row := chainStoredRow(t, rt, "shop")
	if len(row.WorkflowJSON) == 0 {
		t.Error("chain row stores no workflow definition")
	}
	if len(row.StateJSON) == 0 {
		t.Error("chain row stores no workflow state")
	}
	for _, tc := range []struct{ name, actor string }{
		{"shop", "builder"},
		{"shop-rev", "reviewer"},
		{"shop-plan", "lite-planner"},
	} {
		if m := memberByName(t, res.Members, tc.name); m.Role != tc.actor {
			t.Errorf("member %s maps actor %q, want %q", tc.name, m.Role, tc.actor)
		}
	}
}

// TestDefaultChainMemberNamesPerActor pins the shipped default's member names:
// the builder keeps the chain's own name and each reader actor takes the legacy
// suffix its part carries.
func TestDefaultChainMemberNamesPerActor(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	res := startedChain(t, rt, ChainOptions{Security: ptr(true)})

	for _, name := range []string{"shop", "shop-rev", "shop-plan", "shop-sec"} {
		memberByName(t, res.Members, name)
	}
	row := chainStoredRow(t, rt, "shop")
	if row.Builder != "shop" || row.Reviewer != "shop-rev" || row.Planner != "shop-plan" || row.Security != "shop-sec" {
		t.Errorf("member columns = %q/%q/%q/%q, want shop/shop-rev/shop-plan/shop-sec",
			row.Builder, row.Reviewer, row.Planner, row.Security)
	}
}
