package relevo

import (
	"context"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// dirtyHaltedChain halts shop's build round on uncommitted work and returns the
// builder's round afterwards.
func dirtyHaltedChain(t *testing.T) (Runtime, *fakeGit, int) {
	t.Helper()
	rt, fg := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	fg.dirtyResult = true
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) || !chainHaltedOverUncommittedWork(c.Reason) {
		t.Fatalf("setup: status = %s (%q), want halted over uncommitted work", c.Status, c.Reason)
	}
	return rt, fg, chainBinding(t, rt, "shop").Round
}

func staged(rt Runtime, round int) bool {
	_, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", round))
	return err == nil
}

func TestResumeAfterCommitAdvancesWithoutResending(t *testing.T) {
	t.Parallel()

	rt, fg, round := dirtyHaltedChain(t)
	fg.dirtyResult = false
	fg.headCommitID = "commit-after-the-work"

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	row := flowChainRow(t, rt)
	st, err := chain.WorkflowState(row)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.Awaiting.Step != "review" || row.Status != string(workflow.StatusRunning) {
		t.Fatalf("chain = %s awaiting %+v (%q), want running on review", row.Status, st.Awaiting, row.Reason)
	}
	if staged(rt, round) {
		t.Errorf("a prompt was staged for round %d: the plan was re-sent", round)
	}
}

func TestResumeWhileTreeStillDirtyRefuses(t *testing.T) {
	t.Parallel()

	rt, fg, round := dirtyHaltedChain(t)
	fg.headCommitID = "commit-after-the-work"

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil || !strings.Contains(err.Error(), "still halted over uncommitted work") {
		t.Fatalf("ChainResume = %v, want the uncommitted-work refusal", err)
	}
	if staged(rt, round) {
		t.Errorf("a prompt was staged for round %d: something was sent", round)
	}
}

func TestResumeCleanTreeAtBaselineRefuses(t *testing.T) {
	t.Parallel()

	rt, fg, round := dirtyHaltedChain(t)
	fg.dirtyResult = false
	if chainBinding(t, rt, "shop").RoundClosedBaselineHead == "" {
		t.Fatal("setup: the closed round kept no baseline HEAD")
	}

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil || !strings.Contains(err.Error(), chainUncommittedWorkClause) {
		t.Fatalf("ChainResume = %v, want the uncommitted-work refusal", err)
	}
	if staged(rt, round) {
		t.Errorf("a prompt was staged for round %d: something was sent", round)
	}
}

func TestResumeCleanTreeWithUnknownBaselineRefuses(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	fg.headCommitID = ""
	startFlowChain(t, rt, flowReviewWorkflow)
	fg.dirtyResult = true
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	fg.dirtyResult = false
	fg.headCommitID = "commit-after-the-work"
	round := chainBinding(t, rt, "shop").Round

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err == nil || !strings.Contains(err.Error(), "starting commit is unknown") {
		t.Fatalf("ChainResume = %v, want the unknown-baseline refusal", err)
	}
	if staged(rt, round) {
		t.Errorf("a prompt was staged for round %d: something was sent", round)
	}
}

func TestResumeOfAnotherHaltReSendsEvenOverADirtyTree(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	chainBuilderClose(t, rt, "shop", chainHaltedBody("stuck"))
	if c := flowChainRow(t, rt); c.Status != string(workflow.StatusHalted) || chainHaltedOverUncommittedWork(c.Reason) {
		t.Fatalf("setup: status = %s (%q), want halted for another reason", c.Status, c.Reason)
	}
	fg.dirtyResult = true
	before := chainBinding(t, rt, "shop").Round

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if !staged(rt, before) {
		t.Errorf("no prompt was staged for round %d: the step was not re-run", before)
	}
}
