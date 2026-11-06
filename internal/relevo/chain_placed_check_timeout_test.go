package relevo

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// plantPlacedCheckClosed appends one check_closed trace row for the chain's
// check step, the shape the placed-check hold records: planted rows stand for
// the closes of earlier visits so a test can place a timeout, a pass or a
// could-not-start exactly where it needs the walk to read them.
func plantPlacedCheckClosed(t *testing.T, rt Runtime, run int, result string) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.ChainEventAppend("shop", db.ChainEventRow{
			TS: baseTime, Step: "check", Plan: 1,
			Event:  workflow.EncodeEvent(workflow.Event{Kind: workflow.EventCheckClosed, Step: "check", Run: run, Result: result}),
			Action: workflow.EncodeAction(workflow.Action{Kind: workflow.ActionHalt, Step: "check"}),
		})
	}); err != nil {
		t.Fatalf("plant the check_closed row for run %d (%s): %v", run, result, err)
	}
}

// placedCheckTimeoutView is a server's answer for a run that timed out: the
// word an older server already sends over the wire.
func placedCheckTimeoutView(id string) remote.CheckView {
	return remote.CheckView{ID: id, Result: "timeout", DurationMS: 90_000, LogTail: "the gate went quiet\n"}
}

// awaitPlacedCheckRun points the chain's awaited check at a fresh run id, the
// shape a later visit to the step carries.
func awaitPlacedCheckRun(t *testing.T, rt Runtime, run int) {
	t.Helper()
	chainSetWorkflowState(t, rt, func(st *workflow.State) { st.Awaiting.Run = run })
}

// TestPlacedCheckTimeoutRestartsOnceWithoutRed pins the route path's hold: a
// polled timeout seals the served tail and records its close on the trace,
// but no red reaches the engine: the chain keeps awaiting, starts the check
// once more under the next run id, and holds no red result to read as a
// previous failure.
func TestPlacedCheckTimeoutRestartsOnceWithoutRed(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	fr.calls = nil
	fr.getCheckResp = placedCheckTimeoutView("chk-1")
	tickChainChecks(context.Background(), rt)

	if row := flowChainRow(t, rt); row.Status != string(workflow.StatusRunning) {
		t.Fatalf("status = %s (%q), want the chain still running after one timeout", row.Status, row.Reason)
	}
	if !slices.Contains(fr.calls, "CreateCheck:zen:shop:chk-2") {
		t.Fatalf("calls = %v, want the hold to post the check once more under the next id", fr.calls)
	}
	if req := fr.createCheckReq; req.ID != "chk-2" || req.Command != "make check" || req.Step != "check" {
		t.Errorf("retry request = %+v, want chk-2 running make check on step check", req)
	}
	if step, run := chainAwaitedCheck(t, rt); step != "check" || run != 2 {
		t.Errorf("awaiting check = %q run %d, want the chain awaiting the retry run", step, run)
	}

	st, err := chain.WorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if _, ok := st.Results["check"]; ok {
		t.Errorf("engine results = %+v, want no red for a timed-out close", st.Results)
	}
	if b := chainBinding(t, rt, "shop"); b.RepairCount != 0 {
		t.Errorf("repair count = %d, want a timeout to spend no repair budget", b.RepairCount)
	}
	if _, cerr := rt.Store.ChainCheck("shop", 1); !errors.Is(cerr, store.ErrNotFound) {
		t.Errorf("a local check run exists on a placed writer: %v", cerr)
	}

	row := chainLastCheckClosed(t, rt, "check")
	ev, err := workflow.DecodeEvent(row.Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Result != "timeout" || ev.Run != 1 {
		t.Errorf("trace close = %q run %d, want the timeout's own word for run 1", ev.Result, ev.Run)
	}
	body, err := rt.Store.ReadFile(rt.Store.CheckLogPath("shop", 1, 1))
	if err != nil {
		t.Fatalf("the timed-out run's served tail is not sealed: %v", err)
	}
	if !strings.Contains(string(body), "the gate went quiet") {
		t.Errorf("sealed tail = %q, want the served tail stored before the hold", body)
	}

	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	line := RenderTrace(doc)
	if !strings.Contains(line, "result=timeout") {
		t.Errorf("trace render =\n%s\nwant the timeout close readable as a timeout", line)
	}
}

// TestPlacedCheckSecondConsecutiveTimeoutHaltsWithTheLimitWording pins the
// bound: the second timeout in a row on the step halts the chain naming the
// gate timeout setting, without a retry, a red result or a repair round.
func TestPlacedCheckSecondConsecutiveTimeoutHaltsWithTheLimitWording(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	fr.getCheckResp = placedCheckTimeoutView("chk-1")
	tickChainChecks(context.Background(), rt)

	fr.calls = nil
	fr.getCheckResp = placedCheckTimeoutView("chk-2")
	tickChainChecks(context.Background(), rt)

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the halt on the second consecutive timeout", row.Status, row.Reason)
	}
	for _, want := range []string{"timed out after", "policy.gate.timeout_ms"} {
		if !strings.Contains(row.Reason, want) {
			t.Errorf("halt reason %q does not carry %q", row.Reason, want)
		}
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "CreateCheck:") {
			t.Fatalf("calls = %v, want no third run posted: the halt starts nothing", fr.calls)
		}
	}

	st, err := chain.WorkflowState(row)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if _, ok := st.Results["check"]; ok {
		t.Errorf("engine results = %+v, want no red for a timed-out close", st.Results)
	}

	doc, err := ChainTrace(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainTrace: %v", err)
	}
	line := RenderTrace(doc)
	if !strings.Contains(line, "result=timeout → halt") {
		t.Errorf("trace render =\n%s\nwant the halting close readable as a timeout that halted", line)
	}
}

