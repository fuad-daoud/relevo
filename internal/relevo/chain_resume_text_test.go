package relevo

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// startGatedBuildHaltedChain starts the shipped workflow with a gate and halts
// it at build through the builder's own halted close, so the halt awaits the
// builder's round 1 and a later manual round is reviewed rather than refused:
// a repair halt awaits the check itself, so no member close is ever picked up.
func startGatedBuildHaltedChain(t *testing.T, rt Runtime) {
	t.Helper()
	body, err := json.Marshal(workflow.Default())
	if err != nil {
		t.Fatalf("encode the shipped workflow: %v", err)
	}
	startedChain(t, rt, ChainOptions{
		Workflow: writeWorkflowFile(t, string(body)),
		Params:   map[string]string{"gate": "make check", "scan": "false"},
	})
	chainBuilderClose(t, rt, "shop", chainHaltedBody("stuck"))
	if row := flowChainRow(t, rt); row.Status != string(workflow.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
}

// TestResumeAwaitingTextNamesTheCheckAndManualRound pins the resume's awaiting
// line for a gated chain whose resume reviewed a human round: the engine parks
// on the check with no member round, so the line names the check step from the
// engine state and the reviewed manual round, not the columns' builder round
// 0. A served-style row without a manual round names the check alone, and any
// other state or a row without state keeps the columns line byte-identical.
func TestResumeAwaitingTextNamesTheCheckAndManualRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startGatedBuildHaltedChain(t, rt)
	halted := flowChainRow(t, rt)

	if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
		t.Fatalf("Send after the halt: %v", err)
	}
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	if row := flowChainRow(t, rt); row.Status != string(workflow.StatusHalted) {
		t.Fatalf("chain status = %q, want it still halted after a manual round", row.Status)
	}

	res, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"})
	if err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if res.ManualRound != 2 {
		t.Fatalf("ManualRound = %d, want 2: the resume reviewed the human round", res.ManualRound)
	}
	st, err := chain.WorkflowState(res.Chain)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if st.Awaiting.Step != "check" || st.Awaiting.Member != "" || st.Awaiting.Round != 0 {
		t.Fatalf("engine awaiting = %+v, want a check-await with no member round", st.Awaiting)
	}

	if got := ResumeAwaitingText(res.Chain, res.ManualRound); got != "awaiting check check on manual round 2" {
		t.Errorf("ResumeAwaitingText = %q, want the check step and the reviewed round", got)
	}

	// A served-style row carries the same engine state with no manual round:
	// the line names the check without a number.
	view := chainServerView("shop", seedRemoteBase)
	view.Workflow, view.State = res.Chain.WorkflowJSON, res.Chain.StateJSON
	view.AwaitingRound = 0
	mirror, err := chainRowFromView(db.ChainRow{Name: "shop"}, view, time.Now())
	if err != nil {
		t.Fatalf("chainRowFromView: %v", err)
	}
	if got := ResumeAwaitingText(mirror, 0); got != "awaiting check check" {
		t.Errorf("ResumeAwaitingText served-style = %q, want the check without a manual round", got)
	}

	// A run-await keeps the columns line: the halted row waits on the
	// builder's round 1.
	if got := ResumeAwaitingText(halted, 0); got != "awaiting builder round 1" {
		t.Errorf("ResumeAwaitingText run-await = %q, want the columns line", got)
	}

	// A row without state has no engine to read: the columns line stands.
	bare := db.ChainRow{Name: "shop", AwaitingMember: chain.MemberBuilder, AwaitingRound: 5}
	if got := ResumeAwaitingText(bare, 0); got != "awaiting builder round 5" {
		t.Errorf("ResumeAwaitingText stateless = %q, want the columns line", got)
	}
}
