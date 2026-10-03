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
	"github.com/fuad-daoud/relevo/internal/workflow"
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
	// is nothing on the member for an ordinary stop to end. The report is what
	// closed it -- an open round is a prompt with no report, whichever
	// timestamps the binding happens to carry.
	b := chainBinding(t, rt, "shop")
	b.RoundStartedAt = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := rt.Store.AppendLog("shop", store.LogEntry{
		TS: rt.Now().UTC(), Round: b.Round, Direction: store.DirToMasterMind,
		Kind: store.KindReport, Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
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

// TestChainStopOnAHaltedChainWithAnOpenMemberRoundStopsTheMember pins the one
// place a chain that is not running still has something to stop: the awaited
// member's manual round. The builder carries the chain's own name, so this verb
// is the only route to that round, and it must stop it rather than answer
// nothing to stop. A chain with no open member round keeps that answer.
func TestChainStopOnAHaltedChainWithAnOpenMemberRoundStopsTheMember(t *testing.T) {
	t.Parallel()

	t.Run("halted", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		startedChain(t, rt, ChainOptions{})
		chainBuilderClose(t, rt, "shop", chainHaltedBody("stuck"))
		if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
			t.Fatalf("chain status = %q, want halted", row.Status)
		}
		if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
			t.Fatalf("manual Send to the builder: %v", err)
		}
		assertChainStopEndsTheOpenMemberRound(t, rt, chain.StatusHalted)
	})

	t.Run("stopped", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stoppedChain(t, rt, ChainOptions{})
		if _, err := Send(context.Background(), rt, "shop", writePlan(t, "carry on"), SendOptions{}); err != nil {
			t.Fatalf("manual Send to the builder: %v", err)
		}
		assertChainStopEndsTheOpenMemberRound(t, rt, chain.StatusStopped)
	})
}

// assertChainStopEndsTheOpenMemberRound pins the delegation: `relevo stop
// <chain>` on a chain in status want, whose awaited builder carries the chain's
// own name, stops that member's open round the way any binding's round is
// stopped, and the chain row is left where it was -- the round was the work,
// not the chain.
func assertChainStopEndsTheOpenMemberRound(t *testing.T, rt Runtime, want chain.Status) {
	t.Helper()

	open := chainBinding(t, rt, "shop")
	if !HasPromptEntry(chainLog(t, rt, "shop"), open.Round) {
		t.Fatalf("test premise: the builder's manual round %d must be open", open.Round)
	}

	res, err := ChainStop(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainStop on a %s chain with an open member round: %v", want, err)
	}
	if res.Action != "killed" || res.Round != open.Round {
		t.Errorf("result = %+v, want the member's own stop of round %d", res, open.Round)
	}
	if store.RoundOpen(chainLog(t, rt, "shop"), open.Round) {
		t.Errorf("the builder's round %d is still open: the stop delegated nothing", open.Round)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(want) {
		t.Errorf("chain status = %q, want it left at %q: stopping a member round is not the chain's own end", row.Status, want)
	}
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

// TestRefuseRunningMemberReadsState pins the refusal's state read: a custom
// workflow's member sits in chain_member, never in a legacy part column, and a
// manual send to it while the chain runs is still refused.
func TestRefuseRunningMemberReadsState(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)

	row := chainStoredRow(t, rt, "shop")
	if row.Reviewer != "" || row.Planner != "" || row.Security != "" {
		t.Fatalf("member columns = %q/%q/%q, want none for a custom workflow",
			row.Reviewer, row.Planner, row.Security)
	}

	err := rt.Store.WithLock(func(tx *store.Tx) error { return refuseRunningChainMember(tx, "shop-assistant") })
	if !errors.Is(err, ErrRunningChainMember) {
		t.Errorf("refuseRunningChainMember = %v, want ErrRunningChainMember", err)
	}
	if err := refuseRunningChainMemberStore(rt.Store, "shop-assistant"); !errors.Is(err, ErrRunningChainMember) {
		t.Errorf("refuseRunningChainMemberStore = %v, want ErrRunningChainMember", err)
	}
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

// TestWorkflowChainDoneReleasesEveryMember pins the release: a custom
// workflow's non-legacy member is released too, not only the member the legacy
// builder column names.
func TestWorkflowChainDoneReleasesEveryMember(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}

	if _, err := ChainDone(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainDone: %v", err)
	}
	for _, name := range []string{"shop", "shop-assistant"} {
		if b := chainBinding(t, rt, name); b.State != store.StateDone {
			t.Errorf("member %s state = %q, want done", name, b.State)
		}
	}
}

// TestWorkflowChainDoneWritesTheEngineState pins done on a workflow chain: the
// engine state's status becomes done, so a later read sees done rather than the
// stopped the chain carried before the verb.
func TestWorkflowChainDoneWritesTheEngineState(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	stoppedChain(t, rt, ChainOptions{})

	row := chainStoredRow(t, rt, "shop")
	st, err := chainWorkflowState(row)
	if err != nil {
		t.Fatalf("chainWorkflowState before done: %v", err)
	}
	if st.Status != workflow.StatusStopped {
		t.Fatalf("state before done = %q, want stopped", st.Status)
	}

	if _, err := ChainDone(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainDone: %v", err)
	}

	row = chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusDone) {
		t.Fatalf("chain status = %q, want done", row.Status)
	}
	st, err = chainWorkflowState(row)
	if err != nil {
		t.Fatalf("chainWorkflowState after done: %v", err)
	}
	if st.Status != workflow.StatusDone {
		t.Errorf("state status after done = %q, want done", st.Status)
	}
}

