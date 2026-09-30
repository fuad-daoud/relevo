package delivery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// fakeClaimStore is the map-backed ClaimStore the plan asks for: a claim
// present for a mastermind id means live, absent means not.
type fakeClaimStore map[string]*Claim

func (f fakeClaimStore) Live(mastermind string, now time.Time) (*Claim, error) {
	return f[mastermind], nil
}

func (f fakeClaimStore) Write(c Claim, now time.Time) error {
	f[c.MasterMind] = &c
	return nil
}

func (f fakeClaimStore) Remove(mastermind string, pid int) error {
	delete(f, mastermind)
	return nil
}

// routeRuntime is a minimal Deps for the delivery-route tests: a temp
// store, a fixed clock and nothing else wired.
func routeRuntime(t *testing.T) Deps {
	t.Helper()
	return Deps{
		Store: store.New(t.TempDir()),
		Now:   func() time.Time { return baseTime },
	}
}

// seedPending saves an active binding and one unconfirmed mastermind-bound
// entry, which is exactly what DeliverPending and Pull work on.
func seedPending(t *testing.T, rt Deps, name, mastermindID, kind string) store.Binding {
	t.Helper()
	b := store.Binding{
		Name:         name,
		CWD:          "/repo/" + name,
		Round:        1,
		State:        store.StateActive,
		MasterMind:   store.Endpoint{Kind: kind, SessionID: "sess"},
		MasterMindID: mastermindID,
		Builder:      store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		return Queue(context.Background(), rt, tx, name, store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Payload: "round 1 report", Path: "/tmp/report.md",
		})
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return b
}

// deliverOnce runs DeliverPending under the state lock, the way the daemon
// and `relevo wait` do.
func deliverOnce(t *testing.T, rt Deps, b store.Binding) (store.Binding, Delivery) {
	t.Helper()
	var (
		next store.Binding
		got  Delivery
	)
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		next, got, err = DeliverPending(context.Background(), rt, tx, b)
		return err
	}); err != nil {
		t.Fatalf("DeliverPending: %v", err)
	}
	return next, got
}

// notMineDeliverer is OutcomeNotMine: a deliverer that refuses this
// mastermind. Its payload must stay pending -- there is no pane to fall through
// to any more.
type notMineDeliverer struct{}

func (notMineDeliverer) Deliver(context.Context, store.Endpoint, string, string, time.Time) (Outcome, string, error) {
	return OutcomeNotMine, "not mine to deliver", nil
}

func (notMineDeliverer) Confirm(context.Context, store.Endpoint, string, time.Time) (Outcome, string, error) {
	return OutcomeNotMine, "not mine to deliver", nil
}

func (notMineDeliverer) ConfirmOnce(context.Context, store.Endpoint, string, time.Time) (Outcome, string, error) {
	return OutcomeNotMine, "not mine to deliver", nil
}

// TestDeliverPendingNoRouteStaysPendingAsPull is the plan's required case:
// with no live claim and no deliverer for the mastermind's kind, the entry
// stays pending with route=pull. For a Claude Code mastermind in tools mode
// that is the normal path, not a fault (D6).
func TestDeliverPendingNoRouteStaysPendingAsPull(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	_, got := deliverOnce(t, rt, b)

	if got.Delivered {
		t.Error("nothing can deliver this payload; Delivered must be false")
	}
	if got.Route != "pull" {
		t.Errorf("Route = %q, want pull", got.Route)
	}
	if !strings.Contains(got.Reason, "awaiting pull") {
		t.Errorf("Reason = %q, want it to name the awaiting-pull route", got.Reason)
	}
	if _, found, err := rt.Store.PendingForMasterMind("webshop"); err != nil || !found {
		t.Errorf("the entry must stay pending (found=%v err=%v)", found, err)
	}
}

