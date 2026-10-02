package relevo

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// forkAwaitingParent is a fork parent still waiting on both children.
func forkAwaitingParent(t *testing.T, rt Runtime) {
	t.Helper()
	startedChain(t, rt, ChainOptions{
		Name:     "shop",
		Workflow: writeWorkflowFile(t, forkFixedWorkflow(writeWorkflowFile(t, childWorkflowBody))),
	})
}

// forkHaltedParent is a fork parent halted because one child halted and its
// sibling then ended: the state a child's resume has to answer.
func forkHaltedParent(t *testing.T, rt Runtime) db.ChainRow {
	t.Helper()
	forkAwaitingParent(t, rt)
	chainBuilderClose(t, rt, "shop.1", chainHaltedBody("the disk is on fire"))
	chainBuilderClose(t, rt, "shop.2", chainDoneBody())
	parent := chainStoredRow(t, rt, "shop")
	if parent.Status != string(workflow.StatusHalted) {
		t.Fatalf("parent status = %q (%q), want halted once every child ended", parent.Status, parent.Reason)
	}
	if !strings.Contains(parent.Reason, "the disk is on fire") {
		t.Fatalf("parent reason = %q, want the halted child's own reason", parent.Reason)
	}
	return parent
}

// forkHaltedAtJoin brings a fork to the state a parent resume is for: every child
// ended done, and the merge that follows them halted instead of completing, so
// the join is still owed. The merge failure is scripted so the parent's own merge
// cannot have run; the test clears it before the resume, which is the human
// fixing what stopped the merge.
func forkHaltedAtJoin(t *testing.T, rt Runtime, fg *fakeGit) {
	t.Helper()
	fg.mergeKeepErr = errors.New("the index is locked")
	forkAwaitingParent(t, rt)
	forkEndChild(t, rt, "shop.1", "done", "")
	forkEndChild(t, rt, "shop.2", "done", "")

	c := chainStoredRow(t, rt, "shop")
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("parent status = %q (%q), want the failed merge to halt it", c.Status, c.Reason)
	}
	st, err := chainWorkflowState(c)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if !st.Awaiting.Merging {
		t.Fatalf("parent awaiting %+v, want it left merging with every child ended", st.Awaiting)
	}
	if got := forkUndoneChildren(st); len(got) != 0 {
		t.Fatalf("children still standing = %q, want none", got)
	}
	fg.mergeKeepErr = nil
}

// forkReasons is every reason the named chain's trace rows carry.
func forkReasons(t *testing.T, rt Runtime, name string) []string {
	t.Helper()
	rows, err := rt.Store.ChainEvents(name)
	if err != nil {
		t.Fatalf("ChainEvents(%s): %v", name, err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Reason)
	}
	return out
}

// forkHasReason reports whether one of the chain's trace rows carries the reason.
func forkHasReason(t *testing.T, rt Runtime, name, want string) bool {
	t.Helper()
	for _, r := range forkReasons(t, rt, name) {
		if r == want {
			return true
		}
	}
	return false
}

// TestForkChildResumeReopensTheParentAndRejoins is the whole round: a child
// halts, its sibling finishes, the parent halts with the child's reason, and
// resuming the child to done re-opens the parent -- which then joins.
func TestForkChildResumeReopensTheParentAndRejoins(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	forkHaltedParent(t, rt)

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop.1"}); err != nil {
		t.Fatalf("ChainResume shop.1: %v", err)
	}

	reopened := chainStoredRow(t, rt, "shop")
	st, err := chainWorkflowState(reopened)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if reopened.Status != string(workflow.StatusRunning) {
		t.Fatalf("parent status = %q (%q), want running after the child's resume", reopened.Status, reopened.Reason)
	}
	if st.Reason != "" {
		t.Errorf("parent reason = %q, want it cleared by the re-open", st.Reason)
	}
	if _, ended := st.Awaiting.Ended["1"]; ended {
		t.Errorf("parent ended = %+v, want the re-opened child removed", st.Awaiting.Ended)
	}
	if _, ended := st.Awaiting.Ended["2"]; !ended {
		t.Errorf("parent ended = %+v, want the finished sibling kept", st.Awaiting.Ended)
	}
	if st.Awaiting.Merging {
		t.Error("parent is merging with one child still running")
	}
	if !forkHasReason(t, rt, "shop", "reopened by child 1") {
		t.Errorf("parent trace reasons = %q, want the re-open recorded", forkReasons(t, rt, "shop"))
	}

	chainBuilderClose(t, rt, "shop.1", chainDoneBody())

	st, err = chainWorkflowState(chainStoredRow(t, rt, "shop"))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if got := st.Results["split"]; got.Status != chainMergeJoined {
		t.Errorf("fork result = %+v, want joined once the re-opened child ended done", got)
	}
	if st.Awaiting.Step != "merge" {
		t.Errorf("parent awaiting %+v, want the joined edge's merge step", st.Awaiting)
	}
}

// TestForkChildResumeWithStoppedParentRefused pins the refusal for a parent that
// has already stopped: nothing re-opens it, so the child cannot run on under it.
func TestForkChildResumeWithStoppedParentRefused(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	forkAwaitingParent(t, rt)

	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}
	if got := chainStoredRow(t, rt, "shop").Status; got != string(workflow.StatusStopped) {
		t.Fatalf("parent status = %q, want stopped", got)
	}
	before := chainStoredRow(t, rt, "shop.1")

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop.1"})
	if err == nil {
		t.Fatal("ChainResume shop.1 = nil, want the stopped parent's refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, "shop.1") || !strings.Contains(msg, "stopped") {
		t.Errorf("refusal = %q, want it to name the child and the parent's status", msg)
	}
	if !strings.Contains(msg, "--name shop") {
		t.Errorf("refusal = %q, want it to name the command that does work", msg)
	}
	if after := chainStoredRow(t, rt, "shop.1"); !reflect.DeepEqual(after, before) {
		t.Errorf("child row = %+v, want it untouched by the refusal (%+v)", after, before)
	}
}

