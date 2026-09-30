package relevo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestChainStopStopsTheActiveMemberAndMarksTheChainStopped pins the ordinary
// chain stop: the member the chain waits on is stopped the way any binding is,
// and its stopped close is the event that marks the chain stopped and queues its
// one end delivery.
func TestChainStopStopsTheActiveMemberAndMarksTheChainStopped(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	res, err := ChainStop(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainStop: %v", err)
	}
	if res.Action != "killed" {
		t.Errorf("action = %q, want the member's own stop (killed)", res.Action)
	}
	if res.Round != 1 {
		t.Errorf("round = %d, want the round that was stopped (1)", res.Round)
	}

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusStopped) {
		t.Errorf("chain status = %q, want stopped", row.Status)
	}
	events := chainTrace(t, rt, "shop")
	if len(events) != 1 {
		t.Fatalf("trace = %+v, want the stop's one row", events)
	}
	ev, err := chain.DecodeEvent(events[0].Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Kind != chain.EventStopped || ev.Member != chain.MemberBuilder {
		t.Errorf("event = %+v, want the builder's stopped close", ev)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Errorf("pending chain deliveries = %d, want the one end delivery", len(pending))
	}
}

// TestChainStopWithAClosedMemberRoundMarksTheChainStopped pins the direct path:
// when the awaited member has no open round, nothing would ever raise the
// stopped event, so the chain is stopped under the lock with its own trace row
// and its one end delivery.
func TestChainStopWithAClosedMemberRoundMarksTheChainStopped(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	// The member's round already closed but the chain has not mapped it: there
	// is nothing on the member for an ordinary stop to end.
	b := chainBinding(t, rt, "shop")
	b.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	res, err := ChainStop(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainStop: %v", err)
	}
	if res.Action != ChainStopActionStopped {
		t.Errorf("action = %q, want %q", res.Action, ChainStopActionStopped)
	}

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusStopped) {
		t.Errorf("chain status = %q, want stopped", row.Status)
	}
	events := chainTrace(t, rt, "shop")
	if len(events) != 1 {
		t.Fatalf("trace = %+v, want the chain's one row", events)
	}
	if events[0].Reason != chainStopNoRoundReason {
		t.Errorf("trace reason = %q, want %q", events[0].Reason, chainStopNoRoundReason)
	}
	ev, err := chain.DecodeEvent(events[0].Event)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Kind != chain.EventStopped {
		t.Errorf("event = %+v, want a stopped event", ev)
	}
	act, err := chain.DecodeAction(events[0].Action)
	if err != nil {
		t.Fatalf("DecodeAction: %v", err)
	}
	if act.Kind != chain.ActionStop {
		t.Errorf("action = %+v, want stop", act)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Errorf("pending chain deliveries = %d, want the one end delivery", len(pending))
	}
}

// TestChainStopOnAStoppedOrHaltedChainIsNothingToStop pins the answer for a
// chain that is not running: nothing to stop, and nothing changed. It is the
// same answer `relevo stop` gives a binding with no open round.
func TestChainStopOnAStoppedOrHaltedChainIsNothingToStop(t *testing.T) {
	t.Parallel()

	t.Run("stopped", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stoppedChain(t, rt, ChainOptions{})

		if _, err := ChainStop(context.Background(), rt, "shop"); !errors.Is(err, ErrNothingToStop) {
			t.Errorf("ChainStop = %v, want ErrNothingToStop", err)
		}
		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusStopped) {
			t.Errorf("chain status = %q, want it untouched", row.Status)
		}
	})

	t.Run("halted", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		chainBuilderClose(t, rt, "shop", chainHaltedBody("stuck"))
		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
			t.Fatalf("chain status = %q, want halted", row.Status)
		}

		if _, err := ChainStop(context.Background(), rt, "shop"); !errors.Is(err, ErrNothingToStop) {
			t.Errorf("ChainStop = %v, want ErrNothingToStop", err)
		}
	})
}