// TestDeliverPendingDelivererNotMineStaysPending is the plan's required
// case for the deleted pane fallback: OutcomeNotMine does not fall
// through to typing the payload into a pane. It leaves the entry pending
// with the deliverer's own reason.
func TestDeliverPendingDelivererNotMineStaysPending(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Deliverers = map[string]MasterMindDeliverer{"claude": notMineDeliverer{}}
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	_, got := deliverOnce(t, rt, b)

	if got.Delivered {
		t.Error("OutcomeNotMine must not confirm the entry")
	}
	if got.Route != "pull" {
		t.Errorf("Route = %q, want pull", got.Route)
	}
	if got.Reason != "not mine to deliver" {
		t.Errorf("Reason = %q, want the deliverer's own reason", got.Reason)
	}
	if _, found, err := rt.Store.PendingForMasterMind("webshop"); err != nil || !found {
		t.Errorf("the entry must stay pending (found=%v err=%v)", found, err)
	}
}

// TestDeliverPendingChannelByMasterMindID keeps the surviving channel route:
// with a live claim for the binding's mastermind id, DeliverPending hands the
// entry to the channel -- it stays pending for the claim holder's own poll,
// which is what pushes and confirms it with route=channel.
func TestDeliverPendingChannelByMasterMindID(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{"pl_aaaaaaaabbbb": &Claim{MasterMind: "pl_aaaaaaaabbbb", PID: 1}}
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	_, got := deliverOnce(t, rt, b)

	if got.Route != "channel" {
		t.Errorf("Route = %q, want channel", got.Route)
	}
	if got.Delivered {
		t.Error("the channel's own drain confirms the entry; DeliverPending must not")
	}
	if _, found, err := rt.Store.PendingForMasterMind("webshop"); err != nil || !found {
		t.Errorf("the entry must stay pending for the channel reader (found=%v err=%v)", found, err)
	}
}

// TestDeliverPendingMarksDeliveredByDeliverer keeps the deliverer port working: a
// deliverer that reports OutcomeDelivered confirms the entry with
// route=deliverer:<kind>.
func TestDeliverPendingMarksDeliveredByDeliverer(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Deliverers = map[string]MasterMindDeliverer{"claude": deliveredDeliverer{}}
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	_, got := deliverOnce(t, rt, b)

	if !got.Delivered || got.Route != "deliverer:claude" {
		t.Fatalf("Delivery = %+v, want delivered by deliverer:claude", got)
	}
	if _, found, err := rt.Store.PendingForMasterMind("webshop"); err != nil || found {
		t.Errorf("a delivered entry must be confirmed (found=%v err=%v)", found, err)
	}
}

// deliveredDeliverer confirms whatever it is handed.
type deliveredDeliverer struct{}

func (deliveredDeliverer) Deliver(context.Context, store.Endpoint, string, string, time.Time) (Outcome, string, error) {
	return OutcomeDelivered, "handed to the session", nil
}

func (deliveredDeliverer) Confirm(context.Context, store.Endpoint, string, time.Time) (Outcome, string, error) {
	return OutcomeDelivered, "handed to the session", nil
}

func (deliveredDeliverer) ConfirmOnce(context.Context, store.Endpoint, string, time.Time) (Outcome, string, error) {
	return OutcomeDelivered, "handed to the session", nil
}

