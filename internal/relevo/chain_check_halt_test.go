package relevo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

func TestHaltAfterAFailedCheckNamesTheCheckAndQuotesItsLog(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startNoRegateFlowChain(t, rt)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "compile ok\nFAIL TestWidget\n")

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the zero-budget halt", c.Status, c.Reason)
	}
	for _, want := range []string{
		"repair has no budget left",
		"The last check failed (exit code 1);",
		"relevo show shop --round 1 --gate",
		"FAIL TestWidget",
		"compile ok",
	} {
		if !strings.Contains(c.Reason, want) {
			t.Errorf("halt reason %q does not carry %q", c.Reason, want)
		}
	}
}

func TestHaltReasonQuotesOnlyTheLastTwentyLogLines(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startNoRegateFlowChain(t, rt)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	var log strings.Builder
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&log, "line-%02d\n", i)
	}
	chainRedCheck(t, rt, "shop", log.String())

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the zero-budget halt", c.Status, c.Reason)
	}
	if strings.Contains(c.Reason, "line-05\n") || strings.HasSuffix(c.Reason, "line-05") {
		t.Errorf("halt reason quotes line-05, past the last 20 lines:\n%s", c.Reason)
	}
	for _, want := range []string{"line-06", "line-25"} {
		if !strings.Contains(c.Reason, want) {
			t.Errorf("halt reason does not carry %q:\n%s", want, c.Reason)
		}
	}
}

func TestHaltWithNoFailedCheckKeepsItsWording(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	fg.dirtyResult = true
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want halted", c.Status, c.Reason)
	}
	st, err := chainWorkflowState(c)
	if err != nil {
		t.Fatalf("chainWorkflowState: %v", err)
	}
	if st.Reason != c.Reason {
		t.Errorf("state reason %q differs from row reason %q", st.Reason, c.Reason)
	}
	if strings.Contains(c.Reason, "relevo show") || strings.Contains(c.Reason, "last check") {
		t.Errorf("halt reason %q carries a check detail with no failed check behind it", c.Reason)
	}
}

func TestOnlyAHaltedOrStoppedEndCarriesTheCheckDetail(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startNoRegateFlowChain(t, rt)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "FAIL TestWidget\n")

	c := flowChainRow(t, rt)
	if !strings.Contains(c.Reason, "FAIL TestWidget") {
		t.Fatalf("halt reason %q lacks the detail the comparison below needs", c.Reason)
	}
	for _, status := range []workflow.Status{workflow.StatusRunning, workflow.StatusDone} {
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			if got := chainHaltReason(rt, tx, c, workflow.State{Status: status, Reason: "r"}); got != "r" {
				t.Errorf("reason for a %s end = %q, want it unchanged", status, got)
			}
			return nil
		}); err != nil {
			t.Fatalf("WithLock: %v", err)
		}
	}
}

func TestHaltAfterATimedOutCheckSaysItTimedOutAndNamesTheSetting(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startNoRegateFlowChain(t, rt)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	_, row := flowCheckRun(t, rt, "shop")
	clock := &fakeClock{now: time.Unix(row.StartedAt, 0)}
	rt = withClock(rt, clock)
	clock.Advance(2 * rt.Policy.GateTimeout())
	tickChainChecks(context.Background(), rt)

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the zero-budget halt", c.Status, c.Reason)
	}
	for _, want := range []string{"timed out after", "policy.gate.timeout_ms", "relevo show shop --round 1 --gate"} {
		if !strings.Contains(c.Reason, want) {
			t.Errorf("halt reason %q does not carry %q", c.Reason, want)
		}
	}
	if strings.Contains(c.Reason, "last check fail") {
		t.Errorf("halt reason %q words a timeout as a failure", c.Reason)
	}
}

func TestHaltAfterAnErroredCheckNamesItsNote(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startNoRegateFlowChain(t, rt)
	fr := rt.Runner.(*fakeRunner)
	fr.startErr = errors.New("no such shell")
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the zero-budget halt", c.Status, c.Reason)
	}
	if !strings.Contains(c.Reason, "could not run: no such shell.") {
		t.Errorf("halt reason %q does not name the check's note", c.Reason)
	}
	if strings.Contains(c.Reason, "timed out") {
		t.Errorf("halt reason %q words an error as a timeout", c.Reason)
	}
}

// A chain whose check passed and which then halts on a later step keeps that
// step's own reason: the green check is not why it stopped.
func TestHaltAfterAPassingCheckKeepsItsWording(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowCheckLogWorkflow)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainGreenCheck(t, rt, "shop")
	chainReaderClose(t, rt, "shop-assistant", chainVerdictBody("changes"))

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want the reviewer's halt", c.Status, c.Reason)
	}
	if strings.Contains(c.Reason, "relevo show") || strings.Contains(c.Reason, "last check") {
		t.Errorf("halt reason %q carries a check detail after a passing check", c.Reason)
	}
}
