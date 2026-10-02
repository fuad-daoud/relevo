package relevo

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// forkNestedWorkflow forks into one child that is itself a fork, so a stop on the
// parent has a grandchild level to reach.
func forkNestedWorkflow(midFile string) string {
	return fmt.Sprintf(`name: parent
inputs: { plans: optional }
start: split
steps:
  split:
    fork:
      children:
        - { workflow: %s, task: "mid" }
    on: { joined: merge, conflict: done }
  merge: { run: builder, seed: "merge", on: { done: done } }
`, midFile)
}

// forkNestedChildBody is a child that forks in its turn: it needs a writer run
// step of its own, which its finish step is.
func forkNestedChildBody(leafFile string) string {
	return fmt.Sprintf(`name: mid
inputs: { plans: optional, task: optional }
start: split
steps:
  split:
    fork:
      children:
        - { workflow: %s, task: "leaf a" }
        - { workflow: %s, task: "leaf b" }
    on: { joined: finish, conflict: finish }
  finish: { run: builder, seed: "mid", on: { done: done } }
`, leafFile, leafFile)
}

// TestForkStopOnParentStopsEveryChildAndDeliversOnce pins the stop of a fork
// tree: the parent and both children end stopped, the parent's end is the only
// delivery, and no merge runs.
func TestForkStopOnParentStopsEveryChildAndDeliversOnce(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	forkAwaitingParent(t, rt)

	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}

	for _, name := range []string{"shop", "shop.1", "shop.2"} {
		if got := chainStoredRow(t, rt, name).Status; got != string(workflow.StatusStopped) {
			t.Errorf("chain %s status = %q, want stopped", name, got)
		}
	}
	if got := chainEndDeliveries(t, rt, "shop", "shop.1", "shop.2"); len(got) != 1 {
		t.Errorf("end deliveries = %+v, want exactly one, on the parent", got)
	}
	if got := len(fg.mergeKeepCalls); got != 0 {
		t.Errorf("merge calls = %d, want none: a stopped fork never merges", got)
	}
	st := mustChainState(t, chainStoredRow(t, rt, "shop"))
	if got := st.Results["split"]; got.Status != "" {
		t.Errorf("fork result = %+v, want none: the stop reached the merge", got)
	}
	if st.Awaiting.Merging {
		t.Error("a stopped parent is merging")
	}
}

// TestForkStopOnNestedForkStopsGrandchildren pins that the cascade recurses: a
// child that is itself a fork takes its own children down with it.
func TestForkStopOnNestedForkStopsGrandchildren(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	leaf := writeWorkflowFile(t, childWorkflowBody)
	mid := writeWorkflowFile(t, forkNestedChildBody(leaf))
	startedChain(t, rt, ChainOptions{
		Name:     "shop",
		Workflow: writeWorkflowFile(t, forkNestedWorkflow(mid)),
	})
	for _, name := range []string{"shop", "shop.1", "shop.1.1", "shop.1.2"} {
		if _, err := rt.Store.Chain(name); err != nil {
			t.Fatalf("premise: chain %s was never created: %v", name, err)
		}
	}

	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}

	for _, name := range []string{"shop", "shop.1", "shop.1.1", "shop.1.2"} {
		if got := chainStoredRow(t, rt, name).Status; got != string(workflow.StatusStopped) {
			t.Errorf("chain %s status = %q, want stopped", name, got)
		}
	}
	// Only the two chains a human named by stopping -- the outermost parent and
	// nothing else -- carry an end; every level under it delivers nothing.
	if got := chainEndDeliveries(t, rt, "shop.1", "shop.1.1", "shop.1.2"); len(got) != 0 {
		t.Errorf("deliveries under the parent = %+v, want none", got)
	}
}

