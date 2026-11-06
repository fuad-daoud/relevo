package relevo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// TestChainCheckTimeoutRetriesTheCheckFresh pins the hold: a timed-out run
// settles as timeout and starts a fresh run, instead of reddening.
func TestChainCheckTimeoutRetriesTheCheckFresh(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	row := loadChainCheck(t, rt, run)

	clock := &fakeClock{now: time.Unix(row.StartedAt, 0)}
	rt = withClock(rt, clock)
	clock.Advance(2 * rt.Policy.GateTimeout())

	result, _ := chainAdvanceCheckForTest(t, rt, c, run)
	if result != "" {
		t.Errorf("result = %q, want empty while the fresh run is in flight", result)
	}
	first := loadChainCheck(t, rt, run)
	if first.Result != "timeout" {
		t.Errorf("run %d Result = %q, want timeout: the timed-out run must never read red", run, first.Result)
	}
	sealed, serr := rt.Store.ReadFile(first.Log)
	if serr != nil {
		t.Fatalf("ReadFile(%s): %v", first.Log, serr)
	}
	if len(sealed) != 0 {
		t.Errorf("sealed log = %q, want it sealed even when empty", sealed)
	}
	retry := loadChainCheck(t, rt, run+1)
	if retry.Result != "" {
		t.Errorf("run %d Result = %q, want it in flight", run+1, retry.Result)
	}
	if retry.Step != "check" || retry.Command != "make check" {
		t.Errorf("run %d = %+v, want the same check step retried fresh", run+1, retry)
	}
	if len(fr.handles) != 2 {
		t.Fatalf("starts = %d, want the timed-out run plus its fresh retry", len(fr.handles))
	}
	if retry.PID != fr.handles[1].PID {
		t.Errorf("retry PID = %d, want the fresh spawn %d", retry.PID, fr.handles[1].PID)
	}
}

// TestChainCheckSettledTimeoutNeverReds pins the settled-row guard: a row
// already settled as timeout never closes red, so a repeated advance stays
// quiet instead of feeding the engine.
func TestChainCheckSettledTimeoutNeverReds(t *testing.T) {
	t.Parallel()

	rt, _, c := chainCheckFixture(t)
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	row := loadChainCheck(t, rt, run)

	clock := &fakeClock{now: time.Unix(row.StartedAt, 0)}
	rt = withClock(rt, clock)
	clock.Advance(2 * rt.Policy.GateTimeout())

	if result, _ := chainAdvanceCheckForTest(t, rt, c, run); result != "" {
		t.Fatalf("first advance = %q, want the hold", result)
	}
	if result, _ := chainAdvanceCheckForTest(t, rt, c, run); result != "" {
		t.Errorf("settled timeout advance = %q, want empty: a settled timeout never reddens", result)
	}
	if got := loadChainCheck(t, rt, run); got.Result != "timeout" {
		t.Errorf("row.Result = %q, want timeout", got.Result)
	}
}

// TestChainCheckFailStillReds pins the control: a failing check still closes
// red, so the timeout guard changes no other result.
func TestChainCheckFailStillReds(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	pid := loadChainCheck(t, rt, run).PID
	fr.script(pid, false)
	fr.exit(pid, 2)

	result, _ := chainAdvanceCheckForTest(t, rt, c, run)
	if result != chainCheckRed {
		t.Errorf("result = %q, want red for a fail", result)
	}
	if got := loadChainCheck(t, rt, run); got.Result != "fail" {
		t.Errorf("row.Result = %q, want fail", got.Result)
	}
}

