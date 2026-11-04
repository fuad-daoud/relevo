package relevo

import (
	"context"
	"errors"
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

// attachRelease records b's burst ship facts on webshop: a forward at the
// given phase onto the first named node plus a ship entry for every named
// node, and the node binding name the ship repeats per node.
func attachRelease(t *testing.T, rt Runtime, phase string, nodes ...string) store.BurstFacts {
	t.Helper()
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	fwd := &store.Forward{
		Node: nodes[0], NodeBinding: "deadbeef-webshop", Round: b.Round, Phase: phase,
	}
	shipped := make(map[string]string, len(nodes))
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		shipped[n] = strings.Repeat("a", 40)
		names = append(names, "deadbeef-webshop")
	}
	bf := store.BurstFacts{Forward: fwd, Shipped: shipped, NodeBindings: names}
	b.Burst = &bf
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	return bf
}

// TestDoneFansOutPreSettleRecordAndClearsShipFacts pins the release seam:
// Done hands the hook the record as it stood before the settle -- ship
// facts and all -- only after the settle landed, and the settled record
// keeps a closed forward while the ship facts it just released are gone.
// The next forward after a resume then ships a full bundle again.
//
// Mutation check: clear after the save and the saved record keeps Shipped;
// hand the hook the post-save record and its Shipped is already empty;
// clear the forward too and a closed round loses its ack.
func TestDoneFansOutPreSettleRecordAndClearsShipFacts(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := sentHeadless(t, fr)
	attachRelease(t, rt, "closed", "b1", "b2")
	var hooked []store.Binding
	rt.UnbindNodes = func(got store.Binding) {
		hooked = append(hooked, got)
	}

	if _, err := Done(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Done: %v", err)
	}

	if len(hooked) != 1 {
		t.Fatalf("hook calls = %d, want the one released binding", len(hooked))
	}
	got := hooked[0]
	if got.Burst == nil || len(got.Burst.Shipped) != 2 {
		t.Errorf("hooked record burst = %+v, want both ship entries", got.Burst)
	}

	back, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if back.State != store.StateDone {
		t.Errorf("State = %q, want done", back.State)
	}
	if back.Burst == nil || len(back.Burst.Shipped) != 0 || len(back.Burst.NodeBindings) != 0 {
		t.Errorf("settled burst = %+v, want the ship facts cleared", back.Burst)
	}
	if f := back.Burst.Forward; f == nil || f.Phase != "closed" {
		t.Errorf("settled forward = %+v, want the closed one kept for its ack", back.Burst.Forward)
	}
}

// TestDoneWithoutShipFactsSkipsNodeFanOut pins the guard: a binding that
// never forwarded settles without a hook call, so no release dials a node.
func TestDoneWithoutShipFactsSkipsNodeFanOut(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := sentHeadless(t, fr)
	hits := 0
	rt.UnbindNodes = func(store.Binding) { hits++ }

	if _, err := Done(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if hits != 0 {
		t.Errorf("hook calls = %d, want none for a binding that never shipped", hits)
	}
}

// TestDoneRefusedFansOutNothing pins that a refused done releases nothing
// locally and dials nothing: the hook runs only on the settled record.
func TestDoneRefusedFansOutNothing(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := sentHeadless(t, fr)
	attachRelease(t, rt, "closed", "b1")
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	b.Owner = "alice"
	b.QueuedAt = rt.Now()
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	hits := 0
	rt.UnbindNodes = func(store.Binding) { hits++ }

	if _, err := Done(context.Background(), rt, "webshop"); err == nil {
		t.Fatal("Done = nil error, want the queued refusal")
	}
	if hits != 0 {
		t.Errorf("hook calls = %d, want none on a refused done", hits)
	}
}

// TestUnbindRefusesLiveForwardWithoutForce pins the open-round rule at the
// engine seam: a round the forward owns cannot be released by an
// unforced unbind, and the refusal dials nothing.
func TestUnbindRefusesLiveForwardWithoutForce(t *testing.T) {
	t.Parallel()

	for _, phase := range []string{"sending", "running"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()

			fr := newFakeRunner()
			rt, _ := sentHeadless(t, fr)
			attachRelease(t, rt, phase, "b1")
			hits := 0
			rt.UnbindNodes = func(store.Binding) { hits++ }

			_, err := Unbind(context.Background(), rt, "webshop", true)
			if !errors.Is(err, ErrForwardRunning) {
				t.Fatalf("Unbind err = %v, want ErrForwardRunning", err)
			}
			if hits != 0 {
				t.Errorf("hook calls = %d, want none on a refused unbind", hits)
			}
			if _, err := rt.Store.Load("webshop"); err != nil {
				t.Errorf("load after refusal: %v, want the record kept", err)
			}
		})
	}
}

