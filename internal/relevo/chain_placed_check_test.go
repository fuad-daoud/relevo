package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainCheckFake is chainRemoteFake on a server that also runs a placed
// writer's check itself.
func chainCheckFake() *fakeRemote {
	fr := chainRemoteFake()
	fr.whoAmIResp.Features = append(fr.whoAmIResp.Features, remote.FeatureCheck)
	return fr
}

// chainSetWorkflowState rewrites shop's stored workflow state.
func chainSetWorkflowState(t *testing.T, rt Runtime, edit func(st *workflow.State)) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		st, err := chain.WorkflowState(c)
		if err != nil {
			return err
		}
		edit(&st)
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		c.StateJSON = raw
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("store the chain state: %v", err)
	}
}

// chainAwaitingWithoutRun drops the run id from the chain's awaiting check. That
// is the shape a chain started before the check route carries: the client had
// nowhere to record a run id then, so the tick has only the pulled record to
// answer from.
func chainAwaitingWithoutRun(t *testing.T, rt Runtime) {
	t.Helper()
	chainSetWorkflowState(t, rt, func(st *workflow.State) { st.Awaiting.Run = 0 })
}

// chainAwaitedCheck returns the chain's awaiting check step and run.
func chainAwaitedCheck(t *testing.T, rt Runtime) (string, int) {
	t.Helper()
	st, err := chain.WorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	return st.Awaiting.Step, st.Awaiting.Run
}

// chainLastCheckClosed returns the newest check_closed event trace row for the
// chain's check step.
func chainLastCheckClosed(t *testing.T, rt Runtime, step string) db.ChainEventRow {
	t.Helper()
	rows, err := rt.Store.ChainEvents("shop")
	if err != nil {
		t.Fatalf("ChainEvents: %v", err)
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Step != step {
			continue
		}
		if ev, derr := workflow.DecodeEvent(rows[i].Event); derr == nil && ev.Kind == workflow.EventCheckClosed {
			return rows[i]
		}
	}
	t.Fatalf("no check_closed trace row for step %q", step)
	return db.ChainEventRow{}
}

// chainPulledGate plants the gate record a pull installs on the writer's round:
// the only answer a chain started before the check route can get.
func chainPulledGate(t *testing.T, rt Runtime, result string) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("shop", store.LogEntry{
			TS: baseTime, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Path: "/x/001-report.md",
			Gate: &store.GateRecord{Result: result, LogPath: "/x/check.log"},
		})
	}); err != nil {
		t.Fatalf("append the pulled gate: %v", err)
	}
}

// flowGateParamWorkflow is flowPlacedCheckWorkflow with the check command in a
// gate param, the slot the --gate flag fills. A custom workflow keeps its own
// param defaults, so the placed writer's binding carries that command as its
// gate however empty policy's own default is.
const flowGateParamWorkflow = `name: gateparam
inputs: { plans: required }
params:
  gate: make check
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: check } }
  check: { check: "{{params.gate}}", on: { green: done, red: { halt: "red" } } }
`

// flowNoGateWorkflow has a gate param a resume may set, but no check step that
// renders a command, so a placed writer's binding carries no gate of its own.
const flowNoGateWorkflow = `name: nogate
inputs: { plans: required }
params:
  gate: make check
start: plans
steps:
  plans: { for-each: plans, on: { next: build, empty: done } }
  build: { run: builder, seed: "{{plans.current}}", on: { done: done } }
`

