package relevo

import (
	"context"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// attachForward claims webshop's open round onto a burst node at phase: the
// shape the serve side records when it forwards.
func attachForward(t *testing.T, rt Runtime, phase string) store.Forward {
	t.Helper()
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	fwd := store.Forward{
		Node: "b1", NodeBinding: "deadbeef-webshop", Round: b.Round, Phase: phase,
	}
	b.Burst = &store.BurstFacts{Forward: &fwd}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	return fwd
}

// TestStopForwardStampsAndSkipsLocalKill pins the forwarded stop: the
// request is stamped under the lock, the head's process is never touched,
// and the round stays open for the node stop to close. A running forward
// also fires the fan-out hook; a sending one never reached a node, so the
// tick closes it without a dial.
//
// Mutation check: drop the stamp and the saved flag stays false; drop the
// branch and the action is killed with the process gone.
func TestStopForwardStampsAndSkipsLocalKill(t *testing.T) {
	t.Parallel()

	for _, phase := range []string{"sending", "running"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()

			fr := newFakeRunner()
			rt, b := sentHeadless(t, fr)
			var hooked []store.Binding
			rt.StopForward = func(got store.Binding) {
				hooked = append(hooked, got)
			}
			attachForward(t, rt, phase)

			res, err := Stop(context.Background(), rt, "webshop", StopOptions{})
			if err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if res.Action != "stopping" {
				t.Errorf("Action = %q, want stopping", res.Action)
			}
			if res.Round != b.Round {
				t.Errorf("Round = %d, want the still-open %d", res.Round, b.Round)
			}

			got, err := rt.Store.Load("webshop")
			if err != nil {
				t.Fatal(err)
			}
			have := got.Burst.Forward
			if have == nil || !have.StopRequested {
				t.Fatalf("forward = %+v, want the stop stamped", got.Burst)
			}
			if got.Round != b.Round {
				t.Errorf("Round = %d, want %d: the round stays open", got.Round, b.Round)
			}
			if len(fr.kills) != 0 {
				t.Errorf("kills = %+v, want none: the head never stops a round the node owns", fr.kills)
			}
			if got.Builder.PID != b.Builder.PID {
				t.Errorf("Builder.PID = %d, want %d: the process is untouched", got.Builder.PID, b.Builder.PID)
			}
			if stops := stopEntries(t, rt, "webshop"); len(stops) != 0 {
				t.Errorf("stop entries = %+v, want none: nothing closed yet", stops)
			}
			if text := StopText("webshop", res); text != "webshop round 1 stop requested; the round is still open and closes as stopped" {
				t.Errorf("StopText = %q, want the still-open wording", text)
			}

			if phase == "running" {
				if len(hooked) != 1 || hooked[0].Name != "webshop" {
					t.Errorf("hook bindings = %+v, want the one recorded binding", hooked)
				}
			} else if len(hooked) != 0 {
				t.Errorf("hook bindings = %+v, want none: a sending forward never reached a node", hooked)
			}
		})
	}
}

// TestChainStopForwardedMemberFansOut pins that a chain stop of a forwarded
// member takes the same branch: ChainStop reaches Stop, the stamp lands, the
// hook fires, and the member's process survives.
func TestChainStopForwardedMemberFansOut(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)

	// The awaited member is whichever binding holds an open round.
	var member string
	var round int
	bindings, err := rt.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bindings {
		entries, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			t.Fatal(err)
		}
		if store.RoundOpen(entries, b.Round) {
			member, round = b.Name, b.Round
		}
	}
	if member == "" {
		t.Fatal("no member holds an open round")
	}
	b, err := rt.Store.Load(member)
	if err != nil {
		t.Fatal(err)
	}
	b.Burst = &store.BurstFacts{Forward: &store.Forward{
		Node: "b1", NodeBinding: "deadbeef-" + member, Round: round, Phase: "running",
	}}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	var hooked []store.Binding
	rt.StopForward = func(got store.Binding) {
		hooked = append(hooked, got)
	}
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("runner = %T, want *fakeRunner", rt.Runner)
	}

	res, err := ChainStop(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainStop: %v", err)
	}
	if res.Action != "stopping" {
		t.Errorf("Action = %q, want stopping", res.Action)
	}
	if len(hooked) != 1 || hooked[0].Name != member {
		t.Errorf("hook bindings = %+v, want the one forwarded member", hooked)
	}
	if len(fr.kills) != 0 {
		t.Errorf("kills = %+v, want none: a forwarded member is never killed", fr.kills)
	}
	back, err := rt.Store.Load(member)
	if err != nil {
		t.Fatal(err)
	}
	if back.Burst.Forward == nil || !back.Burst.Forward.StopRequested {
		t.Errorf("forward = %+v, want the stop stamped", back.Burst)
	}
	if back.Round != round {
		t.Errorf("Round = %d, want %d: the member's round stays open", back.Round, round)
	}
}

