package delivery

import (
	"context"
	"testing"

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