// TestForkChildResumeWithHaltingParentRefused pins the refusal for a parent
// halted for a reason the child does not answer: it halted before any child
// ended, so the child's own end was discarded by the engine's own replay guard.
func TestForkChildResumeWithHaltingParentRefused(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	forkAwaitingParent(t, rt)

	// A human halts the parent at the fork, before any child has ended.
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventNeedsYou, Step: "split", Reason: "a human stopped it"})
	if got := chainStoredRow(t, rt, "shop").Status; got != string(workflow.StatusHalted) {
		t.Fatalf("parent status = %q, want halted at the fork", got)
	}
	// The child halts afterwards; the parent is terminal, so the close changes
	// nothing and the parent has no record of this child at all.
	forkEndChild(t, rt, "shop.1", "halted", "the disk is on fire")
	if got := forkUndoneChildren(mustChainState(t, chainStoredRow(t, rt, "shop"))); len(got) != 2 {
		t.Fatalf("children the parent knows ended = %q, want none", got)
	}
	before := chainStoredRow(t, rt, "shop.1")

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop.1"})
	if err == nil {
		t.Fatal("ChainResume shop.1 = nil, want the halting parent's refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, "halted") || !strings.Contains(msg, "split") {
		t.Errorf("refusal = %q, want it to name the parent's status and the step", msg)
	}
	if after := chainStoredRow(t, rt, "shop.1"); !reflect.DeepEqual(after, before) {
		t.Errorf("child row = %+v, want it untouched by the refusal (%+v)", after, before)
	}
}

// TestForkParentResumeWithHaltedChildRefused pins the refusal for a parent
// resume while a child is standing: it names the child, because resuming the
// child is what re-opens the parent.
func TestForkParentResumeWithHaltedChildRefused(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	forkHaltedParent(t, rt)
	before := chainStoredRow(t, rt, "shop")

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil {
		t.Fatal("ChainResume shop = nil, want the halted child's refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, "shop.1") {
		t.Errorf("refusal = %q, want it to name the halted child", msg)
	}
	if !strings.Contains(msg, "split") {
		t.Errorf("refusal = %q, want it to name the fork step", msg)
	}
	if after := chainStoredRow(t, rt, "shop"); !reflect.DeepEqual(after, before) {
		t.Errorf("parent row = %+v, want it untouched by the refusal (%+v)", after, before)
	}
	// A refused resume must not have re-forked either: no new children.
	if _, err := rt.Store.Chain("shop.3"); err == nil {
		t.Error("the refused resume forked a third child")
	}
	// The parent's halt told the MasterMind it needs a human. A refused resume
	// does not take that back: the human has still not acted.
	if got := chainPendingChain(t, rt, "shop"); len(got) != 1 {
		t.Errorf("the parent's pending end = %d, want its halt still undelivered", len(got))
	}
}

// TestForkParentResumeReentersTheJoin pins the other half of the parent resume:
// with every child ended done the fork has earned its join, so the resume runs
// the merge that had halted.
func TestForkParentResumeReentersTheJoin(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	forkHaltedAtJoin(t, rt, fg)
	merges := len(fg.mergeKeepCalls)

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume shop: %v", err)
	}

	if got := len(fg.mergeKeepCalls) - merges; got != 2 {
		t.Errorf("merge calls = %d, want the resume to merge both children again", got)
	}
	st, err := chainWorkflowState(chainStoredRow(t, rt, "shop"))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if got := st.Results["split"]; got.Status != chainMergeJoined {
		t.Errorf("fork result = %+v, want joined from the resume's merge", got)
	}
	if st.Awaiting.Step != "merge" {
		t.Errorf("parent awaiting %+v, want the joined edge's merge step", st.Awaiting)
	}
}

// TestForkParentResumeNeverForksAgain pins that a parent resume never re-enters
// the fork step: the children the fork started are still the children it waits
// on, on the same names, and the merge runs on their branches.
func TestForkParentResumeNeverForksAgain(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	forkHaltedAtJoin(t, rt, fg)
	worktrees := len(fg.addWorktreeCalls)
	merges := len(fg.mergeKeepCalls)

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume shop: %v", err)
	}

	if got := len(fg.addWorktreeCalls) - worktrees; got != 0 {
		t.Errorf("the resume cut %d worktrees, want none: it must not re-fork", got)
	}
	if got := len(fg.mergeKeepCalls) - merges; got != 2 {
		t.Errorf("merge calls = %d, want the one merge over the original two children", got)
	}
	for _, ref := range fg.mergeKeepCalls[merges:] {
		if !strings.HasPrefix(ref.Ref, "relevo/shop.") {
			t.Errorf("merged %q, want a branch of the children the fork started", ref.Ref)
		}
	}
}

// mustChainState reads a chain's stored engine state.
func mustChainState(t *testing.T, c db.ChainRow) workflow.State {
	t.Helper()
	st, err := chainWorkflowState(c)
	if err != nil {
		t.Fatalf("chainWorkflowState(%s): %v", c.Name, err)
	}
	return st
}
