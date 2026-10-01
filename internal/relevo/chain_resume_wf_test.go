package relevo

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// flowBranchWorkflow reaches a second actor only when its when step's param is
// on, so a resume that turns the param on must create that actor's member.
const flowBranchWorkflow = `name: branchwf
inputs: { plans: required }
params: { scan: false }
start: plans
steps:
  plans: { for-each: plans, on: { next: gate, empty: done } }
  gate: { when: "{{params.scan}}", on: { true: scan, false: build } }
  scan: { run: researcher, seed: "scan it", on: { done: build } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: done } }
`

// haltFlowChain drives shop to a halt on its review step: the builder's close
// routes to the review, and the review's unmatched close ends the chain, so a
// resume has a halted chain to continue.
func haltFlowChain(t *testing.T, rt Runtime) {
	t.Helper()
	startFlowChain(t, rt, flowReviewWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "review", Member: "assistant", Round: 1, Status: "halted", Reason: "no"})
}

// TestWorkflowResumeUnknownStepListsSteps pins the unknown-step refusal: a
// resume --from a step the workflow does not have names the step and lists the
// ones it does.
func TestWorkflowResumeUnknownStepListsSteps(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltFlowChain(t, rt)

	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", From: "nope"})
	if err == nil {
		t.Fatal("ChainResume --from nope = nil, want the unknown-step refusal")
	}
	for _, want := range []string{"nope", "plans", "build", "review"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}

// TestWorkflowResumeTakesNewerManualRound pins the manual-round rule: a round
// the chain's member closed after the chain stopped -- a send a human made
// while it was down -- is fed as that run step's step_closed, so its outcome
// routes the chain on rather than being thrown away.
func TestWorkflowResumeTakesNewerManualRound(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}

	// The manual round: sent while the chain was stopped, closed done. Its
	// close is not a chain transition, so the chain stays where it is.
	if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
		t.Fatalf("Send the manual round: %v", err)
	}
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	if row := flowChainRow(t, rt); row.Status != string(workflow.StatusStopped) {
		t.Fatalf("status = %s, want it still stopped after the manual round", row.Status)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusRunning) {
		t.Fatalf("status = %s (%q), want running", row.Status, row.Reason)
	}
	st, err := chainWorkflowState(row)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.Awaiting.Step != "review" || st.Awaiting.Member != "assistant" || st.Awaiting.Round != 1 {
		t.Fatalf("awaiting = %+v, want review/assistant round 1: the manual round's done close routes to the review", st.Awaiting)
	}
	b, err := rt.Store.Load("shop")
	if err != nil {
		t.Fatalf("Load shop: %v", err)
	}
	if b.Round != 3 {
		t.Errorf("builder round = %d, want 3: the manual round closed and must not be re-run", b.Round)
	}
}

// TestWorkflowResumeResendsStoppedPrompt pins the stopped re-send: a resume of
// the round the chain was stopped on hands the member that round's own staged
// prompt, not a seed rendered again.
func TestWorkflowResumeResendsStoppedPrompt(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}

	// Make the stopped round's staged bytes distinctive, so a re-rendered seed
	// could not stand in for them.
	const staged = "the stopped round's own prompt\n"
	if err := os.WriteFile(rt.Store.PromptPath("shop", 1), []byte(staged), 0o644); err != nil {
		t.Fatalf("plant the stopped prompt: %v", err)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	got, err := rt.Store.ReadFile(rt.Store.PromptPath("shop", 2))
	if err != nil {
		t.Fatalf("read the resumed prompt: %v", err)
	}
	if string(got) != staged {
		t.Errorf("the resumed round's prompt = %q, want the stopped round's own staged prompt %q", got, staged)
	}
	row := flowChainRow(t, rt)
	st, err := chainWorkflowState(row)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if row.Status != string(workflow.StatusRunning) || st.Awaiting.Step != "build" || st.Awaiting.Round != 2 {
		t.Errorf("chain = %s awaiting %+v, want running on build round 2", row.Status, st.Awaiting)
	}
}