// TestPlacedCheckRunsViaRoute pins a placed writer's check on a server that can
// run it: the client mints the run id and posts the check itself, records the
// run it is awaiting, and the next tick closes the check from the polled result
// instead of from a pulled gate record.
func TestPlacedCheckRunsViaRoute(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)

	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	if !slices.Contains(fr.calls, "CreateCheck:zen:shop:chk-1") {
		t.Fatalf("calls = %v, want the placed check posted to the check route", fr.calls)
	}
	if req := fr.createCheckReq; req.Command != "make check" || req.Step != "check" {
		t.Errorf("check request = %+v, want step check running make check", req)
	}
	if step, run := chainAwaitedCheck(t, rt); step != "check" || run != 1 {
		t.Fatalf("awaiting check = %q run %d, want check run 1", step, run)
	}

	// The run settles on the server and the next tick answers the chain with it.
	fr.getCheckResp = remote.CheckView{ID: "chk-1", Result: "pass", LogTail: "all green\n"}
	tickChainChecks(context.Background(), rt)

	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusDone) {
		t.Errorf("status = %s (%q), want done from the polled pass", got.Status, got.Reason)
	}
	if _, err := rt.Store.ChainCheck("shop", 1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a local check run exists on a placed writer: %v", err)
	}
}

// TestPlacedCheckRedFeedsRepeatRed pins that a polled failure is fed through the
// same driver as a local check: the red closes on the step, its returned tail is
// sealed as the step's log, and the repeat-red rule sees it as the same failure
// the previous red under this for-each item produced.
func TestPlacedCheckRedFeedsRepeatRed(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	// The step has already gone red under this for-each item, with a log the
	// store can still read.
	prevLog := filepath.Join(t.TempDir(), "check.log")
	if err := os.WriteFile(prevLog, []byte("the same failure\n"), 0o644); err != nil {
		t.Fatalf("write the previous red's log: %v", err)
	}
	chainSetWorkflowState(t, rt, func(st *workflow.State) {
		if st.Results == nil {
			st.Results = map[string]workflow.Result{}
		}
		st.Results["check"] = workflow.Result{
			Status:    chainCheckRed,
			Artifacts: map[string][]string{"log": {prevLog}},
		}
	})
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.ChainEventAppend("shop", db.ChainEventRow{
			TS: baseTime, Step: "check", Plan: flowPlanPos(func() workflow.State {
				st, _ := chain.WorkflowState(flowChainRow(t, rt))
				return st
			}()),
			Event:  workflow.EncodeEvent(workflow.Event{Kind: workflow.EventCheckClosed, Step: "check", Run: 1, Result: chainCheckRed, Log: prevLog}),
			Action: workflow.EncodeAction(workflow.Action{Kind: workflow.ActionHalt, Step: "check"}),
		})
	}); err != nil {
		t.Fatalf("plant the previous red's trace: %v", err)
	}

	fr.getCheckResp = remote.CheckView{ID: "chk-1", Result: "fail", LogTail: "the same failure\n"}
	tickChainChecks(context.Background(), rt)

	row := chainLastCheckClosed(t, rt, "check")
	ev, err := workflow.DecodeEvent(row.Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Result != chainCheckRed {
		t.Errorf("closed result = %q, want the red a polled fail means", ev.Result)
	}
	if ev.Log == "" {
		t.Error("the polled tail sealed no log for the red")
	}
	if !ev.RepeatRed {
		t.Error("the polled red did not feed the repeat-red rule: an identical tail repeats the previous red")
	}
}

// TestPlacedCheckUnreachableKeepsAwaiting pins that a server that cannot be
// reached is not a failure: the chain stays awaiting its run, because the
// check may well be running there and only the answer is late.
func TestPlacedCheckUnreachableKeepsAwaiting(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	fr.getCheckErr = errors.New("dial tcp: connection refused")
	tickChainChecks(context.Background(), rt)

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusRunning) {
		t.Errorf("status = %s (%q), want the chain still running", row.Status, row.Reason)
	}
	if step, run := chainAwaitedCheck(t, rt); step != "check" || run != 1 {
		t.Errorf("awaiting check = %q run %d, want it still awaiting check run 1", step, run)
	}
}