// TestDeliverPendingAdmitsThenConfirmsWithoutResending pins the two ticks of an
// admitted push: the first admits the payload (one Deliver plus the admit
// write), the entry is no longer claimable by a reader, and every later tick
// only reads the session back -- it never calls Deliver for that payload again.
func TestDeliverPendingAdmitsThenConfirmsWithoutResending(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "opencode")
	stub := &stubDeliverer{
		outcome:        OutcomeAdmitted,
		reason:         "posted; awaiting the session",
		confirmOutcome: OutcomeAdmitted,
		confirmReason:  "posted; awaiting the session",
	}
	rt.Deliverers = map[string]MasterMindDeliverer{"opencode": stub}

	// Tick 1: the deliverer admits the payload. The entry is admitted, not
	// confirmed, and the read-back that ran with it saw nothing yet.
	_, got := deliverOnce(t, rt, b)
	if got.Delivered {
		t.Fatalf("Delivery = %+v, want admitted, not delivered", got)
	}
	if stub.calls != 1 || stub.confirmCalls != 1 {
		t.Fatalf("deliverer calls = %d, confirm calls = %d; want 1 each", stub.calls, stub.confirmCalls)
	}

	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	admitted := false
	for _, e := range entries {
		if e.Direction != store.DirToMasterMind {
			continue
		}
		admitted = e.AdmittedAt != nil
		if e.Confirmed {
			t.Error("an entry that was only admitted must not be confirmed")
		}
	}
	if !admitted {
		t.Fatal("the entry was not marked admitted")
	}

	// A reader can neither claim nor print it any more.
	if _, found, err := pullPending(context.Background(), rt.Store, b.Name, "wait"); err != nil || found {
		t.Errorf("pullPending after an admit = found %v, err %v; want nothing claimable", found, err)
	}

	// Tick 2: a single read-back only -- no Deliver at all, and no second
	// full poll.
	_, got = deliverOnce(t, rt, b)
	if got.Delivered {
		t.Fatalf("Delivery = %+v, want still admitted", got)
	}
	if stub.calls != 1 {
		t.Fatalf("deliverer calls = %d, want 1: an admitted payload must never be pushed again", stub.calls)
	}
	if stub.confirmCalls != 1 {
		t.Fatalf("confirm calls = %d, want 1: a repeat tick reads back once, it does not re-run the poll", stub.confirmCalls)
	}
	if stub.onceCalls != 1 {
		t.Fatalf("confirmOnce calls = %d, want 1", stub.onceCalls)
	}

	// Once the session records it, the read-back confirms the entry.
	stub.onceOutcome = OutcomeDelivered
	stub.onceReason = ""
	_, got = deliverOnce(t, rt, b)
	if !got.Delivered || got.Route != "deliverer:opencode" {
		t.Fatalf("Delivery = %+v, want delivered by deliverer:opencode", got)
	}
	if stub.calls != 1 {
		t.Fatalf("deliverer calls = %d, want 1 after the confirming read-back", stub.calls)
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || pending {
		t.Errorf("the entry must be confirmed once the session took it: pending=%v err=%v", pending, err)
	}
}

// TestDeliverPendingWithNothingPendingIsNoop keeps the empty case: nothing
// queued reads as nothing to do, never an error.
func TestDeliverPendingWithNothingPendingIsNoop(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := store.Binding{
		Name: "webshop", CWD: "/repo/webshop", Round: 1, State: store.StateActive,
		MasterMind: store.Endpoint{Kind: "claude"}, MasterMindID: "pl_aaaaaaaabbbb",
	}

	_, got := deliverOnce(t, rt, b)

	if got.Delivered || !got.Empty {
		t.Errorf("Delivery = %+v, want Empty with nothing delivered", got)
	}
}

// stubDeliverer is the MasterMindDeliverer test double deliver_test.go controls
// directly, so DeliverPending's consult step can be exercised without a
// real opencode service. Its Deliver and Confirm answers are separate, so a
// test can have one tick admit and the next read back.
type stubDeliverer struct {
	outcome Outcome
	reason  string
	err     error
	calls   int

	confirmOutcome Outcome
	confirmReason  string
	confirmErr     error
	confirmCalls   int

	onceOutcome Outcome
	onceReason  string
	onceCalls   int
}

func (s *stubDeliverer) Deliver(_ context.Context, _ store.Endpoint, _, _ string, _ time.Time) (Outcome, string, error) {
	s.calls++
	return s.outcome, s.reason, s.err
}

func (s *stubDeliverer) Confirm(_ context.Context, _ store.Endpoint, _ string, _ time.Time) (Outcome, string, error) {
	s.confirmCalls++
	return s.confirmOutcome, s.confirmReason, s.confirmErr
}