// TestUnbindForceFansOutShippedAndForwardNodes pins the forced release of
// a live forward: it archives and the hook carries the record whose ship
// entries and forward node are all named -- the forward's node included,
// or the node keeps running a round nobody can read.
func TestUnbindForceFansOutShippedAndForwardNodes(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := sentHeadless(t, fr)
	// b2 holds the ship entry, b1 the sending claim that has none yet.
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	fwd := store.Forward{Node: "b2", NodeBinding: "deadbeef-webshop", Round: b.Round, Phase: "sending"}
	b.Burst = &store.BurstFacts{
		Forward:      &fwd,
		Shipped:      map[string]string{"b1": strings.Repeat("a", 40)},
		NodeBindings: []string{"deadbeef-webshop"},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	var hooked []store.Binding
	rt.UnbindNodes = func(got store.Binding) {
		hooked = append(hooked, got)
	}

	res, err := UnbindWith(context.Background(), rt, "webshop", true, true)
	if err != nil {
		t.Fatalf("UnbindWith force: %v", err)
	}
	if !res.Archived {
		t.Errorf("Archived = false, want the forced release archived")
	}
	if len(hooked) != 1 || hooked[0].Burst == nil {
		t.Fatalf("hook calls = %+v, want the one released record", hooked)
	}
	if _, ok := hooked[0].Burst.Shipped["b1"]; !ok {
		t.Errorf("hooked shipped = %+v, want the ship entry", hooked[0].Burst.Shipped)
	}
	if f := hooked[0].Burst.Forward; f == nil || f.Node != "b2" {
		t.Errorf("hooked forward = %+v, want the forward's own node named", hooked[0].Burst.Forward)
	}
}

// TestUnbindClosedForwardReleasesAndFansOut pins that a closed forward is
// not a live one: the unforced release settles and the hook carries the
// record, because the round came home and only the node's receipt is owed.
func TestUnbindClosedForwardReleasesAndFansOut(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := sentHeadless(t, fr)
	attachRelease(t, rt, "closed", "b1")
	var hooked []store.Binding
	rt.UnbindNodes = func(got store.Binding) {
		hooked = append(hooked, got)
	}

	if _, err := Unbind(context.Background(), rt, "webshop", true); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if len(hooked) != 1 {
		t.Fatalf("hook calls = %d, want the one released binding", len(hooked))
	}
}

// TestChainDoneFansOutMemberShipFacts pins the chain path: chain done
// releases each member through the same Done seam, so a member holding
// ship facts fans its nodes out per member, not once for the chain.
func TestChainDoneFansOutMemberShipFacts(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowReviewWorkflow)
	if _, err := ChainStop(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainStop: %v", err)
	}

	member := ""
	bindings, err := rt.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bindings {
		member = b.Name
		break
	}
	if member == "" {
		t.Fatal("chain left no member binding")
	}
	b, err := rt.Store.Load(member)
	if err != nil {
		t.Fatal(err)
	}
	b.Burst = &store.BurstFacts{
		Shipped:      map[string]string{"b1": strings.Repeat("a", 40)},
		NodeBindings: []string{"deadbeef-" + member},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	var hooked []store.Binding
	rt.UnbindNodes = func(got store.Binding) {
		hooked = append(hooked, got)
	}

	if _, err := ChainDone(context.Background(), rt, "shop"); err != nil {
		t.Fatalf("ChainDone: %v", err)
	}
	found := false
	for _, got := range hooked {
		if got.Name == member {
			found = true
		}
	}
	if !found {
		t.Errorf("hooked = %+v, want the shipped member among them", hooked)
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

	t.Run("a round that moved on", func(t *testing.T) {
		t.Parallel()

		rt, fwd, _ := stoppedForwardFixture(t, newFakeRunner())
		fwd.Round++
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			closed, err := CloseStopRequestedForward(context.Background(), rt, tx, "api", fwd, "dequeued")
			if err != nil {
				return err
			}
			if closed {
				t.Error("closed = true, want nothing closed for another round")
			}
			return nil
		}); err != nil {
			t.Fatalf("WithLock: %v", err)
		}
	})

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
