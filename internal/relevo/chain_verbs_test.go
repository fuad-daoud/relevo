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

// TestDoneRefusedOnARunningChainMember pins the first guard: done on a member
// of a running chain is refused the way a manual send is, because a DONE member
// is neither gone nor NEEDS YOU and the chain would wait forever for a close
// that can no longer come. Nothing is written: the member keeps its round.
func TestDoneRefusedOnARunningChainMember(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	logBefore := len(chainLog(t, rt, "shop"))

	for _, name := range []string{"shop", "shop-rev"} {
		_, err := Done(context.Background(), rt, name)
		if !errors.Is(err, ErrRunningChainMember) {
			t.Errorf("Done(%s) = %v, want ErrRunningChainMember", name, err)
		} else if !strings.Contains(err.Error(), "relevo stop shop first") {
			t.Errorf("Done(%s) message = %v, want it to name `relevo stop shop first`", name, err)
		}
	}

	if b := chainBinding(t, rt, "shop"); b.State != store.StateActive || b.RoundStartedAt.IsZero() {
		t.Errorf("builder = state %q round started %v, want it active with its round still open", b.State, b.RoundStartedAt)
	}
	if b := chainBinding(t, rt, "shop-rev"); b.State != store.StateActive {
		t.Errorf("reviewer state = %q, want it left active", b.State)
	}
	for _, e := range chainLog(t, rt, "shop")[logBefore:] {
		if e.Kind == store.KindStop {
			t.Errorf("a refused done appended %+v; want no stop entry", e)
		}
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
		t.Errorf("chain status = %q, want it running and untouched", row.Status)
	}
}

// TestUnbindRefusedOnARunningChainMember pins the second guard: unbind on a
// running chain's member is refused before any side effect, because the chain's
// next send needs the record. Both the delete and the archive shape refuse, and
// nothing is written.
func TestUnbindRefusedOnARunningChainMember(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	before := chainBinding(t, rt, "shop-rev")
	logBefore := len(chainLog(t, rt, "shop-rev"))
	rowBefore := chainStoredRow(t, rt, "shop")

	for _, archive := range []bool{false, true} {
		if _, err := Unbind(context.Background(), rt, "shop-rev", archive); !errors.Is(err, ErrRunningChainMember) {
			t.Errorf("Unbind(archive=%v) = %v, want ErrRunningChainMember", archive, err)
		}
	}

	after := chainBinding(t, rt, "shop-rev")
	if after.State != before.State || after.Round != before.Round || after.CWD != before.CWD {
		t.Errorf("member = %+v, want it unchanged from %+v", after, before)
	}
	if n := len(chainLog(t, rt, "shop-rev")); n != logBefore {
		t.Errorf("member log = %d entries, want the %d it had: a refused unbind writes nothing", n, logBefore)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != rowBefore.Status || !row.UpdatedAt.Equal(rowBefore.UpdatedAt) {
		t.Errorf("chain row = %+v, want it untouched at %+v", row, rowBefore)
	}
}

// assertChainMemberReleased runs both verbs on a chain that has left running
// and checks each took effect: done marks the member's record, unbind removes it.
func assertChainMemberReleased(t *testing.T, rt Runtime) {
	t.Helper()

	if _, err := Done(context.Background(), rt, "shop-rev"); err != nil {
		t.Fatalf("Done after the chain left running: %v", err)
	}
	if b := chainBinding(t, rt, "shop-rev"); b.State != store.StateDone {
		t.Errorf("member state = %q, want done", b.State)
	}
	if _, err := Unbind(context.Background(), rt, "shop-rev", false); err != nil {
		t.Fatalf("Unbind after the chain left running: %v", err)
	}
	if _, err := rt.Store.Load("shop-rev"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("member record after unbind = %v, want it gone", err)
	}
}

// TestDoneAndUnbindAllowedAfterTheChainStops pins the freedom half: the guard
// keys on a chain that is still running, so a stopped, halted or finished chain
// has handed its members back and both verbs work on them.
func TestDoneAndUnbindAllowedAfterTheChainStops(t *testing.T) {
	t.Parallel()

	t.Run("stopped", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stoppedChain(t, rt, ChainOptions{})
		assertChainMemberReleased(t, rt)
	})

	t.Run("halted", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		chainBuilderClose(t, rt, "shop", chainHaltedBody("stuck"))
		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
			t.Fatalf("chain status = %q, want halted", row.Status)
		}
		assertChainMemberReleased(t, rt)
	})

	t.Run("done", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		chainBuilderClose(t, rt, "shop", chainDoneBody())
		chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusDone) {
			t.Fatalf("chain status = %q, want done", row.Status)
		}
		assertChainMemberReleased(t, rt)
	})
}

// TestStopOnARunningChainMemberStaysAllowed pins that stop is not guarded: a
// member's stop closes its round through the chain's own stopped event, which is
// the spec's "a stopped member stops the chain". Fails if the guard is later put
// on Stop.
func TestStopOnARunningChainMemberStaysAllowed(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})

	res, err := Stop(context.Background(), rt, "shop", StopOptions{})
	if err != nil {
		t.Fatalf("Stop on a running chain's awaited member = %v, want it allowed", err)
	}
	if res.Action != "killed" {
		t.Errorf("action = %q, want the member's own stop (killed)", res.Action)
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
	if ev.Kind != chain.EventStopped {
		t.Errorf("event = %+v, want a stopped close", ev)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Errorf("pending chain deliveries = %d, want the one end delivery", len(pending))
	}
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
