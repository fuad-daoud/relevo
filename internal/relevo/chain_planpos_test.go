package relevo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// exhaustedPlansWorkflow is a chain whose plans walk has run out: the engine
// sits at scan, past every plan, with the walk reset to {Index: -1, Done: true}.
const exhaustedPlansWorkflow = `name: exhausted
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: scan, empty: done } }
  scan: { run: lite-planner, seed: "scan it", on: { done: done } }
`

// setChainFlowState rewrites the fixture's workflow state and legacy columns in
// one write, the shape a pulled mirror and a local engine row both wear.
func setChainFlowState(t *testing.T, rt Runtime, name, status string, st workflow.State, plan, plans int) {
	t.Helper()
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal the state: %v", err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain(name)
		if err != nil {
			return err
		}
		c.Status = status
		c.WorkflowJSON = []byte(exhaustedPlansWorkflow)
		c.StateJSON = raw
		c.Plan, c.Plans = plan, plans
		// The stored step is the legacy column's own; a workflow row's step
		// comes from its state, which chainFlowFacts reads first.
		c.Step = "reviewing"
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("store the chain state: %v", err)
	}
}

// exhaustedPlansState is the plans walk after its last item: the same -1 index a
// walk that never started carries, and only Done tells them apart.
func exhaustedPlansState(status workflow.Status, at string, n int) workflow.State {
	items := make([]string, n)
	for i := range items {
		items[i] = "plan"
	}
	return workflow.State{
		Status: status,
		At:     at,
		Iter:   map[string]workflow.Iter{"plans": {Index: -1, Items: items, Done: true}},
	}
}

// TestStatusDoneChainShowsFinalPlanCount pins the exhausted walk's projection:
// a chain past its plans reads its last plan, exactly as workflow.LegacyView
// projects it and as the stored legacy columns record it. Reading the raw
// iterator instead would clamp the reset's 0 to 1 and print "plans 1/6" on a
// finished chain.
func TestStatusDoneChainShowsFinalPlanCount(t *testing.T) {
	rt := newRuntime(t)
	newChainFixture(t, rt, "done")
	setChainFlowState(t, rt, "x", "done",
		exhaustedPlansState(workflow.StatusDone, "done", 6), 6, 6)

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	f := rep.Bindings[0].Chain
	if f == nil {
		t.Fatalf("row %q carries no chain facts", rep.Bindings[0].Name)
	}
	if f.PlanPos != 6 || f.PlanTotal != 6 {
		t.Errorf("plan position = %d/%d, want 6/6 for an exhausted walk", f.PlanPos, f.PlanTotal)
	}
	// A done chain omits its step word but keeps the plan count it finished on.
	if got := view.ChainSegment(*f); got != "plans 6/6" {
		t.Errorf("ChainSegment = %q, want %q", got, "plans 6/6")
	}
}

// TestStatusSecurityPhaseShowsFinalPlanCount pins the same projection while the
// chain is still running past its plans: the security fix line reads the plan it
// is fixing, not a plan index the reset invented.
func TestStatusSecurityPhaseShowsFinalPlanCount(t *testing.T) {
	rt := newRuntime(t)
	newChainFixture(t, rt, "running")
	st := exhaustedPlansState(workflow.StatusRunning, "scan", 6)
	setChainFlowState(t, rt, "x", "running", st, 6, 6)

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	f := rep.Bindings[0].Chain
	if f == nil {
		t.Fatalf("row %q carries no chain facts", rep.Bindings[0].Name)
	}
	if f.PlanPos != 6 || f.PlanTotal != 6 {
		t.Errorf("plan position = %d/%d, want 6/6 for an exhausted walk mid-chain", f.PlanPos, f.PlanTotal)
	}
	if got := view.ChainSegment(*f); got != "scan r0 · plans 6/6" {
		t.Errorf("ChainSegment = %q, want %q", got, "scan r0 · plans 6/6")
	}
}

