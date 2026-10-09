package delivery

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// fakeClaimStore is the ClaimStore the plan asks for: a claim present for a
// mastermind id means live, absent means not.
//
// It is a mutex-guarded struct rather than a bare map because a test that runs
// a holder has two goroutines on it: the holder's own refresh loop writes the
// claim through Write while awaitConfirm reads it back through Live. Production
// KVClaims is already safe; without the lock this fake is the only unsynchronized
// map under -race, and the race report points at refreshPushClaim against
// claimHeld.
type fakeClaimStore struct {
	mu sync.Mutex
	m  map[string]*Claim
}

// newFakeClaimStore returns a fake store holding claims, keyed by each claim's
// MasterMind. It replaces the map literal the fake used to be, so every site
// builds one through the constructor.
func newFakeClaimStore(claims ...*Claim) *fakeClaimStore {
	f := &fakeClaimStore{m: map[string]*Claim{}}
	for _, c := range claims {
		f.m[c.MasterMind] = c
	}
	return f
}

func (f *fakeClaimStore) Live(mastermind string, now time.Time) (*Claim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[mastermind], nil
}

func (f *fakeClaimStore) Write(c Claim, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]*Claim{}
	}
	f.m[c.MasterMind] = &c
	return nil
}

func (f *fakeClaimStore) Remove(mastermind string, pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, mastermind)
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