// TestPlacedCheckFallsBackToPulledRecordWithoutFeature pins the new client
// against a server too old to run a check: the check route is never posted, and
// the answer comes from the gate record the writer's round carries, exactly as
// it did for a chain started before the route existed.
func TestPlacedCheckFallsBackToPulledRecordWithoutFeature(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	// The server goes back to a build without the check feature, and the
	// awaiting check loses the run id it could not have recorded there.
	rt.Remote = chainRemoteFake()
	chainAwaitingWithoutRun(t, rt)
	chainPulledGate(t, rt, "pass")

	tickChainChecks(context.Background(), rt)

	if slices.Contains(rt.Remote.(*fakeRemote).calls, "CreateCheck:zen:shop:chk-1") {
		t.Error("the check was posted to a server without the check feature")
	}
	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusDone) {
		t.Errorf("status = %s (%q), want done from the pulled gate", got.Status, got.Reason)
	}
}

// TestFallbackCheckRecordsNoRunID pins that the fallback to a pulled gate record
// mints no run id. The fallback runs no check on the server, so it has nothing
// to poll: recording the builder's closed round as the run id left that number
// on the state, so the next check step inherited it and its tick asked the
// server for a check run that was never created. The miss that answered was the
// halt these two reports are about.
func TestFallbackCheckRecordsNoRunID(t *testing.T) {
	t.Parallel()

	rt, _, _ := chainRemoteRuntime(t, chainCheckFake())
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowTwoChecksWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	// The server loses the check feature, so every check from here is answered
	// from a pulled gate record and no CreateCheck is ever posted.
	rt.Remote = chainRemoteFake()
	chainAwaitingWithoutRun(t, rt)
	chainPulledGate(t, rt, "pass")
	fr := rt.Remote.(*fakeRemote)
	tickChainChecks(context.Background(), rt)

	// The round number is not a run id: the check it answered, and the check
	// step it left the chain on, both carry none.
	if _, run := chainAwaitedCheck(t, rt); run != 0 {
		t.Errorf("awaiting run = %d, want no run id: the fallback started no check to poll", run)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "GetCheck:") {
			t.Errorf("calls = %v, want no GetCheck for a run the fallback never started", fr.calls)
			break
		}
	}

	// A later tick on the chain the fallback left behind must still not poll:
	// nothing here ever posted a CreateCheck to answer.
	fr.calls = nil
	tickChainChecks(context.Background(), rt)
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "GetCheck:") {
			t.Errorf("calls = %v, want no GetCheck for a run the fallback never started", fr.calls)
			break
		}
	}
}

// TestPlacedStartAllowsTwoCommandsWithCheck pins that two different check
// commands are no longer a reason to refuse a placed writer: each check is its
// own run on the server's check route, so two commands are two runs.
func TestPlacedStartAllowsTwoCommandsWithCheck(t *testing.T) {
	t.Parallel()

	rt, _, _ := chainRemoteRuntime(t, chainCheckFake())
	stubRemoteSend(&rt)
	res := startedChain(t, rt, ChainOptions{
		Name: "shop", Feature: "auth", MasterMindID: testMasterMindName,
		Plans:    []string{writePlan(t, "build it")},
		Workflow: writeWorkflowFile(t, flowTwoChecksWorkflow),
	})
	if len(res.Members) == 0 {
		t.Fatal("the start produced no members")
	}
	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusRunning) {
		t.Errorf("chain status = %q reason %q, want the start to have begun", got.Status, got.Reason)
	}
}

