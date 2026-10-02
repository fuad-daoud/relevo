package relevo

import (
	"context"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// forkEndOnCloseWorkflow is the one-step workflow whose builder close ends the
// chain, so a test can watch a top-level chain's own end delivery.
const forkEndOnCloseWorkflow = `name: endonclose
inputs: { plans: optional }
start: build
steps:
  build: { run: builder, seed: "build it", on: { done: done } }
`

// forkChildClose builds the close one fork child is waiting for, so a test
// names only the kind and the result it wants the child to end with.
func forkChildClose(t *testing.T, rt Runtime, child string) workflow.Event {
	t.Helper()
	c, err := rt.Store.Chain(child)
	if err != nil {
		t.Fatalf("load child %s: %v", child, err)
	}
	st, err := chainWorkflowState(c)
	if err != nil {
		t.Fatalf("chainWorkflowState(%s): %v", child, err)
	}
	return workflow.Event{Step: st.Awaiting.Step, Member: st.Awaiting.Member, Round: st.Awaiting.Round}
}

// forkChildEnd drives one fork child to its end through the engine, the way its
// own member's close would, so the parent moves in the child's transaction.
func forkChildEnd(t *testing.T, rt Runtime, child string, ev workflow.Event) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, cerr := tx.Chain(child)
		if cerr != nil {
			return cerr
		}
		return chainAdvance(context.Background(), rt, tx, c, ev)
	}); err != nil {
		t.Fatalf("end child %s with %+v: %v", child, ev, err)
	}
}

// forkEndChild drives one fork child to the given final status, the way a close
// that matched no edge ends it.
func forkEndChild(t *testing.T, rt Runtime, child, status, reason string) {
	t.Helper()
	ev := forkChildClose(t, rt, child)
	ev.Kind = workflow.EventStepClosed
	ev.Status = status
	ev.Reason = reason
	forkChildEnd(t, rt, child, ev)
}

// forkStopChild stops one fork child, the way a human stop ends it.
func forkStopChild(t *testing.T, rt Runtime, child string) {
	t.Helper()
	ev := forkChildClose(t, rt, child)
	ev.Kind = workflow.EventStopped
	forkChildEnd(t, rt, child, ev)
}

// chainEndDeliveries is every end delivery queued on the bindings named.
func chainEndDeliveries(t *testing.T, rt Runtime, names ...string) []store.LogEntry {
	t.Helper()
	var out []store.LogEntry
	for _, name := range names {
		entries, err := rt.Store.ReadLog(name)
		if err != nil {
			t.Fatalf("ReadLog(%s): %v", name, err)
		}
		for _, e := range entries {
			if e.Kind == store.KindChain && e.Direction == store.DirToMasterMind {
				out = append(out, e)
			}
		}
	}
	return out
}

// TestForkChildDoneEndsTheParentForkAndDeliversNothing pins the whole handoff:
// the first child's done reaches the parent as child_ended and queues no
// delivery, and the second child's done moves the parent on to the merge.
func TestForkChildDoneEndsTheParentForkAndDeliversNothing(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	childFile := writeWorkflowFile(t, childWorkflowBody)
	startedChain(t, rt, ChainOptions{Name: "shop", Workflow: writeWorkflowFile(t, forkFixedWorkflow(childFile))})

	forkEndChild(t, rt, "shop.1", "done", "")

	parent := flowChainRow(t, rt)
	st, err := chainWorkflowState(parent)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	end, ok := st.Awaiting.Ended["1"]
	if !ok {
		t.Fatalf("parent awaited ended = %+v, want child 1 recorded", st.Awaiting.Ended)
	}
	if end.Status != string(workflow.StatusDone) {
		t.Errorf("child 1 end = %+v, want done", end)
	}
	if st.Awaiting.Merging {
		t.Error("parent is already merging after one of two children ended")
	}
	if got := chainEndDeliveries(t, rt, "shop.1", "shop.2"); len(got) != 0 {
		t.Errorf("deliveries = %+v, want none: a child never delivers", got)
	}

	forkEndChild(t, rt, "shop.2", "done", "")

	st, err = chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if got := st.Results["split"]; got.Status != chainMergeJoined {
		t.Errorf("fork result = %+v, want joined after both children ended", got)
	}
	if got := chainEndDeliveries(t, rt, "shop.1", "shop.2"); len(got) != 0 {
		t.Errorf("deliveries = %+v, want none for either child", got)
	}
}