// TestDeliverPendingPushByMasterMindID keeps the surviving push route:
// with a live claim for the binding's mastermind id, DeliverPending hands the
// entry to the holder -- it stays pending for the holder's own drain, which is
// what pushes and confirms it with route=push.
func TestDeliverPendingPushByMasterMindID(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	rt.Channels = newFakeClaimStore(&Claim{MasterMind: "pl_aaaaaaaabbbb", PID: 1})
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	_, got := deliverOnce(t, rt, b)

	if got.Route != "push" {
		t.Errorf("Route = %q, want push", got.Route)
	}
	if got.Delivered {
		t.Error("the holder's own drain confirms the entry; DeliverPending must not")
	}
	if _, found, err := rt.Store.PendingForMasterMind("webshop"); err != nil || !found {
		t.Errorf("the entry must stay pending for the push holder (found=%v err=%v)", found, err)
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
	if _, found, err := Pull(context.Background(), rt.Store, b.Name, "wait"); err != nil || found {
		t.Errorf("Pull after an admit = found %v, err %v; want nothing claimable", found, err)
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
	// texts records the payload of each Deliver, so a test can tell which
	// queue entry a push carried.
	texts []string

	confirmOutcome Outcome
	confirmReason  string
	confirmErr     error
	confirmCalls   int

	onceOutcome Outcome
	onceReason  string
	onceCalls   int

	// horizon bounds how long an admit this stub leaves stands. Zero means
	// unbounded, which is what a stub that never sets it means.
	horizon time.Duration
}

func (s *stubDeliverer) Deliver(_ context.Context, _ store.Endpoint, payload, _ string, _ time.Time) (Outcome, string, error) {
	s.calls++
	s.texts = append(s.texts, payload)
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

func (s *stubDeliverer) AdmitHorizon() time.Duration { return s.horizon }

func (notMineDeliverer) AdmitHorizon() time.Duration { return 0 }

func (deliveredDeliverer) AdmitHorizon() time.Duration { return 0 }

// agedClock reads as `after` past the wall clock. An admit stamp is written by
// the store from the real clock, so ageing an admit means moving Deps.Now
// forward rather than rewriting the stamp under the lock.
func agedClock(after time.Duration) func() time.Time {
	return func() time.Time { return time.Now().Add(after) }
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
	rt.Channels = newFakeClaimStore(&Claim{MasterMind: b.MasterMindID, PID: 1, SeenAt: rt.Now()})
	wantState := b.State

	next, got := deliverOnce(t, rt, b)
	if got.Delivered || got.Route == "" || got.Empty {
		t.Fatalf("want the all-false push handoff, got %+v", got)
	}
	if got.Route != "push" {
		t.Errorf("Route = %q, want push", got.Route)
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
// falls through to the pull route exactly as it did before the push claim
// existed.
func TestDeliverIgnoresStaleClaim(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	rt.Channels = newFakeClaimStore() // no entry for this mastermind: Live returns nil, nil

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

// admitOf reads the admit stamp off the named binding's oldest unconfirmed
// mastermind entry, so a test can tell "still admitted" from "the admit is
// gone".
func admitOf(t *testing.T, rt Deps, name string) *time.Time {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("read log %s: %v", name, err)
	}
	for _, entry := range entries {
		if entry.Direction == store.DirToMasterMind && !entry.Confirmed {
			return entry.AdmittedAt
		}
	}
	return nil
}

// TestDeliverPendingExpiredAdmitFallsBackAndDelivers is the core regression:
// an entry a push route admitted, whose payload never showed up in a
// read-back, stops being an admit once the route's horizon passes. The admit is
// cleared and the same tick pushes it again -- Deliver reads the session back
// before it sends, so a payload that did arrive is confirmed instead of posted
// twice.
func TestDeliverPendingExpiredAdmitFallsBackAndDelivers(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "opencode")
	admit(t, rt, b.Name)
	rt.Now = agedClock(time.Hour) // an hour of wall clock: past the stub's minute

	stub := &stubDeliverer{horizon: time.Minute, outcome: OutcomeDelivered, reason: "handed to the session"}
	rt.Deliverers = map[string]MasterMindDeliverer{"opencode": stub}

	_, got := deliverOnce(t, rt, b)
	if !got.Delivered {
		t.Fatalf("Delivery = %+v, want the expired admit handed back to the push path", got)
	}
	if stub.calls != 1 {
		t.Fatalf("Deliver calls = %d, want 1: an expired admit must be pushed again", stub.calls)
	}
	if stub.onceCalls != 0 {
		t.Fatalf("ConfirmOnce calls = %d, want 0: the admit was cleared, not read back", stub.onceCalls)
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || pending {
		t.Errorf("a delivered entry must be confirmed: pending=%v err=%v", pending, err)
	}
}

// TestDeliverPendingFreshAdmitStaysAdmitted pins the other side of the bound: an
// admit inside its horizon is still the route's answer. The tick reads the
// session back once, never pushes, and leaves the stamp alone -- otherwise the
// bound would double-post every payload that merely has not been read yet.
func TestDeliverPendingFreshAdmitStaysAdmitted(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "opencode")
	admit(t, rt, b.Name)
	rt.Now = time.Now // the admit stamp is seconds old

	stub := &stubDeliverer{horizon: time.Hour, onceOutcome: OutcomeAdmitted, onceReason: "posted; awaiting the session"}
	rt.Deliverers = map[string]MasterMindDeliverer{"opencode": stub}

	_, got := deliverOnce(t, rt, b)
	if got.Delivered {
		t.Fatalf("Delivery = %+v, want the entry still admitted", got)
	}
	if stub.calls != 0 {
		t.Fatalf("Deliver calls = %d, want 0: a fresh admit must never be pushed again", stub.calls)
	}
	if stub.onceCalls != 1 {
		t.Fatalf("ConfirmOnce calls = %d, want 1: a fresh admit is still read back", stub.onceCalls)
	}
	if admitOf(t, rt, b.Name) == nil {
		t.Error("a fresh admit must keep its stamp: clearing it inside the horizon would re-post the payload")
	}
}

// TestDeliverPendingExpiredAdmitUnblocksTheQueue proves the bound also clears a
// stuck head. Two entries are queued and the head is admitted; while that admit
// stands, PendingForMasterMind keeps returning the head and no reader may claim
// it, so the second entry is unreachable. Once the head's admit expires the head
// is delivered and the second entry is pushed on the following tick.
func TestDeliverPendingExpiredAdmitUnblocksTheQueue(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "opencode")
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return Queue(context.Background(), rt, tx, b.Name, store.LogEntry{
			Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Payload: "round 2 report", Path: "/tmp/report2.md",
		})
	}); err != nil {
		t.Fatalf("queue round 2: %v", err)
	}
	admit(t, rt, b.Name) // the head only: round 1
	rt.Now = agedClock(time.Hour)

	stub := &stubDeliverer{horizon: time.Minute, outcome: OutcomeDelivered, reason: "handed to the session"}
	rt.Deliverers = map[string]MasterMindDeliverer{"opencode": stub}

	// Tick 1: the head's admit expires, so the head itself is pushed again.
	if _, got := deliverOnce(t, rt, b); !got.Delivered {
		t.Fatalf("Delivery = %+v, want the stuck head delivered once its admit expired", got)
	}

	// Tick 2: the second entry, which the stuck head had been shadowing, is now
	// the head of the queue and is pushed.
	if _, got := deliverOnce(t, rt, b); !got.Delivered || got.Round != 2 {
		t.Fatalf("Delivery = %+v, want round 2 delivered next", got)
	}

	if stub.calls != 2 {
		t.Fatalf("Deliver calls = %d, want 2: one per queue entry", stub.calls)
	}
	for _, round := range []string{"round 1", "round 2"} {
		found := false
		for _, text := range stub.texts {
			if strings.Contains(text, round) {
				found = true
			}
		}
		if !found {
			t.Errorf("no Deliver carried the %s payload; pushes = %v", round, stub.texts)
		}
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || pending {
		t.Errorf("both entries must be confirmed: pending=%v err=%v", pending, err)
	}
}