// TestChainDoneOnAWorkflowlessChainNeverInternal pins the row the engine's own
// conversion cannot leave behind on the done path: `chain --done` read its
// missing workflow or its missing engine state as a plain error, which the CLI
// reports as internal. A row with no workflow is migrated through the same
// single-row conversion the daemon's start-up sweep runs, and done then
// releases it as any other chain. A row that still carries neither a workflow
// nor a state -- or one whose stored definition no longer decodes -- is refused
// in the input class instead, naming its own reason and the command that reads
// it, and it is refused before any member is released, so the refusal leaves
// every member and the chain's own status untouched and the verb retryable.
func TestChainDoneOnAWorkflowlessChainNeverInternal(t *testing.T) {
	t.Parallel()

	t.Run("a row with no workflow is migrated and released", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stripChainToLegacy(t, rt, "shop")
		// The copies of the round files the chain's seeds named are no longer
		// needed once it is done, so the sweep has something to remove.
		inputs := rt.Store.ChainInputDir("shop")
		if err := os.MkdirAll(inputs, 0o755); err != nil {
			t.Fatalf("MkdirAll the chain inputs: %v", err)
		}
		if err := os.WriteFile(inputs+"/seed-1.md", []byte("seed\n"), 0o644); err != nil {
			t.Fatalf("write the chain inputs: %v", err)
		}
		pendingBefore := len(chainPendingChain(t, rt, "shop"))
		traceBefore := len(chainTrace(t, rt, "shop"))

		if _, err := ChainDone(context.Background(), rt, "shop"); err != nil {
			t.Fatalf("ChainDone over a legacy row: %v", err)
		}

		row := chainStoredRow(t, rt, "shop")
		if row.Status != string(chain.StatusDone) {
			t.Errorf("chain status = %q, want done", row.Status)
		}
		if len(row.WorkflowJSON) == 0 || len(row.StateJSON) == 0 {
			t.Errorf("chain carries workflow %d and state %d bytes, want the migration to have written both",
				len(row.WorkflowJSON), len(row.StateJSON))
		}
		st, err := chainWorkflowState(row)
		if err != nil {
			t.Fatalf("chainWorkflowState after the migrated done: %v", err)
		}
		if st.Status != workflow.StatusDone {
			t.Errorf("engine state status = %q, want done", st.Status)
		}

		members, err := rt.Store.ChainMembers("shop")
		if err != nil {
			t.Fatalf("ChainMembers shop: %v", err)
		}
		if len(members) == 0 {
			t.Fatal("the chain has no members: the fixture did not start one")
		}
		if members[0].Binding != "shop" {
			t.Errorf("first member = %q, want the builder shop", members[0].Binding)
		}
		for _, m := range members {
			if b := chainBinding(t, rt, m.Binding); b.State != store.StateDone {
				t.Errorf("member %s state = %q, want done", m.Binding, b.State)
			}
		}

		events := chainTrace(t, rt, "shop")
		if len(events) != traceBefore+1 {
			t.Fatalf("trace = %d rows, want one more than %d", len(events), traceBefore)
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
		if _, err := os.Stat(inputs); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the chain inputs directory %s = %v, want it swept", inputs, err)
		}
	})

	t.Run("an unconvertible row is refused, not internal", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stripChainEngine(t, rt, true)
		// Settings the conversion reads and cannot parse: the migration cannot
		// bring this row onto the engine, which is what halts it in the sweep.
		corruptChainSettings(t, rt, "shop")
		traceBefore := len(chainTrace(t, rt, "shop"))

		_, err := ChainDone(context.Background(), rt, "shop")
		if err == nil {
			t.Fatal("ChainDone over an unconvertible row = nil, want a refusal")
		}
		assertDoneRefusedNotInternal(t, err)
		if !strings.Contains(err.Error(), "carries no workflow") {
			t.Errorf("refusal = %q, want the row's own reason named as the cause", err)
		}
		assertDoneRefusedTouchedNothing(t, rt, "shop", traceBefore)
	})

	t.Run("a row with no engine state is refused, not internal", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stripChainEngine(t, rt, false)
		traceBefore := len(chainTrace(t, rt, "shop"))

		_, err := ChainDone(context.Background(), rt, "shop")
		if err == nil {
			t.Fatal("ChainDone over a state-less row = nil, want a refusal")
		}
		assertDoneRefusedNotInternal(t, err)
		if !strings.Contains(err.Error(), "has no engine state") {
			t.Errorf("refusal = %q, want the missing engine state named as the cause", err)
		}
		assertDoneRefusedTouchedNothing(t, rt, "shop", traceBefore)
	})

	t.Run("a row whose definition no longer decodes is refused, not internal", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stoppedChain(t, rt, ChainOptions{})
		corruptChainWorkflow(t, rt, "shop")
		traceBefore := len(chainTrace(t, rt, "shop"))

		_, err := ChainDone(context.Background(), rt, "shop")
		if err == nil {
			t.Fatal("ChainDone over a corrupt definition = nil, want a refusal")
		}
		assertDoneRefusedNotInternal(t, err)
		assertDoneRefusedTouchedNothing(t, rt, "shop", traceBefore)
	})
}

