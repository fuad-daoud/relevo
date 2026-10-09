package delivery

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// admit writes the admit marker a push route leaves, so a read-back has
// something to settle.
func admit(t *testing.T, rt Deps, name string) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, idx, found, err := tx.PendingForMasterMind(name)
		if err != nil {
			return err
		}
		if !found {
			t.Fatalf("%s: nothing pending to admit", name)
		}
		return tx.AdmitIndex(name, idx)
	}); err != nil {
		t.Fatalf("admit %s: %v", name, err)
	}
}

// confirmAdmittedOnce runs ConfirmAdmitted under the state lock, the way the
// daemon's done-binding tick does.
func confirmAdmittedOnce(t *testing.T, rt Deps, b store.Binding) (store.Binding, Delivery) {
	t.Helper()
	var (
		next store.Binding
		got  Delivery
	)
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		next, got, err = ConfirmAdmitted(context.Background(), rt, tx, b)
		return err
	}); err != nil {
		t.Fatalf("ConfirmAdmitted: %v", err)
	}
	return next, got
}

// TestConfirmAdmittedSettlesAnAdmittedDoneEntry pins the done-binding
// read-back: an entry a push route admitted before the binding was marked done
// is read back once and confirmed when the session took it, and Deliver is
// never called -- a done binding must not get a new push.
func TestConfirmAdmittedSettlesAnAdmittedDoneEntry(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "opencode")
	admit(t, rt, b.Name)
	b.State = store.StateDone

	stub := &stubDeliverer{onceOutcome: OutcomeAdmitted, onceReason: "posted; awaiting the session"}
	rt.Deliverers = map[string]MasterMindDeliverer{"opencode": stub}

	// The read-back does not see the payload yet: it stays admitted.
	_, got := confirmAdmittedOnce(t, rt, b)
	if got.Delivered {
		t.Fatalf("Delivery = %+v, want still admitted", got)
	}
	if stub.calls != 0 {
		t.Fatalf("Deliver calls = %d, want 0: an admitted payload is never pushed again", stub.calls)
	}
	if stub.onceCalls != 1 {
		t.Fatalf("ConfirmOnce calls = %d, want 1", stub.onceCalls)
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || !pending {
		t.Errorf("the entry must stay pending until the session takes it: pending=%v err=%v", pending, err)
	}

	// Once the session records it, the next read-back confirms it.
	stub.onceOutcome = OutcomeDelivered
	stub.onceReason = ""
	_, got = confirmAdmittedOnce(t, rt, b)
	if !got.Delivered || got.Route != "deliverer:opencode" {
		t.Fatalf("Delivery = %+v, want delivered by deliverer:opencode", got)
	}
	if stub.calls != 0 {
		t.Fatalf("Deliver calls = %d, want 0 after the confirming read-back", stub.calls)
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || pending {
		t.Errorf("the admitted entry must be confirmed: pending=%v err=%v", pending, err)
	}
}

// TestConfirmAdmittedLeavesANonAdmittedEntryAlone keeps the guard: an entry no
// push route admitted is not read back and not pushed for a done binding -- it
// waits for the background wait, exactly as it did before this call existed.
func TestConfirmAdmittedLeavesANonAdmittedEntryAlone(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "opencode")
	b.State = store.StateDone
	stub := &stubDeliverer{}
	rt.Deliverers = map[string]MasterMindDeliverer{"opencode": stub}

	_, got := confirmAdmittedOnce(t, rt, b)
	if got.Delivered {
		t.Fatalf("Delivery = %+v, want nothing delivered", got)
	}
	if got.Route != "pull" {
		t.Errorf("Route = %q, want pull", got.Route)
	}
	if stub.calls != 0 || stub.confirmCalls != 0 || stub.onceCalls != 0 {
		t.Fatalf("deliverer calls = deliver %d, confirm %d, confirmOnce %d; want 0, 0, 0",
			stub.calls, stub.confirmCalls, stub.onceCalls)
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || !pending {
		t.Errorf("the entry must stay pending: pending=%v err=%v", pending, err)
	}
}

// TestConfirmAdmittedWithNothingPendingIsNoop keeps the empty case total.
func TestConfirmAdmittedWithNothingPendingIsNoop(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := store.Binding{Name: "webshop", CWD: "/repo/webshop", Round: 1, State: store.StateDone}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save: %v", err)
	}

	_, got := confirmAdmittedOnce(t, rt, b)
	if got.Delivered || !got.Empty {
		t.Errorf("Delivery = %+v, want Empty with nothing delivered", got)
	}
}