// TestStopRemoteForwardedRoundStillOpen pins what the laptop sees when the
// server takes the stop but the round is still open: no failure, and no
// claim the round closed. The head answers this way for a forwarded round
// and closes it on a later tick.
func TestStopRemoteForwardedRoundStillOpen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureStop}}}
	rt, _ := remoteStopFixture(t, fr)

	open := remote.BindingView{RoundState: remote.RoundRunning, Round: 1}
	fr.stopResp = open
	fr.getBindingResp = open

	res, err := Stop(ctx, rt, "api", StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v, want success when the server took the stop", err)
	}
	if res.Action != "stopping" {
		t.Errorf("Action = %q, want stopping, not a close the round never saw", res.Action)
	}
	const want = "api round 1 stop requested; the round is still open and closes as stopped"
	if text := StopText("api", res); text != want {
		t.Errorf("StopText = %q, want %q", text, want)
	}

	got, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 1 {
		t.Errorf("Round = %d, want 1: the round is still open", got.Round)
	}
	found := false
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Stop:") {
			found = true
		}
	}
	if !found {
		t.Errorf("calls = %v, want the server Stop among them", fr.calls)
	}
}

// stoppedForwardFixture is an owned binding with an open round and a
// stop-requested sending forward: the shape a head close starts from.
func stoppedForwardFixture(t *testing.T, fr *fakeRunner) (Runtime, store.Forward, *bool) {
	t.Helper()
	rt, b := ownedStopFixture(t, fr, false)
	fwd := store.Forward{
		Node: "b1", NodeBinding: "deadbeef-api", Round: b.Round, Phase: "sending",
		StopRequested: true,
	}
	b.Burst = &store.BurstFacts{Forward: &fwd}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	called := false
	rt.CloseServedRound = recordCloseServedRound(&called, "")
	return rt, fwd, &called
}

// TestCloseStopRequestedForwardClosesSendingAsDequeued pins the shared close
// the tick's paths run under the store lock: a stop-requested sending
// forward closes with the same entry and note as a queued local stop, the
// round advances, and the next round is free to queue.
func TestCloseStopRequestedForwardClosesSendingAsDequeued(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, fwd, called := stoppedForwardFixture(t, fr)

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		closed, err := CloseStopRequestedForward(context.Background(), rt, tx, "api", fwd, "dequeued")
		if err != nil {
			return err
		}
		if !closed {
			t.Error("closed = false, want the matching forward closed")
		}
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}

	got, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 2 {
		t.Errorf("Round = %d, want 2: the round closed", got.Round)
	}
	if got.Burst != nil {
		t.Errorf("Burst = %+v, want it cleared with the forward", got.Burst)
	}
	if !got.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero: a close never requeues", got.QueuedAt)
	}
	stops := stopEntries(t, rt, "api")
	if len(stops) != 1 || stops[0].Note != "stopped/dequeued" {
		t.Errorf("stop entries = %+v, want one stopped/dequeued", stops)
	}
	pending, found, err := rt.Store.PendingForMasterMind("api")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Note, "noreport stopped") {
		t.Errorf("report note = %q, want noreport stopped", pending.Note)
	}
	if !*called {
		t.Error("CloseServedRound hook not called: the close must record the served round")
	}
}

// TestCloseStopRequestedForwardLeavesOthersAlone pins the guard: without the
// flag, or for a forward that moved, nothing closes and nothing is saved.
//
// Mutation check: drop the StopRequested term and the unflagged forward
// closes here.
func TestCloseStopRequestedForwardLeavesOthersAlone(t *testing.T) {
	t.Parallel()

	t.Run("without the flag", func(t *testing.T) {
		t.Parallel()

		fr := newFakeRunner()
		rt, fwd, _ := stoppedForwardFixture(t, fr)
		b, err := rt.Store.Load("api")
		if err != nil {
			t.Fatal(err)
		}
		b.Burst.Forward.StopRequested = false
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		fwd.StopRequested = false

		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			closed, err := CloseStopRequestedForward(context.Background(), rt, tx, "api", fwd, "dequeued")
			if err != nil {
				return err
			}
			if closed {
				t.Error("closed = true, want nothing closed without the flag")
			}
			return nil
		}); err != nil {
			t.Fatalf("WithLock: %v", err)
		}
		if got, err := rt.Store.Load("api"); err != nil {
			t.Fatal(err)
		} else if got.Round != 1 {
			t.Errorf("Round = %d, want 1: the round stays open", got.Round)
		}
	})

	t.Run("superseded forward", func(t *testing.T) {
		t.Parallel()

		fr := newFakeRunner()
		rt, fwd, _ := stoppedForwardFixture(t, fr)
		fwd.Node = "b2"

		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			closed, err := CloseStopRequestedForward(context.Background(), rt, tx, "api", fwd, "dequeued")
			if err != nil {
				return err
			}
			if closed {
				t.Error("closed = true, want a moved forward left alone")
			}
			return nil
		}); err != nil {
			t.Fatalf("WithLock: %v", err)
		}
		if got, err := rt.Store.Load("api"); err != nil {
			t.Fatal(err)
		} else if got.Round != 1 {
			t.Errorf("Round = %d, want 1: the round stays open", got.Round)
		}
	})
}
