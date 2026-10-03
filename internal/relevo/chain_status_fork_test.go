package relevo

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// flowEvent builds a workflow trace row the way ChainTrace decodes a stored one.
func flowEvent(e workflow.Event, a workflow.Action) ChainTraceEvent {
	return ChainTraceEvent{
		Seq: 1, TS: time.Time{}, Step: "fork",
		Flow: &e, FlowAction: &a,
	}
}

// TestForkChildrenOrderNumericallyWithTenKeys pins that the key order is the
// declaration order at any width: with children "1".."12" a plain string
// comparison would read 1, 10, 11, 12, 2, 3, ... Both children the rows the
// nested status listing threads under the parent and the names the chain
// facts' own child list carries come through sortChainRowsByKey, so both are
// asserted here.
func TestForkChildrenOrderNumericallyWithTenKeys(t *testing.T) {
	rt := newRuntime(t)
	parent, _ := newChainFixture(t, rt, "running")
	keys := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"}
	for _, k := range keys {
		newForkChildFixture(t, rt, parent.Name, k, "running")
	}

	c, err := rt.Store.Chain(parent.Name)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	children, _ := chainChildRows(rt.Store, c)
	got := make([]string, len(children))
	for i, ch := range children {
		got[i] = ch.Name
	}
	want := make([]string, len(keys))
	for i, k := range keys {
		want[i] = parent.Name + "." + k
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("chainChildRows = %v, want numeric declaration order %v", got, want)
	}

	var f view.ChainFacts
	chainChildFacts(rt.Store, &f, c)
	if !reflect.DeepEqual(f.Children, want) {
		t.Errorf("ChainFacts.Children = %v, want numeric declaration order %v", f.Children, want)
	}
}

func TestTraceLineNamesTheChildrenAForkStarted(t *testing.T) {
	e := workflow.Event{Kind: workflow.EventStepClosed, Step: "fork"}
	a := workflow.Action{
		Kind: workflow.ActionFork,
		Step: "fork",
		Children: []workflow.ChildSpec{
			{Key: "1", Workflow: "child"}, {Key: "2", Workflow: "child"},
		},
	}
	if got, want := flowTraceLine(flowEvent(e, a)), "fork r0 → forked 1, 2"; got != want {
		t.Errorf("fork trace line = %q, want %q", got, want)
	}
}

func TestTraceLineNamesAChildEnd(t *testing.T) {
	e := workflow.Event{Kind: workflow.EventChildEnded, Step: "fork", Child: "1", Status: "done"}
	a := workflow.Action{Kind: workflow.ActionSend, Step: "build"}
	if got, want := flowTraceLine(flowEvent(e, a)), "child 1 done → build"; got != want {
		t.Errorf("child-end trace line = %q, want %q", got, want)
	}
}

// The merge row keeps the result word: a trace that dropped it would read a
// conflict as a clean join.
func TestTraceLineNamesTheMergeResult(t *testing.T) {
	e := workflow.Event{Kind: workflow.EventMergeClosed, Step: "fork", Result: "joined"}
	a := workflow.Action{Kind: workflow.ActionSend, Step: "build", Reason: "merged 1, 2 → joined"}
	if got, want := flowTraceLine(flowEvent(e, a)), "merged 1, 2 → joined → build"; got != want {
		t.Errorf("merge trace line = %q, want %q", got, want)
	}
}

func TestTraceLineCountsTheConflictPaths(t *testing.T) {
	e := workflow.Event{
		Kind: workflow.EventMergeClosed, Step: "fork", Result: "conflict",
		Artifacts: map[string][]string{"conflict": {"a.go", "b.go"}},
	}
	a := workflow.Action{Kind: workflow.ActionHalt, Step: "fork", Reason: "merged 1 → conflict"}
	if got, want := flowTraceLine(flowEvent(e, a)), "merged → conflict (2 paths) → halt"; got != want {
		t.Errorf("conflict trace line = %q, want %q", got, want)
	}
}

// A child reads its own trace, and the header says which parent forked it. A
// chain with no parent prints no header, so its trace is byte-identical to the
// text it had before forks existed.
func TestRenderTraceNamesAChildsParentInTheHeader(t *testing.T) {
	child := ChainTraceDoc{Name: "cc.1", Status: "done", Parent: "cc"}
	if got, want := RenderTrace(child), "child of cc\n"; got != want {
		t.Errorf("child trace = %q, want %q", got, want)
	}
	parent := ChainTraceDoc{Name: "cc", Status: "running"}
	if strings.Contains(RenderTrace(parent), "child of") {
		t.Errorf("a parent chain's trace grew a child header:\n%s", RenderTrace(parent))
	}
}
