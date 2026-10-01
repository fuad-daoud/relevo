package relevo

import (
	"context"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// flowPendingChain collects a workflow chain's undelivered end payloads across
// its members, whichever member carried the one delivery.
func flowPendingChain(t *testing.T, rt Runtime) []store.LogEntry {
	t.Helper()
	var out []store.LogEntry
	for _, name := range []string{"shop", "shop-assistant"} {
		out = append(out, chainPendingChain(t, rt, name)...)
	}
	return out
}

// TestWorkflowSweepHaltsOnGoneMember pins the workflow sweep: a chain whose
// awaited member's record is gone halts with that member named, one trace row
// and the one end delivery.
func TestWorkflowSweepHaltsOnGoneMember(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)

	if err := rt.Store.Delete("shop"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	tickChains(context.Background(), rt)

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
	if !strings.Contains(row.Reason, "member shop gone") {
		t.Errorf("halt reason = %q, want member shop gone", row.Reason)
	}
	if got := len(flowPendingChain(t, rt)); got != 1 {
		t.Errorf("pending chain deliveries = %d, want the one end delivery", got)
	}
}

// TestWorkflowStopSendsStopped pins the workflow stop: the member the engine
// awaits is stopped the way any binding is, and its stopped close marks the
// chain stopped with one trace row and the one end delivery.
func TestWorkflowStopSendsStopped(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)

	res, err := ChainStop(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainStop: %v", err)
	}
	if res.Action != "killed" {
		t.Errorf("action = %q, want the member's own stop (killed)", res.Action)
	}

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusStopped) {
		t.Fatalf("chain status = %q, want stopped", row.Status)
	}
	events := chainTrace(t, rt, "shop")
	if len(events) != 1 {
		t.Fatalf("trace = %+v, want the stop's one row", events)
	}
	ev, err := workflow.DecodeEvent(events[0].Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Kind != workflow.EventStopped {
		t.Errorf("event = %+v, want a stopped event", ev)
	}
	if got := len(flowPendingChain(t, rt)); got != 1 {
		t.Errorf("pending chain deliveries = %d, want the one end delivery", got)
	}
}

// TestWorkflowTerminalDeliversOnce pins the one end delivery: a terminal
// workflow queues exactly one payload, and a later terminal-reaching close
// writes no second trace row and no second delivery.
func TestWorkflowTerminalDeliversOnce(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	flowAdvance(t, rt, workflow.Event{Kind: workflow.EventStepClosed, Step: "build", Member: "builder", Round: 1, Status: "done"})

	halt := workflow.Event{Kind: workflow.EventStepClosed, Step: "review", Member: "assistant", Round: 1, Status: "halted", Reason: "no"}
	flowAdvance(t, rt, halt)
	if got := flowChainRow(t, rt); got.Status != string(workflow.StatusHalted) {
		t.Fatalf("status = %s (%q), want halted", got.Status, got.Reason)
	}
	rows := flowTraceRows(t, rt)
	if got := len(flowPendingChain(t, rt)); got != 1 {
		t.Fatalf("pending chain deliveries = %d, want exactly one", got)
	}

	flowAdvance(t, rt, halt)
	if got := flowTraceRows(t, rt); got != rows {
		t.Errorf("trace rows = %d, want still %d after a terminal replay", got, rows)
	}
	if got := len(flowPendingChain(t, rt)); got != 1 {
		t.Errorf("pending chain deliveries = %d, want still one", got)
	}
}