// TestConfirmAdmittedExpiredAdmitBecomesWaitClaimableWithoutPushing pins the
// done-binding half of the bound. An admit that outlived its route's horizon is
// not a settlement, so it is cleared -- which is what puts the entry back into
// the claimable scans, so the background wait can take it. This path must NEVER
// push: a binding whose state already changed does not get a new payload, and
// its Deliver and Confirm counts are the proof. The one read-back it still takes
// is the ConfirmOnce that looks for a payload the session did already receive;
// here the session has not taken it, so the entry stays for the wait.
func TestConfirmAdmittedExpiredAdmitBecomesWaitClaimableWithoutPushing(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "opencode")
	admit(t, rt, b.Name)
	b.State = store.StateDone
	rt.Now = agedClock(time.Hour) // an hour of wall clock: past the stub's minute

	stub := &stubDeliverer{horizon: time.Minute, onceOutcome: OutcomeAdmitted, onceReason: "posted; awaiting the session"}
	rt.Deliverers = map[string]MasterMindDeliverer{"opencode": stub}

	_, got := confirmAdmittedOnce(t, rt, b)
	if got.Delivered {
		t.Fatalf("Delivery = %+v, want nothing delivered", got)
	}
	if got.Route != "pull" {
		t.Errorf("Route = %q, want pull: the background wait is the route that takes it", got.Route)
	}
	if stub.calls != 0 || stub.confirmCalls != 0 {
		t.Fatalf("deliverer calls = deliver %d, confirm %d; want 0, 0: this path never pushes",
			stub.calls, stub.confirmCalls)
	}
	if stub.onceCalls != 1 {
		t.Fatalf("ConfirmOnce calls = %d, want 1: an expired admit is read back once before the wait takes it", stub.onceCalls)
	}

	if admitOf(t, rt, b.Name) != nil {
		t.Error("an expired admit must be cleared, or the entry stays invisible to every reader")
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, _, found, err := tx.ClaimableForMasterMind(b.Name)
		if err != nil {
			return err
		}
		if !found {
			t.Error("the cleared entry must be claimable for the background wait")
		}
		return nil
	}); err != nil {
		t.Fatalf("ClaimableForMasterMind: %v", err)
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || !pending {
		t.Errorf("the entry must stay pending for the wait: pending=%v err=%v", pending, err)
	}
}

// TestConfirmAdmittedExpiredAdmitConfirmsAnArrivedPayloadWithoutPushing pins
// the other half of the expired-admit branch: an admit past its horizon says
// the read-back has not settled the entry yet, not that the payload was never
// received. So the cleared admit is followed by the same one read-back, and a
// payload the session already took is confirmed instead of handed back as
// claimable -- which is what keeps the background wait from showing the same
// payload twice, once pushed and once pulled. The read-back confirms, it does
// not send: Deliver is never called.
func TestConfirmAdmittedExpiredAdmitConfirmsAnArrivedPayloadWithoutPushing(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "opencode")
	admit(t, rt, b.Name)
	b.State = store.StateDone
	rt.Now = agedClock(time.Hour) // an hour of wall clock: past the stub's minute

	stub := &stubDeliverer{horizon: time.Minute, onceOutcome: OutcomeDelivered, onceReason: "the session took it"}
	rt.Deliverers = map[string]MasterMindDeliverer{"opencode": stub}

	_, got := confirmAdmittedOnce(t, rt, b)
	if !got.Delivered || got.Route != "deliverer:opencode" {
		t.Fatalf("Delivery = %+v, want delivered by deliverer:opencode", got)
	}
	if stub.calls != 0 || stub.confirmCalls != 0 {
		t.Fatalf("deliverer calls = deliver %d, confirm %d; want 0, 0: the read-back confirms, it does not send",
			stub.calls, stub.confirmCalls)
	}
	if stub.onceCalls != 1 {
		t.Fatalf("ConfirmOnce calls = %d, want 1: one read-back settles the expired admit", stub.onceCalls)
	}

	if admitOf(t, rt, b.Name) != nil {
		t.Error("an expired admit must be cleared even when the payload was confirmed")
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || pending {
		t.Errorf("a payload the session already took must be confirmed: pending=%v err=%v", pending, err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, _, found, err := tx.ClaimableForMasterMind(b.Name)
		if err != nil {
			return err
		}
		if found {
			t.Error("a confirmed entry must not stay claimable: the wait would show it a second time")
		}
		return nil
	}); err != nil {
		t.Fatalf("ClaimableForMasterMind: %v", err)
	}
}

// TestConfirmAdmittedFreshAdmitIsStillReadBack keeps the done-binding
// read-back honest under the new bound: an admit inside its horizon is settled
// by the read-back as before, and never by a clear.
func TestConfirmAdmittedFreshAdmitIsStillReadBack(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	b := seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "opencode")
	admit(t, rt, b.Name)
	b.State = store.StateDone
	rt.Now = time.Now // the admit stamp is seconds old

	stub := &stubDeliverer{horizon: time.Hour, onceOutcome: OutcomeAdmitted, onceReason: "posted; awaiting the session"}
	rt.Deliverers = map[string]MasterMindDeliverer{"opencode": stub}

	_, got := confirmAdmittedOnce(t, rt, b)
	if got.Delivered {
		t.Fatalf("Delivery = %+v, want still admitted", got)
	}
	if stub.onceCalls != 1 {
		t.Fatalf("ConfirmOnce calls = %d, want 1: a fresh admit is still read back", stub.onceCalls)
	}
	if admitOf(t, rt, b.Name) == nil {
		t.Error("a fresh admit must keep its stamp")
	}
}