// TestResumeGateOnPlacedWriterCallsSetGate pins that a --gate on a placed
// writer's chain reaches the server instead of being written only to the chain
// row: the binding's gate is pushed through the gate route. A binding that
// carries no gate of its own is left alone, because the check travels in the
// check request and the route would have nothing to update.
func TestResumeGateOnPlacedWriterCallsSetGate(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowGateParamWorkflow)
	if b := chainBinding(t, rt, "shop"); b.Gate != "make check" {
		t.Fatalf("placed writer gate = %q, want the chain's one check command", b.Gate)
	}
	advanceRemoteChain(t, rt, 2)
	haltRemoteChain(t, rt)

	fr.calls = nil
	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Gate: "make other"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if !slices.Contains(fr.calls, "SetGate:zen:shop") {
		t.Fatalf("calls = %v, want the gate route called on the server", fr.calls)
	}
	if fr.setGateReq.Gate == nil || *fr.setGateReq.Gate != "make other" {
		t.Errorf("gate request = %+v, want the resumed command", fr.setGateReq)
	}

	// The same resume on a writer whose binding carries no gate of its own
	// leaves the server alone.
	fr2 := chainCheckFake()
	rt2, _, _ := chainRemoteRuntime(t, fr2)
	stubRemoteSend(&rt2)
	startFlowChain(t, rt2, flowNoGateWorkflow)
	if b := chainBinding(t, rt2, "shop"); b.Gate != "" {
		t.Fatalf("writer with no check step carries gate %q, want none", b.Gate)
	}
	advanceRemoteChain(t, rt2, 2)
	haltRemoteChain(t, rt2)

	fr2.calls = nil
	if _, err := ChainResume(context.Background(), rt2, ResumeOptions{Name: "shop", Gate: "make other"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if slices.Contains(fr2.calls, "SetGate:zen:shop") {
		t.Errorf("calls = %v, want no gate route call for a binding with no gate", fr2.calls)
	}
}

// TestResumeGateOnPlacedWriterSkipsSetGateWhenRoundIsOpen pins that a resume
// refused for an open round pushes no gate: the server must not be told to run
// the next check under a command the chain's own settings never took, because
// the refusal leaves those settings exactly as they were.
func TestResumeGateOnPlacedWriterSkipsSetGateWhenRoundIsOpen(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowGateParamWorkflow)
	advanceRemoteChain(t, rt, 2)
	haltRemoteChain(t, rt)

	// The awaited round is still open: its prompt went out and no report came
	// back, so the resume has to refuse rather than overwrite the round in
	// flight.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("shop", store.LogEntry{
			TS: baseTime, Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt,
			Path: "/x/prompt-2.md",
		})
	}); err != nil {
		t.Fatalf("append the open round's prompt: %v", err)
	}

	fr.calls = nil
	_, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Gate: "make other"})
	var open *RoundOpenError
	if !errors.As(err, &open) {
		t.Fatalf("ChainResume = %v, want the open round's refusal", err)
	}
	if slices.Contains(fr.calls, "SetGate:zen:shop") {
		t.Errorf("calls = %v, want no gate route call for a refused resume", fr.calls)
	}
}

// TestResumeGateOnPlacedWriterSkipsSetGateOnBadParam pins the same for a
// refusal that happens before the transaction even opens: a --param the
// workflow has no slot for is refused, and the gate the resume carried must
// never reach the server either.
func TestResumeGateOnPlacedWriterSkipsSetGateOnBadParam(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowGateParamWorkflow)
	advanceRemoteChain(t, rt, 2)
	haltRemoteChain(t, rt)

	fr.calls = nil
	_, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name: "shop", Gate: "make other", Params: map[string]string{"nosuchparam": "x"},
	})
	if !errors.Is(err, refusal.ErrRefused) {
		t.Fatalf("ChainResume = %v, want refusal.ErrRefused for a param the workflow has no slot for", err)
	}
	if slices.Contains(fr.calls, "SetGate:zen:shop") {
		t.Errorf("calls = %v, want no gate route call for a refused resume", fr.calls)
	}
}