// TestForkChildHaltedWaitsForItsSiblingThenHaltsTheParent pins that a halted
// child is recorded straight away and the parent only halts once the running
// sibling has ended -- with the halted child's own reason.
func TestForkChildHaltedWaitsForItsSiblingThenHaltsTheParent(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	childFile := writeWorkflowFile(t, childWorkflowBody)
	startedChain(t, rt, ChainOptions{Name: "shop", Workflow: writeWorkflowFile(t, forkFixedWorkflow(childFile))})

	forkEndChild(t, rt, "shop.1", "halted", "the disk is on fire")

	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if got := flowChainRow(t, rt).Status; got != string(workflow.StatusRunning) {
		t.Fatalf("parent status = %q, want running while the sibling still runs", got)
	}
	if end := st.Awaiting.Ended["1"]; end.Status != string(workflow.StatusHalted) {
		t.Errorf("child 1 end = %+v, want halted", end)
	}
	if st.Awaiting.Merging {
		t.Error("parent is merging with one child still running")
	}

	forkEndChild(t, rt, "shop.2", "done", "")

	parent := flowChainRow(t, rt)
	if parent.Status != string(workflow.StatusHalted) {
		t.Fatalf("parent status = %q (%q), want halted once every child ended", parent.Status, parent.Reason)
	}
	if !strings.Contains(parent.Reason, "the disk is on fire") {
		t.Errorf("parent reason = %q, want the halted child's own reason", parent.Reason)
	}
}

// TestForkChildStoppedEndsAsStopped pins that a stopped child reaches the parent
// as stopped, not halted.
func TestForkChildStoppedEndsAsStopped(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	childFile := writeWorkflowFile(t, childWorkflowBody)
	startedChain(t, rt, ChainOptions{Name: "shop", Workflow: writeWorkflowFile(t, forkFixedWorkflow(childFile))})

	forkStopChild(t, rt, "shop.1")

	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if end := st.Awaiting.Ended["1"]; end.Status != string(workflow.StatusStopped) {
		t.Errorf("child 1 end = %+v, want stopped", end)
	}
}

// TestTopLevelChainEndDeliversExactlyOnce pins that the child handoff took
// nothing away from a top-level chain: its own end still queues one delivery.
func TestTopLevelChainEndDeliversExactlyOnce(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, forkEndOnCloseWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	got := chainEndDeliveries(t, rt, "shop", "shop-assistant")
	if len(got) != 1 {
		t.Fatalf("end deliveries = %+v, want exactly one", got)
	}
	if !strings.Contains(got[0].Payload, "chain shop finished") {
		t.Errorf("delivery payload = %q, want the chain's own end", got[0].Payload)
	}
}

// TestForkChildEndWithNoParentRowEndsQuietly pins that a child whose parent row
// is gone still ends, and its end is a warning rather than a failed close.
func TestForkChildEndWithNoParentRowEndsQuietly(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	res := startedChain(t, rt, ChainOptions{Name: "shop", Workflow: writeWorkflowFile(t, forkFixedWorkflow(writeWorkflowFile(t, childWorkflowBody)))})
	_ = res

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.ChainDelete("shop")
	}); err != nil {
		t.Fatalf("delete the parent row: %v", err)
	}

	forkEndChild(t, rt, "shop.1", "done", "")

	c, err := rt.Store.Chain("shop.1")
	if err != nil {
		t.Fatalf("load child: %v", err)
	}
	if c.Status != string(workflow.StatusDone) {
		t.Errorf("child status = %q, want done", c.Status)
	}
	if got := chainEndDeliveries(t, rt, "shop.1"); len(got) != 0 {
		t.Errorf("deliveries = %+v, want none", got)
	}
}

// TestForkLateChildEndAfterTheParentEndedIsIgnored pins the replay guard on the
// parent side: a child that ends after its parent already reached a terminal
// status changes nothing and writes no row.
func TestForkLateChildEndAfterTheParentEndedIsIgnored(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	childFile := writeWorkflowFile(t, childWorkflowBody)
	startedChain(t, rt, ChainOptions{Name: "shop", Workflow: writeWorkflowFile(t, forkFixedWorkflow(childFile))})

	// A halt closes the parent on its own, before any child has ended.
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventNeedsYou, Step: "split", Reason: "a human stopped it"})
	if got := flowChainRow(t, rt).Status; got != string(workflow.StatusHalted) {
		t.Fatalf("parent status = %q, want halted before any child ended", got)
	}
	before := flowChainRow(t, rt)

	forkEndChild(t, rt, "shop.1", "done", "")

	after := flowChainRow(t, rt)
	if after.Status != before.Status || after.Reason != before.Reason || string(after.StateJSON) != string(before.StateJSON) {
		t.Errorf("parent row = %+v, want it untouched by a late child end", after)
	}
}

// TestForkChildKeyIsTheSegmentAfterTheParent pins the key a child is known by
// inside its parent: the segment of its chain name after "<parent>.".
func TestForkChildKeyIsTheSegmentAfterTheParent(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		child, parent, want string
	}{
		{"shop.1", "shop", "1"},
		{"shop.10", "shop", "10"},
		{"shop", "", ""},
	} {
		c := db.ChainRow{Name: tc.child, Parent: tc.parent}
		if got := chainForkChildKey(c); got != tc.want {
			t.Errorf("chainForkChildKey(%+v) = %q, want %q", c, got, tc.want)
		}
	}
}
