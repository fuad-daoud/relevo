package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// childWorkflowBody is a minimal child workflow running a builder step.
const childWorkflowBody = `name: sub
inputs: { plans: optional, task: optional }
start: work
steps:
  work: { run: builder, seed: "child work", on: { done: done } }
`

// forkFixedWorkflow defines a workflow that forks into two fixed children.
func forkFixedWorkflow(childFile string) string {
	return fmt.Sprintf(`name: parent
inputs: { plans: optional }
start: split
steps:
  split:
    fork:
      children:
        - { workflow: %s, task: "first" }
        - { workflow: %s, task: "second" }
    on: { joined: merge, conflict: done }
  merge: { run: builder, seed: "merge", on: { done: done } }
`, childFile, childFile)
}

// forkEachWorkflow defines a workflow that forks each plan into a child.
func forkEachWorkflow(childFile string) string {
	return fmt.Sprintf(`name: parent
inputs: { plans: required }
start: split
steps:
  split:
    fork:
      each: plans
      workflow: %s
    on: { joined: merge, conflict: done }
  merge: { run: builder, seed: "merge", on: { done: done } }
`, childFile)
}

// TestForkTwoChildrenCreatesRowsAndBranchesCutFromParentTip pins that a fork
// with two children creates two chain rows named <p>.1 and <p>.2, with Parent
// set to <p>, one builder member each, and branches cut from the parent tip.
func TestForkTwoChildrenCreatesRowsAndBranchesCutFromParentTip(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	childFile := writeWorkflowFile(t, childWorkflowBody)
	parentFile := writeWorkflowFile(t, forkFixedWorkflow(childFile))

	// Direct parent tip commit so the test proves child branches cut from the tip.
	fg.refSHA = map[string]string{
		"relevo/shop": "parent-tip-commit-789",
	}

	res := startedChain(t, rt, ChainOptions{
		Name:     "shop",
		Workflow: parentFile,
	})
	if res.Chain.Status != string(workflow.StatusRunning) {
		t.Fatalf("parent status = %q, want running", res.Chain.Status)
	}

	c1, err := rt.Store.Chain("shop.1")
	if err != nil {
		t.Fatalf("load child 1: %v", err)
	}
	if c1.Parent != "shop" {
		t.Errorf("c1.Parent = %q, want %q", c1.Parent, "shop")
	}
	if c1.Base != "parent-tip-commit-789" {
		t.Errorf("c1.Base = %q, want parent tip %q", c1.Base, "parent-tip-commit-789")
	}
	if c1.Branch != "relevo/shop.1" {
		t.Errorf("c1.Branch = %q, want relevo/shop.1", c1.Branch)
	}

	c2, err := rt.Store.Chain("shop.2")
	if err != nil {
		t.Fatalf("load child 2: %v", err)
	}
	if c2.Parent != "shop" {
		t.Errorf("c2.Parent = %q, want %q", c2.Parent, "shop")
	}
	if c2.Base != "parent-tip-commit-789" {
		t.Errorf("c2.Base = %q, want parent tip %q", c2.Base, "parent-tip-commit-789")
	}
	if c2.Branch != "relevo/shop.2" {
		t.Errorf("c2.Branch = %q, want relevo/shop.2", c2.Branch)
	}

	m1, err := rt.Store.Load("shop.1")
	if err != nil {
		t.Fatalf("load child 1 member: %v", err)
	}
	if m1.Role != "builder" || m1.Shape != store.ShapeWriter {
		t.Errorf("child 1 member = %+v, want builder writer", m1)
	}

	m2, err := rt.Store.Load("shop.2")
	if err != nil {
		t.Fatalf("load child 2 member: %v", err)
	}
	if m2.Role != "builder" || m2.Shape != store.ShapeWriter {
		t.Errorf("child 2 member = %+v, want builder writer", m2)
	}
}

// TestForkEachPlansCreatesChildRows pins that each: plans with three items
// creates three child chain rows cut from the parent tip.
func TestForkEachPlansCreatesChildRows(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	childFile := writeWorkflowFile(t, childWorkflowBody)
	parentFile := writeWorkflowFile(t, forkEachWorkflow(childFile))

	fg.refSHA = map[string]string{
		"relevo/shop": "parent-tip-commit-333",
	}

	plans := []string{
		writePlan(t, "plan 1 body"),
		writePlan(t, "plan 2 body"),
		writePlan(t, "plan 3 body"),
	}

	startedChain(t, rt, ChainOptions{
		Name:     "shop",
		Plans:    plans,
		Workflow: parentFile,
	})

	for _, key := range []string{"1", "2", "3"} {
		childName := "shop." + key
		c, err := rt.Store.Chain(childName)
		if err != nil {
			t.Fatalf("load %s: %v", childName, err)
		}
		if c.Parent != "shop" {
			t.Errorf("%s.Parent = %q, want shop", childName, c.Parent)
		}
		if c.Base != "parent-tip-commit-333" {
			t.Errorf("%s.Base = %q, want parent tip %q", childName, c.Base, "parent-tip-commit-333")
		}
	}
}