// TestPlacedCheckMissKeepsAwaiting pins that a bare miss is not a loss. A
// check run this machine minted can be one the server has not created yet --
// the run is stored when the create answer comes back, and a tick that reads
// between the two sees nothing under the id. That is a chain still waiting,
// not a chain that can never settle, so the tick says nothing, writes nothing
// and delivers nothing: a halt here would be a false end for a check that is
// about to answer.
func TestPlacedCheckMissKeepsAwaiting(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	fr.calls = nil
	fr.getCheckErr = &client.HTTPError{
		Status: 404,
		Body:   remote.ErrorBody{Message: "relevo: no such check run on this binding"},
	}
	tickChainChecks(context.Background(), rt)

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusRunning) || row.Reason != "" {
		t.Errorf("status = %s (%q), want the chain still running with no halt reason", row.Status, row.Reason)
	}
	if step, run := chainAwaitedCheck(t, rt); step != "check" || run != 1 {
		t.Errorf("awaiting check = %q run %d, want it still awaiting check run 1", step, run)
	}
	if entries := chainPendingChain(t, rt, "shop"); len(entries) != 0 {
		t.Errorf("queued %d chain deliveries, want none for a miss that is not a loss", len(entries))
	}
}

// TestPlacedCheckMissAsksAgain pins the other half of staying awaiting: the run
// is still polled on the next tick, so a run the server did create is still
// picked up. A miss that stopped the poll would strand the chain forever.
func TestPlacedCheckMissAsksAgain(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	fr.calls = nil
	fr.getCheckErr = &client.HTTPError{Status: 404, Body: remote.ErrorBody{Message: "relevo: no such check run on this binding"}}
	tickChainChecks(context.Background(), rt)
	tickChainChecks(context.Background(), rt)

	if n := slices.ContainsFunc(fr.calls, func(c string) bool {
		return strings.HasPrefix(c, "GetCheck:")
	}); !n {
		t.Errorf("calls = %v, want the tick to poll the run again after a miss", fr.calls)
	}
}

// TestPlacedCheckHaltsWhenTheServerHoldsAnotherRun pins the loss the tick can
// prove: the binding holds a check run under a different id, so the id this
// machine awaits names nothing that can ever settle. A bare miss proves
// nothing, but a server that holds a *different* run for this same step does
// prove the awaited one is gone, and the chain halts naming it.
func TestPlacedCheckHaltsWhenTheServerHoldsAnotherRun(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	// The binding holds chk-2, not the chk-1 this machine awaits, and chk-1 is
	// already settled there: the awaited run is gone for good.
	fr.getCheckIDs = map[string]remote.CheckView{
		"chk-2": {ID: "chk-2", Step: "check", Result: "pass"},
	}
	fr.getCheckErr = &client.HTTPError{Status: 404, Body: remote.ErrorBody{Message: "relevo: no such check run on this binding"}}
	tickChainChecks(context.Background(), rt)

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the chain halted on a run the server replaced", row.Status, row.Reason)
	}
	if !strings.Contains(row.Reason, "check") {
		t.Errorf("halt reason = %q, want it to name the check", row.Reason)
	}
}