// TestForkStopOnChildAloneLetsTheParentDecide pins the other stop: a child on its
// own is just a chain, and its end reaches the parent as a stopped child, which
// the parent then answers.
func TestForkStopOnChildAloneLetsTheParentDecide(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	forkAwaitingParent(t, rt)

	if _, err := ChainStop(context.Background(), rt, "shop.1"); err != nil {
		t.Fatalf("ChainStop shop.1: %v", err)
	}

	st := mustChainState(t, chainStoredRow(t, rt, "shop"))
	if end := st.Awaiting.Ended["1"]; end.Status != string(workflow.StatusStopped) {
		t.Errorf("parent's record of child 1 = %+v, want stopped", end)
	}
	if got := chainStoredRow(t, rt, "shop.2").Status; got != string(workflow.StatusRunning) {
		t.Errorf("sibling status = %q, want it untouched by a stop on its sibling", got)
	}
	if got := chainStoredRow(t, rt, "shop").Status; got != string(workflow.StatusRunning) {
		t.Errorf("parent status = %q, want it still waiting on the sibling", got)
	}

	// The last child ending is what halts the parent, with the stopped child's
	// own wording.
	forkEndChild(t, rt, "shop.2", "done", "")
	parent := chainStoredRow(t, rt, "shop")
	if parent.Status != string(workflow.StatusHalted) {
		t.Fatalf("parent status = %q (%q), want halted once every child ended", parent.Status, parent.Reason)
	}
	if !strings.Contains(parent.Reason, "child 1 stopped") {
		t.Errorf("parent reason = %q, want the stopped child's own wording", parent.Reason)
	}
}

// TestForkSweepHaltsAChildWithNoMemberAndTheParentSeesIt pins step 4: the sweep
// needed no change to work with children. A child whose member record is gone
// halts, its end reaches the parent as that child's end, and the parent waits for
// its sibling exactly as it waits for any other child.
func TestForkSweepHaltsAChildWithNoMemberAndTheParentSeesIt(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	forkAwaitingParent(t, rt)

	if err := rt.Store.Delete("shop.1"); err != nil {
		t.Fatalf("Delete shop.1: %v", err)
	}
	tickChains(context.Background(), rt)

	child := chainStoredRow(t, rt, "shop.1")
	if child.Status != string(workflow.StatusHalted) {
		t.Fatalf("child status = %q, want halted", child.Status)
	}
	if !strings.Contains(child.Reason, "member shop.1 gone") {
		t.Errorf("child halt reason = %q, want the gone member named", child.Reason)
	}
	st := mustChainState(t, chainStoredRow(t, rt, "shop"))
	if end := st.Awaiting.Ended["1"]; end.Status != string(workflow.StatusHalted) {
		t.Errorf("parent's record of child 1 = %+v, want the sweep's halt", end)
	}
	if got := chainStoredRow(t, rt, "shop").Status; got != string(workflow.StatusRunning) {
		t.Errorf("parent status = %q, want it still waiting on the sibling", got)
	}

	forkEndChild(t, rt, "shop.2", "done", "")

	parent := chainStoredRow(t, rt, "shop")
	if parent.Status != string(workflow.StatusHalted) {
		t.Fatalf("parent status = %q (%q), want halted once every child ended", parent.Status, parent.Reason)
	}
	if !strings.Contains(parent.Reason, "gone") {
		t.Errorf("parent reason = %q, want the child's own reason carried through", parent.Reason)
	}
}

// TestForkStopWithNoChildrenStopsTheParentAlone pins that the cascade adds
// nothing to an ordinary chain: a parent whose step awaits a member is stopped
// through its member, and its sibling-level state never reaches a child that does
// not exist.
func TestForkStopWithNoChildrenStopsTheParentAlone(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{Name: "shop", Workflow: writeWorkflowFile(t, childWorkflowBody)})

	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}

	c := chainStoredRow(t, rt, "shop")
	if c.Status != string(workflow.StatusStopped) {
		t.Fatalf("chain status = %q, want stopped", c.Status)
	}
	if c.Parent != "" {
		t.Errorf("chain parent = %q, want none", c.Parent)
	}
	if _, err := rt.Store.Chain("shop.1"); err == nil {
		t.Error("the stop created a child chain")
	}
	if _, err := rt.Store.Load("shop.1"); !strings.Contains(err.Error(), store.ErrNotFound.Error()) &&
		err == nil {
		t.Error("the stop created a child member")
	}
}