// TestChainDoneRefusedWhileRunning pins the refusal: a live chain is the
// human's to stop first, and the refusal says so.
func TestChainDoneRefusedWhileRunning(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	_, err := ChainDone(context.Background(), rt, "shop")
	if err == nil {
		t.Fatal("ChainDone on a running chain = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "relevo stop shop first") {
		t.Errorf("err = %v, want it to name `relevo stop shop first`", err)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
		t.Errorf("chain status = %q, want it untouched", row.Status)
	}
}

// TestChainDoneReleasesEveryMemberAndMarksDone pins the release: every member
// that exists is marked done, builder first, the chain is marked done with one
// trace row, and no MasterMind delivery is queued -- the human ran the verb.
func TestChainDoneReleasesEveryMemberAndMarksDone(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	stoppedChain(t, rt, ChainOptions{})
	pendingBefore := len(chainPendingChain(t, rt, "shop"))
	traceBefore := len(chainTrace(t, rt, "shop"))

	// The builder's worktree is on disk (the fake git never cut one), so the
	// release is a real removal rather than a recorded path already gone.
	builder := chainBinding(t, rt, "shop")
	if err := os.MkdirAll(builder.Worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll worktree: %v", err)
	}

	res, err := ChainDone(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainDone: %v", err)
	}

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusDone) || row.Phase != string(chain.PhaseFinished) {
		t.Errorf("chain = status %q phase %q, want done/finished", row.Status, row.Phase)
	}
	for _, name := range chainMembersOf(row) {
		if b := chainBinding(t, rt, name); b.State != store.StateDone {
			t.Errorf("member %s state = %q, want done", name, b.State)
		}
	}

	events := chainTrace(t, rt, "shop")
	if len(events) != traceBefore+1 {
		t.Errorf("trace = %d rows, want one more than %d", len(events), traceBefore)
	}
	act, err := chain.DecodeAction(events[len(events)-1].Action)
	if err != nil {
		t.Fatalf("DecodeAction: %v", err)
	}
	if act.Kind != chain.ActionFinish {
		t.Errorf("done row action = %+v, want finish", act)
	}

	if pending := chainPendingChain(t, rt, "shop"); len(pending) != pendingBefore {
		t.Errorf("pending chain deliveries = %d, want %d: done tells nobody", len(pending), pendingBefore)
	}

	// The result is the chain's own tree: the builder's, not the last member's.
	if res.Branch != "relevo/shop" {
		t.Errorf("result branch = %q, want the chain's branch", res.Branch)
	}
	if res.WorktreeRemoved == "" {
		t.Errorf("result = %+v, want the builder's worktree released", res)
	}
}

// TestChainDoneLeavesTheChainAloneWhenAMemberFails pins the retryable failure:
// a member that cannot be released returns its error and the chain keeps its
// status, so the verb can be run again.
func TestChainDoneLeavesTheChainAloneWhenAMemberFails(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	stoppedChain(t, rt, ChainOptions{})

	// The builder still has a live process, and the runner refuses to kill it:
	// Done marks the member done and returns ErrStopFailed.
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatal("the chain runtime must carry a fakeRunner")
	}
	b := chainBinding(t, rt, "shop")
	b.Builder.Mode = store.ModeHeadless
	b.Builder.PID = 4242
	b.Builder.StartedAt = baseTime.Unix()
	b.Builder.LogPath = rt.Store.StreamPath(b.Name, b.Round)
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fr.script(4242, true)
	fr.killErr = errors.New("kill refused")

	if _, err := ChainDone(context.Background(), rt, "shop"); !errors.Is(err, ErrStopFailed) {
		t.Fatalf("ChainDone = %v, want ErrStopFailed", err)
	}

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusStopped) {
		t.Errorf("chain status = %q, want the stop it had: a member failure leaves the chain alone", row.Status)
	}
	if events := chainTrace(t, rt, "shop"); len(events) != 1 {
		t.Errorf("trace = %+v, want no done row for a failed release", events)
	}
}