// TestResumeFromEntersReviewOnTheDefaultWorkflow pins the rule --from is judged
// by, in the pure function that enters the step: on the shipped default,
// resuming at review runs the review step, not a second check run. A resume
// that re-entered check is the symptom this rules out, so the assertion is the
// entered step and not the run number.
func TestResumeFromEntersReviewOnTheDefaultWorkflow(t *testing.T) {
	t.Parallel()

	def := workflow.Default()
	state, _ := workflow.Start(def, workflow.StartInputs{Plans: []string{"/x/plan.md"}})
	// Drive the default to the check step and close it green, so the chain
	// would otherwise sit on check.
	state, _ = workflow.Next(def, state, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	state, _ = workflow.Next(def, state, workflow.Event{Kind: workflow.EventCheckClosed, Step: "check", Run: 1, Result: "pass"})
	state.Status = workflow.StatusHalted
	state.Reason = "halted for the human"

	resumed, acts, err := workflow.Resume(def, state, workflow.ResumeOpts{From: "review"})
	if err != nil {
		t.Fatalf("Resume(from review): %v", err)
	}
	if resumed.Awaiting.Step != "review" {
		t.Errorf("resumed awaiting step = %q, want the review the --from named", resumed.Awaiting.Step)
	}
	if resumed.Awaiting.Member == "builder" {
		t.Errorf("resumed awaiting member = %q, want the reviewer, not a second check run", resumed.Awaiting.Member)
	}
	if resumed.Awaiting.Run != 0 {
		t.Errorf("resumed awaiting run = %d, want no check run for a resume at review", resumed.Awaiting.Run)
	}
	var sent bool
	for _, act := range acts {
		if act.Kind == workflow.ActionSend && act.Actor != "" {
			sent = true
		}
		if act.Kind == workflow.ActionRunCheck {
			t.Errorf("resume at review started a check run, want the review step's send")
		}
	}
	if !sent {
		t.Error("resume at review produced no send, want the review step handed to the reviewer")
	}
}

// TestResumeFromAndCloseIsRefused pins the precedence the plan asks for: a
// resume that names both a target and a close is refused by the typed refusal,
// never silently letting one of them win. Entering check here would be the
// silent loss.
func TestResumeFromAndCloseIsRefused(t *testing.T) {
	t.Parallel()

	def := workflow.Default()
	state, _ := workflow.Start(def, workflow.StartInputs{Plans: []string{"/x/plan.md"}})
	state.Status = workflow.StatusHalted

	_, _, err := workflow.Resume(def, state, workflow.ResumeOpts{
		From:   "review",
		Closed: &workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 2},
	})
	if err == nil {
		t.Fatal("Resume(from+close) = nil error, want the typed refusal")
	}
	if !strings.Contains(err.Error(), "review") {
		t.Errorf("error = %q, want it to name the refused target", err)
	}
}

// TestShowGateRendersTheWholePulledLog pins the render half of the gate path:
// `show --round N --gate` prints the whole pulled log, past the stage a chain
// appears to stop at. The log arrives whole from the gate fetch, so the half
// that cut it was here -- a gate that ends at an early stage reads as a check
// that stopped there, and the last stage's verdict never reaches the reader.
func TestShowGateRendersTheWholePulledLog(t *testing.T) {
	t.Parallel()

	rt, _, _ := chainRemoteRuntime(t, chainCheckFake())
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)

	var stages []string
	for i := 1; i <= 12; i++ {
		stages = append(stages, fmt.Sprintf("stage %02d running", i))
	}
	stages = append(stages, "running check-comments.sh", "check-comments.sh: ok", "gate green")
	full := strings.Join(stages, "\n") + "\n"

	// The log arrives as the gate fetch installed it, at the gate log path.
	path := rt.Store.GateLogPath("shop", 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(full), 0o644); err != nil {
		t.Fatalf("write the gate log: %v", err)
	}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "shop", Round: 1, Section: ShowGate})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Missing {
		t.Fatal("Missing = true, want the pulled gate log")
	}
	if res.Text != full {
		t.Errorf("rendered gate =\n%q\nwant the whole log\n%q", res.Text, full)
	}
}

// TestPlacedCheckStoresPartialNoteForCutLog pins that a served tail cut at the
// cap is stored under a line saying it is partial and where the full log is,
// and that a log within the cap is stored bare.
func TestPlacedCheckStoresPartialNoteForCutLog(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		view remote.CheckView
		want string
	}{
		{"cut", remote.CheckView{ID: "chk-1", Result: "pass", LogTail: "tail\n", LogTruncated: true},
			"partial: the last 64 KiB of the check log; the full log is check run chk-1 on server zen\ntail\n"},
		{"whole", remote.CheckView{ID: "chk-1", Result: "pass", LogTail: "tail\n"}, "tail\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fr := chainCheckFake()
			rt, _, _ := chainRemoteRuntime(t, fr)
			stubRemoteSend(&rt)
			startFlowChain(t, rt, flowPlacedCheckWorkflow)
			flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
			fr.getCheckResp = tc.view
			tickChainChecks(context.Background(), rt)

			got, err := rt.Store.ReadFile(rt.Store.CheckLogPath("shop", 1, 1))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("stored check log = %q, want %q", got, tc.want)
			}
		})
	}
}