// TestForkFailureRollsBackEarlierChildrenAndHaltsParent pins that a failure
// creating child 2 unwinds child 1 and halts the parent with the reason.
func TestForkFailureRollsBackEarlierChildrenAndHaltsParent(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	childFile := writeWorkflowFile(t, childWorkflowBody)
	parentFile := writeWorkflowFile(t, forkFixedWorkflow(childFile))

	// Plant a file blocking child 2's binding directory to force child 2 start failure.
	if err := os.WriteFile(rt.Store.Dir("shop.2"), []byte("blocker"), 0o644); err != nil {
		t.Fatalf("plant blocking file: %v", err)
	}

	startedChain(t, rt, ChainOptions{
		Name:     "shop",
		Workflow: parentFile,
	})

	parent, err := rt.Store.Chain("shop")
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	if parent.Status != string(workflow.StatusHalted) {
		t.Fatalf("parent status = %q, want halted", parent.Status)
	}
	if !strings.Contains(parent.Reason, "child shop.2 could not start") {
		t.Errorf("parent reason = %q, want failure reason", parent.Reason)
	}

	// Child 1 must be completely rolled back.
	if _, cerr := rt.Store.Chain("shop.1"); !errors.Is(cerr, store.ErrNotFound) {
		t.Errorf("child 1 row = %v, want ErrNotFound", cerr)
	}
	if _, lerr := rt.Store.Load("shop.1"); !errors.Is(lerr, store.ErrNotFound) {
		t.Errorf("child 1 member = %v, want ErrNotFound", lerr)
	}

	foundRemove := false
	for _, call := range fg.removeWorktreeCalls {
		if strings.Contains(call.Path, "shop.1") {
			foundRemove = true
			break
		}
	}
	if !foundRemove {
		t.Errorf("removeWorktreeCalls = %+v, want child 1 removed", fg.removeWorktreeCalls)
	}

	foundBranchDelete := false
	for _, call := range fg.deleteBranchCalls {
		if call.Branch == "relevo/shop.1" {
			foundBranchDelete = true
			break
		}
	}
	if !foundBranchDelete {
		t.Errorf("deleteBranchCalls = %+v, want relevo/shop.1 deleted", fg.deleteBranchCalls)
	}
}

// TestForkNameOverCapRefused pins that a child name exceeding chainNameCap is
// refused at fork time.
func TestForkNameOverCapRefused(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	// Long child workflow with reviewer reader member (suffix -reviewer, 9 chars).
	childWorkflow := `name: sublong
inputs: { plans: optional, task: optional }
start: work
steps:
  work: { run: builder, seed: "build", on: { done: check } }
  check: { run: reviewer, seed: "rev", on: { verdict=pass: done, verdict=changes: done } }
`
	childFile := writeWorkflowFile(t, childWorkflow)
	// Cap is store.MaxAgentNameLen (32) - len("-reviewer") (9) = 23 chars.
	// A parent of length 22 gives child name len 24 ("p22.1"), which exceeds 23.
	parentName := "a123456789012345678901"
	parentFile := writeWorkflowFile(t, forkFixedWorkflow(childFile))

	startedChain(t, rt, ChainOptions{
		Name:     parentName,
		Workflow: parentFile,
	})

	parent, err := rt.Store.Chain(parentName)
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	if parent.Status != string(workflow.StatusHalted) {
		t.Fatalf("parent status = %q, want halted", parent.Status)
	}
	if !strings.Contains(parent.Reason, "exceeds") {
		t.Errorf("parent reason = %q, want over-cap refusal", parent.Reason)
	}
}

// TestForkOnPlacedWriterRefused pins that a workflow with a fork step refuses
// placed writers at start, naming W4.
func TestForkOnPlacedWriterRefused(t *testing.T) {
	t.Parallel()

	rt, _ := chainPlacedRuntime(t, chainRemoteFake(), chainRemoteRows())
	childFile := writeWorkflowFile(t, childWorkflowBody)
	parentFile := writeWorkflowFile(t, forkFixedWorkflow(childFile))

	_, err := ChainStart(context.Background(), rt, ChainOptions{
		Name:         "shop",
		Feature:      "auth",
		MasterMindID: testMasterMindName,
		Workflow:     parentFile,
	})
	if err == nil {
		t.Fatal("ChainStart = nil, want placed writer fork refusal")
	}
	if !strings.Contains(err.Error(), "W4") {
		t.Errorf("refusal error = %q, want it to name W4", err)
	}
}

// TestForkDryRunListsChildrenWithoutWriting pins that --dry-run lists each fork
// and the children it would create with names, without creating anything.
func TestForkDryRunListsChildrenWithoutWriting(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	childFile := writeWorkflowFile(t, childWorkflowBody)
	parentFile := writeWorkflowFile(t, forkFixedWorkflow(childFile))

	doc, err := ChainDryRun(context.Background(), rt, ChainOptions{
		Name:     "shop",
		Workflow: parentFile,
	})
	if err != nil {
		t.Fatalf("ChainDryRun: %v", err)
	}

	rendered := RenderChainDryRun(doc)
	if !strings.Contains(rendered, "children=shop.1,shop.2") {
		t.Errorf("rendered dry run missing children list:\n%s", rendered)
	}

	chains, err := rt.Store.Chains()
	if err != nil {
		t.Fatalf("Store.Chains: %v", err)
	}
	if len(chains) != 0 {
		t.Errorf("chains count = %d, want 0", len(chains))
	}
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("addWorktreeCalls = %d, want 0", len(fg.addWorktreeCalls))
	}
}