// TestChainCheckGreenAfterTimeoutRoutesGreen pins the recovery: a green
// re-run after a timeout closes green, with no previous red for a later
// retry to repeat.
func TestChainCheckGreenAfterTimeoutRoutesGreen(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	run, row := flowCheckRun(t, rt, "shop")
	clock := &fakeClock{now: time.Unix(row.StartedAt, 0)}
	rt = withClock(rt, clock)
	clock.Advance(2 * rt.Policy.GateTimeout())

	tickChainChecks(context.Background(), rt)
	next, nrow := flowCheckRun(t, rt, "shop")
	if next != run+1 {
		t.Fatalf("run = %d, want %d: the first timeout retries fresh", next, run+1)
	}
	fr := rt.Runner.(*fakeRunner)
	fr.script(nrow.PID, false)
	fr.exit(nrow.PID, 0)
	c := flowChainRow(t, rt)
	var result string
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		result, _, err = chainAdvanceCheck(context.Background(), rt, tx, c, next)
		return err
	}); err != nil {
		t.Fatalf("chainAdvanceCheck: %v", err)
	}
	if result != chainCheckGreen {
		t.Errorf("result = %q, want green for the re-run", result)
	}
	st, serr := chain.WorkflowState(flowChainRow(t, rt))
	if serr != nil {
		t.Fatalf("WorkflowState: %v", serr)
	}
	if chainRepeatRedCheck(rt, c, st, "check", nrow.Log) {
		t.Errorf("a timeout counts as a previous red")
	}
}

// TestChainCheckGreenRetryFinishesTheChainViaTick pins the tick continuation:
// a timeout's retry run advanced through the tick finishes the chain when it
// goes green, so the hold does not wedge the chain with all rows settled.
func TestChainCheckGreenRetryFinishesTheChainViaTick(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	run, row := flowCheckRun(t, rt, "shop")
	clock := &fakeClock{now: time.Unix(row.StartedAt, 0)}
	rt = withClock(rt, clock)
	clock.Advance(2 * rt.Policy.GateTimeout())

	tickChainChecks(context.Background(), rt)
	next, nrow := flowCheckRun(t, rt, "shop")
	if next != run+1 {
		t.Fatalf("run = %d, want %d: the first timeout retries fresh", next, run+1)
	}

	fr := rt.Runner.(*fakeRunner)
	fr.script(nrow.PID, false)
	fr.exit(nrow.PID, 0)
	tickChainChecks(context.Background(), rt)

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusDone) {
		t.Fatalf("status = %s (%q), want done after the retry goes green", c.Status, c.Reason)
	}
	st, serr := chain.WorkflowState(c)
	if serr != nil {
		t.Fatalf("WorkflowState: %v", serr)
	}
	if got := st.Results["check"].Status; got != chainCheckGreen {
		t.Errorf("check result = %q, want green from the retry", got)
	}
	if chainRepeatRedCheck(rt, c, st, "check", nrow.Log) {
		t.Errorf("a timeout counts as a previous red")
	}
}

// seedTimeoutCheckHolds records settled timeout rows for the step, the shape
// a step timed out that many times already has.
func seedTimeoutCheckHolds(t *testing.T, rt Runtime, name string, runs ...db.ChainCheckRow) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, row := range runs {
			if err := tx.ChainCheckPut(name, row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed holds: %v", err)
	}
}

// TestTimeoutCheckHoldsSkipsCouldNotStart pins the walk: a could-not-start
// between two timeouts neither counts nor ends the walk, so the second
// timeout still halts.
func TestTimeoutCheckHoldsSkipsCouldNotStart(t *testing.T) {
	t.Parallel()

	rt, _, _ := chainCheckFixture(t)
	seedTimeoutCheckHolds(t, rt, "shop",
		db.ChainCheckRow{Run: 1, Step: "check", Visit: 1, Command: "true", Result: "timeout"},
		db.ChainCheckRow{Run: 2, Step: "check", Visit: 2, Command: "true", Result: chainCheckCouldNotStart},
		db.ChainCheckRow{Run: 3, Step: "check", Visit: 3, Command: "true", Result: "timeout"},
	)
	var holds int
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		holds, err = timeoutCheckHolds(tx, "shop", "check")
		return err
	}); err != nil {
		t.Fatalf("timeoutCheckHolds: %v", err)
	}
	if holds != 2 {
		t.Errorf("holds = %d, want 2: the freeze between two timeouts changes nothing", holds)
	}
}