// TestStatusDoneChainNamesNoPendingMember pins the display contract for a done
// chain: it does not say it is pending on anything. Done() deliberately leaves
// the log history in place, so every member keeps the stranded entry that read
// as pending forever.
func TestStatusDoneChainNamesNoPendingMember(t *testing.T) {
	rt := newRuntime(t)
	newChainFixture(t, rt, "done")
	setChainFlowState(t, rt, "x", "done",
		exhaustedPlansState(workflow.StatusDone, "done", 6), 6, 6)

	// The same stranded report a running chain would name.
	if err := rt.Store.AppendLog("x-rev", store.LogEntry{
		TS: time.Now().UTC(), Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport,
	}); err != nil {
		t.Fatalf("plant the stranded report: %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	f := rep.Bindings[0].Chain
	if len(f.PendingMembers) != 0 {
		t.Errorf("pending members = %v, want none for a done chain", f.PendingMembers)
	}
	if got := view.ChainPendingSegment(*f); got != "" {
		t.Errorf("ChainPendingSegment = %q, want no segment", got)
	}
	if line := view.RenderStatus(rep); strings.Contains(line, "pending on") {
		t.Errorf("done chain still reads as pending:\n%s", line)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), "pending_members") {
		t.Errorf("document carries the pending_members key for a done chain: %s", raw)
	}
}

// TestStatusHaltedChainStillNamesPendingMember pins that the done guard is not
// a blanket one: a halted chain's pending member is meaningful and stays named.
func TestStatusHaltedChainStillNamesPendingMember(t *testing.T) {
	rt := newRuntime(t)
	newChainFixture(t, rt, "halted")
	setChainFlowState(t, rt, "x", "halted",
		workflow.State{
			Status: workflow.StatusHalted, At: "review",
			Awaiting: workflow.Awaiting{Step: "review", Member: "reviewer", Round: 2},
			Iter:     map[string]workflow.Iter{"plans": {Index: 1, Items: []string{"p1", "p2", "p3", "p4"}}},
		}, 2, 4)

	if err := rt.Store.AppendLog("x-rev", store.LogEntry{
		TS: time.Now().UTC(), Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport,
	}); err != nil {
		t.Fatalf("plant the stranded report: %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	f := rep.Bindings[0].Chain
	if len(f.PendingMembers) != 1 || f.PendingMembers[0] != "x-rev" {
		t.Errorf("pending members = %v, want [x-rev] for a halted chain", f.PendingMembers)
	}
	if got := view.ChainPendingSegment(*f); got != "pending on x-rev" {
		t.Errorf("ChainPendingSegment = %q, want %q", got, "pending on x-rev")
	}
}

// TestChainFlowPlanPosExhausted pins the helper itself, so the two status
// surfaces it serves cannot drift apart: an exhausted walk reads its last plan,
// an in-flight walk reads its own index, and an empty plan input clamps to 1.
func TestChainFlowPlanPosExhausted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		it         workflow.Iter
		pos, total int
	}{
		{"exhausted", workflow.Iter{Index: -1, Items: []string{"a", "b", "c"}, Done: true}, 3, 3},
		{"mid-walk", workflow.Iter{Index: 1, Items: []string{"a", "b", "c"}}, 2, 3},
		{"before the first item", workflow.Iter{Index: -1, Items: []string{"a", "b", "c"}}, 1, 3},
		{"no plans", workflow.Iter{Index: -1, Done: true}, 1, 0},
		{"exhausted with no items", workflow.Iter{Index: -1, Done: true}, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := workflow.State{Iter: map[string]workflow.Iter{"plans": tc.it}}
			pos, total := chainFlowPlanPos(st)
			if pos != tc.pos || total != tc.total {
				t.Errorf("chainFlowPlanPos = %d/%d, want %d/%d", pos, total, tc.pos, tc.total)
			}
		})
	}
}

// TestChainDocDoneChainShowsFinalPlanCount pins the chains-doc reader, the
// surface `relevo status --chains` prints: the same exhausted walk projects to
// its last plan there too, so the two surfaces cannot disagree.
func TestChainDocDoneChainShowsFinalPlanCount(t *testing.T) {
	rt := newRuntime(t)
	newChainFixture(t, rt, "done")
	setChainFlowState(t, rt, "x", "done",
		exhaustedPlansState(workflow.StatusDone, "", 6), 6, 6)

	c, err := rt.Store.Chain("x")
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	_, _, pos, total := chainDocStepsAndFacts(c, nil, nil)
	if pos != 6 || total != 6 {
		t.Errorf("chains-doc plan position = %d/%d, want 6/6 for an exhausted walk", pos, total)
	}
}