func (s *stubDeliverer) ConfirmOnce(_ context.Context, _ store.Endpoint, _ string, _ time.Time) (Outcome, string, error) {
	s.onceCalls++
	return s.onceOutcome, s.onceReason, nil
}

// TestDeliverConsultsDelivererForMatchingKind proves DeliverPending routes a
// matching mastermind's payload through rt.Deliverers exactly once: the stub
// reports OutcomeDelivered, the entry is confirmed, and nothing else is
// consulted. There is no pane, so the assertion is the deliverer's own call
// count plus the confirmed entry.
func TestDeliverConsultsDelivererForMatchingKind(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "opencode")
	stub := &stubDeliverer{outcome: OutcomeDelivered, reason: "already present"}
	rt.Deliverers = map[string]MasterMindDeliverer{"opencode": stub}

	next, got := deliverOnce(t, rt, b)
	if !got.Delivered || got.Reason != "already present" {
		t.Fatalf("want delivered via the deliverer, got %+v", got)
	}
	if stub.calls != 1 {
		t.Fatalf("deliverer calls = %d, want 1", stub.calls)
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || pending {
		t.Errorf("an OutcomeDelivered outcome must confirm the log entry: pending=%v err=%v", pending, err)
	}
	if next.MasterMindScreen != "" {
		t.Errorf("a deliverer-routed delivery must leave the deleted fingerprint state empty, got %+v", next)
	}
}

// TestDeliverNilDeliverersBehavesAsToday proves a nil Deliverers map -- the
// zero value every existing test already runs with -- takes the pull route
// exactly as it did before this round: nothing is confirmed, and the payload
// waits for `relevo wait`.
func TestDeliverNilDeliverersBehavesAsToday(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Deliverers = nil
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "opencode")

	_, got := deliverOnce(t, rt, b)
	if got.Delivered {
		t.Fatalf("a nil Deliverers map must not deliver, got %+v", got)
	}
	if got.Route != "pull" {
		t.Errorf("Route = %q, want pull", got.Route)
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || !pending {
		t.Errorf("the payload must stay pending: pending=%v err=%v", pending, err)
	}
}

// TestDeliverYieldsToLiveClaim is the daemon-guard test the plan requires:
// a live claim on the mastermind's id must produce an all-false Delivery, with
// zero prompts, zero notifies, the entry still pending, and the
// binding's State left untouched. Commenting out the guard in DeliverPending
// makes this fail on the route (verified by hand per the plan's step
// 2 instructions).
func TestDeliverYieldsToLiveClaim(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	rt.Channels = fakeClaimStore{b.MasterMindID: &Claim{MasterMind: b.MasterMindID, PID: 1, SeenAt: rt.Now()}}
	wantState := b.State

	next, got := deliverOnce(t, rt, b)
	if got.Delivered || got.Route == "" || got.Empty {
		t.Fatalf("want the all-false channel handoff, got %+v", got)
	}
	if got.Route != "channel" {
		t.Errorf("Route = %q, want channel", got.Route)
	}
	if next.State != wantState {
		t.Errorf("State = %q, want unchanged %q", next.State, wantState)
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || !pending {
		t.Errorf("the entry must stay pending: pending=%v err=%v", pending, err)
	}
}

// TestDeliverIgnoresStaleClaim proves the guard is inert when the claim
// store answers "not live" (or knows nothing about the mastermind): delivery
// falls through to the pull route exactly as it did before the channel
// existed.
func TestDeliverIgnoresStaleClaim(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	rt.Channels = fakeClaimStore{} // no entry for this mastermind: Live returns nil, nil

	_, got := deliverOnce(t, rt, b)
	if got.Delivered {
		t.Fatalf("a stale claim must not block or confirm, got %+v", got)
	}
	if got.Route != "pull" {
		t.Fatalf("Route = %q, want the pull route", got.Route)
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || !pending {
		t.Errorf("the entry must stay pending for pull: pending=%v err=%v", pending, err)
	}
}