// stripChainToLegacy leaves a stopped chain row as one that predates the
// workflow engine: its definition and its engine state are gone and its legacy
// columns and member rows are all it holds. Nothing on the row says a
// conversion failed, so a done over it is the plainly migratable case.
func stripChainToLegacy(t *testing.T, rt Runtime, name string) {
	t.Helper()

	stoppedChain(t, rt, ChainOptions{Name: name})
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain(name)
		if err != nil {
			return err
		}
		c.WorkflowJSON = nil
		c.StateJSON = nil
		c.UpdatedAt = rt.Now().UTC()
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("strip %s onto the legacy shape: %v", name, err)
	}
}

// corruptChainWorkflow writes a chain definition no workflow parser can read.
func corruptChainWorkflow(t *testing.T, rt Runtime, name string) {
	t.Helper()

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		c, err := tx.Chain(name)
		if err != nil {
			return err
		}
		c.WorkflowJSON = []byte("not json")
		c.UpdatedAt = rt.Now().UTC()
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("corrupt the chain workflow: %v", err)
	}
}

// assertDoneRefusedNotInternal pins the class of a done refusal: the input
// class, so the CLI reports it as refused or conflict and never as an internal
// failure; it names the row's own reason, the done verb rather than the resume
// verb the shared pattern carries, and the command that reads the row.
func assertDoneRefusedNotInternal(t *testing.T, err error) {
	t.Helper()

	if !errors.Is(err, ErrRefused) {
		t.Fatalf("refusal %v is not in the input class: errors.Is(err, ErrRefused) is false, so the CLI reads it as internal", err)
	}
	if !strings.Contains(err.Error(), "cannot be done") {
		t.Errorf("refusal %q is not worded for the done verb", err)
	}
	if strings.Contains(err.Error(), "cannot be resumed") {
		t.Errorf("refusal %q reuses the resume verb's wording", err)
	}
	if !strings.Contains(err.Error(), "relevo status shop") {
		t.Errorf("refusal %q names no working next step", err)
	}
}

// assertDoneRefusedTouchedNothing pins that a refusal released nothing: every
// member keeps its own state, the chain carries no done row of its own, and the
// verb can be run again.
func assertDoneRefusedTouchedNothing(t *testing.T, rt Runtime, name string, traceBefore int) {
	t.Helper()

	if b := chainBinding(t, rt, name); b.State == store.StateDone {
		t.Errorf("member %s state = %q, want it untouched by a refusal", name, b.State)
	}
	if row := chainStoredRow(t, rt, name); row.Status == string(chain.StatusDone) {
		t.Errorf("chain status = %q, want it untouched by a refusal", row.Status)
	}
	if events := chainTrace(t, rt, name); len(events) != traceBefore {
		t.Errorf("trace = %d rows, want the %d it had: a refusal writes no done row", len(events), traceBefore)
	}
}

// TestWorkflowEndDeliveryCarrierIsAMember pins the carrier: the one end
// delivery lands on the first surviving member in chain_member order, even when
// that member fills no legacy part.
func TestWorkflowEndDeliveryCarrierIsAMember(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)

	// The builder's record is gone, so the first surviving member is the
	// assistant, which the legacy columns never name.
	if err := rt.Store.Delete("shop"); err != nil {
		t.Fatalf("Delete shop: %v", err)
	}
	tickChains(context.Background(), rt)

	row := flowChainRow(t, rt)
	if row.Status != string(workflow.StatusHalted) {
		t.Fatalf("chain status = %q, want halted", row.Status)
	}
	if pending := chainPendingChain(t, rt, "shop-assistant"); len(pending) != 1 {
		t.Fatalf("pending on shop-assistant = %d, want the one end delivery", len(pending))
	}

	var carrier string
	var found bool
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		name, _, ok, cerr := chainDeliveryMember(tx, row)
		carrier, found = name, ok
		return cerr
	}); err != nil {
		t.Fatalf("chainDeliveryMember: %v", err)
	}
	if !found || carrier != "shop-assistant" {
		t.Errorf("carrier = %q (found %v), want shop-assistant", carrier, found)
	}
}