// TestTimeoutCheckHoldsEndsOnOtherResult pins the walk's end: a pass between
// two timeouts ends it, so the later timeout is not in a row with the first.
func TestTimeoutCheckHoldsEndsOnOtherResult(t *testing.T) {
	t.Parallel()

	rt, _, _ := chainCheckFixture(t)
	seedTimeoutCheckHolds(t, rt, "shop",
		db.ChainCheckRow{Run: 1, Step: "check", Visit: 1, Command: "true", Result: "timeout"},
		db.ChainCheckRow{Run: 2, Step: "check", Visit: 2, Command: "true", Result: "pass"},
		db.ChainCheckRow{Run: 3, Step: "check", Visit: 3, Command: "true", Result: "timeout"},
	)
	var holds int
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		holds, err = timeoutCheckHolds(tx, "shop", "check")
		return err
	}); err != nil {
		t.Fatalf("timeoutCheckHolds: %v", err)
	}
	if holds != 1 {
		t.Errorf("holds = %d, want 1: a pass ends the walk", holds)
	}
}

// TestChainCheckSecondTimeoutHaltsTheChain pins the terminal hold: the tick
// that advances a second consecutive timeout halts the chain naming the
// limit, with no red close, no repair and no previous red.
func TestChainCheckSecondTimeoutHaltsTheChain(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	run, row := flowCheckRun(t, rt, "shop")
	clock := &fakeClock{now: time.Unix(row.StartedAt, 0)}
	rt = withClock(rt, clock)
	clock.Advance(2 * rt.Policy.GateTimeout())

	tickChainChecks(context.Background(), rt)
	if c := flowChainRow(t, rt); c.Status != string(workflow.StatusRunning) {
		t.Fatalf("status = %s (%q), want running after the first timeout's retry", c.Status, c.Reason)
	}
	next, nrow := flowCheckRun(t, rt, "shop")
	if next != run+1 {
		t.Fatalf("run = %d, want %d: the first timeout retries fresh", next, run+1)
	}
	if nrow.Step != "check" {
		t.Errorf("step = %q, want the same check step", nrow.Step)
	}
	if st, serr := chain.WorkflowState(flowChainRow(t, rt)); serr != nil {
		t.Fatalf("WorkflowState: %v", serr)
	} else if st.Awaiting.Run != next {
		t.Errorf("Awaiting.Run = %d, want the retry run %d", st.Awaiting.Run, next)
	}

	tickChainChecks(context.Background(), rt)

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the timeout halt", c.Status, c.Reason)
	}
	for _, want := range []string{"timed out after", "policy.gate.timeout_ms", "2 times in a row"} {
		if !strings.Contains(c.Reason, want) {
			t.Errorf("halt reason %q does not carry %q", c.Reason, want)
		}
	}
	st, serr := chain.WorkflowState(c)
	if serr != nil {
		t.Fatalf("WorkflowState: %v", serr)
	}
	if _, ok := st.Results["check"]; ok {
		t.Errorf("engine results = %+v, want no red for a hold", st.Results)
	}
	if chainRepeatRedCheck(rt, c, st, "check", nrow.Log) {
		t.Errorf("a timeout run counts as a previous red")
	}
}

// TestChainCheckTimeoutHaltResumesWithoutExtraRetry pins the resume half: a
// resumed chain whose newest rows are timeouts halts on its next timeout
// with no extra retry.
func TestChainCheckTimeoutHaltResumesWithoutExtraRetry(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowCheckWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})
	_, row := flowCheckRun(t, rt, "shop")
	clock := &fakeClock{now: time.Unix(row.StartedAt, 0)}
	rt = withClock(rt, clock)
	clock.Advance(2 * rt.Policy.GateTimeout())

	tickChainChecks(context.Background(), rt)
	tickChainChecks(context.Background(), rt)
	if c := flowChainRow(t, rt); c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the timeout halt before the resume", c.Status, c.Reason)
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusRunning) {
		t.Fatalf("status = %s (%q), want the resumed chain running", c.Status, c.Reason)
	}
	next, _ := flowCheckRun(t, rt, "shop")
	if next != 3 {
		t.Fatalf("resumed run = %d, want 3: a resume runs the check fresh", next)
	}

	tickChainChecks(context.Background(), rt)
	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the timeout halt with no extra retry", got.Status, got.Reason)
	}
	for _, want := range []string{"timed out after", "policy.gate.timeout_ms", "3 times in a row"} {
		if got := flowChainRow(t, rt); !strings.Contains(got.Reason, want) {
			t.Errorf("halt reason %q does not carry %q", got.Reason, want)
		}
	}
	if _, err := rt.Store.ChainCheck("shop", 4); err == nil {
		t.Errorf("run 4 exists after a resumed timeout: want no extra retry")
	}
}