// TestPlacedCheckTimeoutAfterAGreenIsNotInARow pins the walk's end rule: a
// pass between two timeouts ends the walk, so a timeout, a green and a later
// timeout is not consecutive and holds once more instead of halting.
func TestPlacedCheckTimeoutAfterAGreenIsNotInARow(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	plantPlacedCheckClosed(t, rt, 1, "timeout")
	plantPlacedCheckClosed(t, rt, 2, "green")
	awaitPlacedCheckRun(t, rt, 3)

	fr.calls = nil
	fr.getCheckResp = placedCheckTimeoutView("chk-3")
	tickChainChecks(context.Background(), rt)

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusRunning) {
		t.Fatalf("status = %s (%q), want the hold after a green is not the second in a row", row.Status, row.Reason)
	}
	if !slices.Contains(fr.calls, "CreateCheck:zen:shop:chk-4") {
		t.Fatalf("calls = %v, want the retry minted past the recorded closes", fr.calls)
	}
}

// TestPlacedCheckFreezeBetweenTimeoutsStillHalts pins the walk's skip rule:
// a could-not-start close neither counts nor ends the walk, because a freeze
// between two timeouts changes nothing about them.
func TestPlacedCheckFreezeBetweenTimeoutsStillHalts(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	plantPlacedCheckClosed(t, rt, 1, "timeout")
	plantPlacedCheckClosed(t, rt, 2, chainCheckCouldNotStart)
	awaitPlacedCheckRun(t, rt, 3)

	fr.getCheckResp = placedCheckTimeoutView("chk-3")
	tickChainChecks(context.Background(), rt)

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want two timeouts across a freeze to be in a row", row.Status, row.Reason)
	}
	if !strings.Contains(row.Reason, "policy.gate.timeout_ms") {
		t.Errorf("halt reason %q, want it to name the timeout setting", row.Reason)
	}
}

