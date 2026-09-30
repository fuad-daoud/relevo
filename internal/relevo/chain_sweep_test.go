package relevo

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainToReviewer starts a chain and closes the builder's first round, so the
// chain is running and waiting on the reviewer.
func chainToReviewer(t *testing.T, rt Runtime) {
	t.Helper()
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())
}

// TestChainSweepHaltsOnAMissingMember pins the gone arm: the member the chain
// waits on has no record, so no close will ever come and the sweep ends the
// chain with the member named, one trace row and the one end delivery.
func TestChainSweepHaltsOnAMissingMember(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	chainToReviewer(t, rt)
	before := len(chainTrace(t, rt, "shop"))
	if err := rt.Store.Delete("shop-rev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	tickChains(context.Background(), rt)

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
	if row.Reason != "member shop-rev gone" {
		t.Errorf("halt reason = %q, want member shop-rev gone", row.Reason)
	}
	events := chainTrace(t, rt, "shop")
	if len(events) != before+1 {
		t.Fatalf("trace = %+v, want one row more than the %d before the sweep", events, before)
	}
	if last := events[len(events)-1]; last.Reason != "member shop-rev gone" {
		t.Errorf("sweep trace row reason = %q, want member shop-rev gone", last.Reason)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Errorf("pending chain deliveries = %d, want the one end delivery", len(pending))
	}
}

// TestChainSweepHaltsOnAMemberNeedsYou pins the other arm: a member sitting
// NEEDS YOU cannot run its round, so the chain halts with that member's own
// reason rather than waiting for a close that will not come.
func TestChainSweepHaltsOnAMemberNeedsYou(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	chainToReviewer(t, rt)

	const reason = "the reviewer asks for a human"
	rev := chainBinding(t, rt, "shop-rev")
	rev.State = store.StateNeedsYou
	rev.Halt = reason
	if err := rt.Store.Save(rev); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tickChains(context.Background(), rt)

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
	if row.Reason != reason {
		t.Errorf("halt reason = %q, want the member's reason %q", row.Reason, reason)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Errorf("pending chain deliveries = %d, want the one end delivery", len(pending))
	}
}

// TestChainSweepLeavesHaltedChainsAlone pins the second tick: the halt is
// terminal, so the sweep writes no second trace row and queues no second end
// delivery.
func TestChainSweepLeavesHaltedChainsAlone(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	chainToReviewer(t, rt)
	if err := rt.Store.Delete("shop-rev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	tickChains(context.Background(), rt)
	first := chainStoredRow(t, rt, "shop")
	rows := len(chainTrace(t, rt, "shop"))

	tickChains(context.Background(), rt)

	after := chainStoredRow(t, rt, "shop")
	if after.Status != first.Status || after.Reason != first.Reason {
		t.Errorf("chain row = %+v, want it unchanged at %+v", after, first)
	}
	if events := chainTrace(t, rt, "shop"); len(events) != rows {
		t.Errorf("trace = %+v, want still %d rows", events, rows)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Errorf("pending chain deliveries = %d, want still one", len(pending))
	}
}
