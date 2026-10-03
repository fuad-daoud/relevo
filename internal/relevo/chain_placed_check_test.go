package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
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
		st, err := chainWorkflowState(c)
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
	st, err := chainWorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
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
				st, _ := chainWorkflowState(flowChainRow(t, rt))
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

// TestPlacedStartAllowsTwoCommandsWithCheck pins that two different check
// commands are no longer a reason to refuse a placed writer: each check is its
// own run on the server's check route, so two commands are two runs.
func TestPlacedStartAllowsTwoCommandsWithCheck(t *testing.T) {
	t.Parallel()

	rt, _, _ := chainRemoteRuntime(t, chainCheckFake())
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
	startFlowChain(t, rt, flowGateParamWorkflow)
	advanceRemoteChain(t, rt, 2)
	haltRemoteChain(t, rt)

	fr.calls = nil
	_, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name: "shop", Gate: "make other", Params: map[string]string{"nosuchparam": "x"},
	})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("ChainResume = %v, want ErrRefused for a param the workflow has no slot for", err)
	}
	if slices.Contains(fr.calls, "SetGate:zen:shop") {
		t.Errorf("calls = %v, want no gate route call for a refused resume", fr.calls)
	}
}

// TestPlacedCheckHaltsWhenTheServerHasNoSuchRun pins a lost run: a check the
// server no longer holds is a chain that can never settle, so the chain halts
// and says which check it was.
func TestPlacedCheckHaltsWhenTheServerHasNoSuchRun(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	fr.getCheckErr = &client.HTTPError{Status: 404, Body: remote.ErrorBody{Message: "no check chk-1"}}
	tickChainChecks(context.Background(), rt)

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusHalted) {
		t.Errorf("status = %s, want the chain halted on a check the server dropped", row.Status)
	}
	if !strings.Contains(row.Reason, "check") {
		t.Errorf("halt reason = %q, want it to name the check", row.Reason)
	}
}