// TestPlacedCheckCouldNotStartAloneDoesNotCountAsATimeout pins the other
// half of the skip rule: a could-not-start close does not count, so a first
// timeout over one holds and retries instead of halting.
func TestPlacedCheckCouldNotStartAloneDoesNotCountAsATimeout(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	plantPlacedCheckClosed(t, rt, 1, chainCheckCouldNotStart)
	awaitPlacedCheckRun(t, rt, 2)

	fr.calls = nil
	fr.getCheckResp = placedCheckTimeoutView("chk-2")
	tickChainChecks(context.Background(), rt)

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusRunning) {
		t.Fatalf("status = %s (%q), want the first timeout beside a could-not-start to hold, not halt", row.Status, row.Reason)
	}
	if !slices.Contains(fr.calls, "CreateCheck:zen:shop:chk-3") {
		t.Fatalf("calls = %v, want the retry posted under the next id", fr.calls)
	}
}

// TestPlacedCheckGreenAfterTimeoutRoutesGreen pins the control: a hold is
// not a dead end -- the retried run's green close reaches the engine and the
// chain moves on along its green edge, the budget unspent.
func TestPlacedCheckGreenAfterTimeoutRoutesGreen(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	fr.getCheckResp = placedCheckTimeoutView("chk-1")
	tickChainChecks(context.Background(), rt)

	fr.getCheckResp = remote.CheckView{ID: "chk-2", Result: "pass", LogTail: "all green\n"}
	tickChainChecks(context.Background(), rt)

	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusDone) {
		t.Errorf("status = %s (%q), want done from the retried run's green", got.Status, got.Reason)
	}
	st, err := chain.WorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if res, ok := st.Results["check"]; !ok || res.Status != chainCheckGreen {
		t.Errorf("engine results = %+v, want the green close recorded for the step", st.Results)
	}
	if b := chainBinding(t, rt, "shop"); b.RepairCount != 0 {
		t.Errorf("repair count = %d, want the timeout to have spent no repair budget", b.RepairCount)
	}
}

// TestPlacedCheckFailStillClosesRed pins the route control: a fail is not a
// timeout -- it still closes red through the engine and the workflow's red
// edge still takes.
func TestPlacedCheckFailStillClosesRed(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	fr.getCheckResp = remote.CheckView{ID: "chk-1", Result: "fail", ExitCode: 2, LogTail: "the same failure\n"}
	tickChainChecks(context.Background(), rt)

	row := chainLastCheckClosed(t, rt, "check")
	ev, err := workflow.DecodeEvent(row.Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Result != chainCheckRed {
		t.Errorf("closed result = %q, want a fail to still close red", ev.Result)
	}
	st, err := chain.WorkflowState(flowChainRow(t, rt))
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if res, ok := st.Results["check"]; !ok || res.Status != chainCheckRed {
		t.Errorf("engine results = %+v, want the red entry a fail still writes", st.Results)
	}
	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusHalted) {
		t.Errorf("status = %s (%q), want the workflow's red edge taken", got.Status, got.Reason)
	}
}

// TestPlacedCheckTimeoutRetryRefusalHaltsAsAStartFailure pins the hold's
// error branch: a server that refuses the retry's fresh check halts the
// chain with the start-failure wording, not with a timeout sentence and not
// with a red close.
func TestPlacedCheckTimeoutRetryRefusalHaltsAsAStartFailure(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	fr.getCheckResp = placedCheckTimeoutView("chk-1")
	fr.createCheckErr = errors.New("the gate route is closed")
	tickChainChecks(context.Background(), rt)

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the refused retry to halt the chain", row.Status, row.Reason)
	}
	if !strings.Contains(row.Reason, "could not start") || !strings.Contains(row.Reason, "the gate route is closed") {
		t.Errorf("halt reason %q, want the start failure the refusal carries", row.Reason)
	}
	st, err := chain.WorkflowState(row)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if _, ok := st.Results["check"]; ok {
		t.Errorf("engine results = %+v, want no red for a timeout or its failed retry", st.Results)
	}
}