// TestWorkflowResumeParamCreatesMissingMember pins Q2: a resume that turns a
// branch on creates the member for the actor the branch reaches, which the
// start never made.
func TestWorkflowResumeParamCreatesMissingMember(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowBranchWorkflow)
	if _, err := rt.Store.Load("shop-researcher"); err == nil {
		t.Fatal("test premise: the researcher member exists before the scan branch is on")
	}
	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Params: map[string]string{"scan": "true"}}); err != nil {
		t.Fatalf("ChainResume --param scan=true: %v", err)
	}

	if _, err := rt.Store.Load("shop-researcher"); err != nil {
		t.Fatalf("Load shop-researcher after the resume: %v", err)
	}
	rows, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers: %v", err)
	}
	found := false
	for _, m := range rows {
		if m.Binding == "shop-researcher" {
			found = true
		}
	}
	if !found {
		t.Errorf("chain members = %+v, want shop-researcher among them", rows)
	}
}

// TestPlacedWriterCheckAnsweredWhenTheGateRecordArrivesLater pins the pending
// placed check: a check that starts with no pulled gate record leaves the chain
// awaiting it, and the next tick answers it from the record a later pull
// installs on the writer's round.
func TestPlacedWriterCheckAnsweredWhenTheGateRecordArrivesLater(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)

	// The check begins with no pulled gate record: the chain is left awaiting
	// the check, with no local run of its own.
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.Awaiting.Step != "check" || st.Awaiting.Member != "" || st.Awaiting.Run != 0 {
		t.Fatalf("awaiting = %+v, want the check step with no run", st.Awaiting)
	}
	if _, err := rt.Store.ChainCheck("shop", 1); err == nil {
		t.Error("a local check run exists on a placed writer")
	}

	// The pull installs the writer's round with the gate record it carried.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("shop", store.LogEntry{
			TS: baseTime, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Path: "/x/001-report.md",
			Gate: &store.GateRecord{Result: "pass", LogPath: "/x/check.log"},
		})
	}); err != nil {
		t.Fatalf("append the pulled gate: %v", err)
	}

	// The next tick answers the pending check and routes it.
	tickChainChecks(context.Background(), rt)
	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusDone) {
		t.Errorf("status = %s (%q), want done from the pulled gate", got.Status, got.Reason)
	}
}

// flowTriageBuildWorkflow gives a custom workflow a yes-no reader and a
// builder: the builder is the chain's keeper writer, so it takes the chain's
// own name, and the reader fills no legacy part, so a resume's awaited member
// must resolve through chain_member.
const flowTriageBuildWorkflow = `name: triagebuild
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: triage } }
  triage: { run: yes-no, seed: "ship it?", on: { done: done } }
`

// flowTriageRemoteRows is the chain roles file with a yes-no reader added and
// the builder placed on zen, so a custom workflow's keeper writer runs remotely.
func flowTriageRemoteRows() map[string]roles.Row {
	rows := chainRows()
	rows["yes-no"] = roles.Row{
		Shape:       ptr("reader"),
		Candidates:  []string{testClaudeRef},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "yes-no"}},
	}
	b := rows["builder"]
	b.Placement = []string{"zen"}
	rows["builder"] = b
	return rows
}

// TestWorkflowResumeShipsAPlacedCustomMembersRound pins the resume's ship: a
// custom workflow whose awaited member is placed remotely stages the resumed
// round, and the unlocked pending-send step hands it to the server.
func TestWorkflowResumeShipsAPlacedCustomMembersRound(t *testing.T) {
	t.Parallel()

	fr := chainRemoteFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, flowTriageRemoteRows())

	startFlowChain(t, rt, flowTriageBuildWorkflow)
	advanceRemoteChain(t, rt, 2)
	haltRemoteChain(t, rt)

	fr.calls = nil
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if !slices.Contains(fr.calls, "StartRound:zen:shop:2") {
		t.Errorf("calls = %v, want the resumed round shipped to zen", fr.calls)
	}
}