// TestPlacedCheckGateRecordTimeoutHaltsWithTheWording pins the pulled-gate
// close: a writer's round whose gate timed out halts the chain with the
// timeout wording -- no red close, no repair round staged and no retry,
// because the record is history and no check process stands behind it.
func TestPlacedCheckGateRecordTimeoutHaltsWithTheWording(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	// The pull shape: the chain answers from the writer's newest closed
	// round's gate record, with no run id to poll.
	chainAwaitingWithoutRun(t, rt)
	chainPulledGate(t, rt, "timeout")

	fr.calls = nil
	tickChainChecks(context.Background(), rt)

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the timeout's halt on the pulled record", row.Status, row.Reason)
	}
	for _, want := range []string{"timed out after", "policy.gate.timeout_ms"} {
		if !strings.Contains(row.Reason, want) {
			t.Errorf("halt reason %q does not carry %q", row.Reason, want)
		}
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "CreateCheck:") {
			t.Fatalf("calls = %v, want no check posted from a pulled record", fr.calls)
		}
	}

	st, err := chain.WorkflowState(row)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	if _, ok := st.Results["check"]; ok {
		t.Errorf("engine results = %+v, want no red for a timed-out gate record", st.Results)
	}
	if b := chainBinding(t, rt, "shop"); b.RepairCount != 0 {
		t.Errorf("repair count = %d, want no repair round staged for a timeout", b.RepairCount)
	}
	closed := chainLastCheckClosed(t, rt, "check")
	ev, derr := workflow.DecodeEvent(closed.Event)
	if derr != nil {
		t.Fatalf("DecodeEvent: %v", derr)
	}
	if ev.Result != "timeout" {
		t.Errorf("trace close = %q, want the timeout's own word", ev.Result)
	}
}

// TestPlacedCheckGateRecordFailStillClosesRed pins the pull control: a failed
// gate record is not a timeout -- it still closes red and still feeds the
// repeat-red rule.
func TestPlacedCheckGateRecordFailStillClosesRed(t *testing.T) {
	t.Parallel()

	rt, _, _ := chainRemoteRuntime(t, chainCheckFake())
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	chainAwaitingWithoutRun(t, rt)
	chainPulledGate(t, rt, "fail")
	tickChainChecks(context.Background(), rt)

	row := chainLastCheckClosed(t, rt, "check")
	ev, err := workflow.DecodeEvent(row.Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Result != chainCheckRed {
		t.Errorf("closed result = %q, want a failed gate record to still close red", ev.Result)
	}
	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusHalted) {
		t.Errorf("status = %s (%q), want the workflow's red edge taken", got.Status, got.Reason)
	}
}

// TestPlacedCheckTimeoutHaltResumesWithoutExtraRetry pins the resume half of
// the route path: a resumed chain whose newest trace closes are timeouts
// halts on its next timeout with no extra retry -- the bound reads the
// trace, which outlives the halt.
func TestPlacedCheckTimeoutHaltResumesWithoutExtraRetry(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	stubRemoteSend(&rt)
	startFlowChain(t, rt, flowPlacedCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	fr.getCheckResp = placedCheckTimeoutView("chk-1")
	tickChainChecks(context.Background(), rt)
	fr.getCheckResp = placedCheckTimeoutView("chk-2")
	tickChainChecks(context.Background(), rt)
	if row := flowChainRow(t, rt); row.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the timeout halt before the resume", row.Status, row.Reason)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if row := flowChainRow(t, rt); row.Status != string(workflow.StatusRunning) {
		t.Fatalf("status = %s (%q), want the resumed chain running", row.Status, row.Reason)
	}
	// The resumed run mints past the timeout trace rows: the recorded closes
	// keep ids advancing.
	if step, run := chainAwaitedCheck(t, rt); step != "check" || run != 3 {
		t.Fatalf("awaiting check = %q run %d, want the resumed chain awaiting chk-3", step, run)
	}

	fr.calls = nil
	fr.getCheckResp = placedCheckTimeoutView("chk-3")
	tickChainChecks(context.Background(), rt)

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the halt on the resumed chain's next timeout", row.Status, row.Reason)
	}
	if !strings.Contains(row.Reason, "policy.gate.timeout_ms") {
		t.Errorf("halt reason %q, want it to name the timeout setting", row.Reason)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "CreateCheck:") {
			t.Fatalf("calls = %v, want no fourth run posted after the resume", fr.calls)
		}
	}
}
