package delivery

import (
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// ackRuntime is a Deps whose claim store answers "live" for the test
// mastermind, which is the state a waiting holder leaves behind.
func ackRuntime(t *testing.T) Deps {
	t.Helper()
	rt := routeRuntime(t)
	rt.Channels = fakeClaimStore{
		testClaimMasterMind: &Claim{MasterMind: testClaimMasterMind, PID: 1, SeenAt: baseTime},
	}
	return rt
}

// ackEntry reads name's entry at idx, failing the test if it cannot.
func ackEntry(t *testing.T, rt Deps, name string, idx int) store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil || idx >= len(entries) {
		t.Fatalf("ReadLog(%s): %v (%d entries)", name, err, len(entries))
	}
	return entries[idx]
}

// TestAckPushConfirmsAdmittedEntry is the happy path: an admitted, unconfirmed
// entry under a live claim is confirmed with route push.
func TestAckPushConfirmsAdmittedEntry(t *testing.T) {
	t.Parallel()

	rt := ackRuntime(t)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	admitEntryAt(t, rt, "webshop")

	if _, err := AckPush(rt, testClaimMasterMind, "webshop", 1); err != nil {
		t.Fatalf("AckPush: %v", err)
	}
	e := ackEntry(t, rt, "webshop", 0)
	if !e.Confirmed || e.Route != "push" {
		t.Errorf("entry = confirmed:%v route:%q, want confirmed with route push", e.Confirmed, e.Route)
	}
}

// TestAckPushIdempotentOnPushedEntry: a second ack of an entry this route
// already confirmed succeeds rather than reporting a conflict.
func TestAckPushIdempotentOnPushedEntry(t *testing.T) {
	t.Parallel()

	rt := ackRuntime(t)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	admitEntryAt(t, rt, "webshop")
	if _, err := AckPush(rt, testClaimMasterMind, "webshop", 1); err != nil {
		t.Fatalf("first AckPush: %v", err)
	}
	if _, err := AckPush(rt, testClaimMasterMind, "webshop", 1); err != nil {
		t.Fatalf("second AckPush of a pushed entry: %v, want nil", err)
	}
}

// TestAckPushReportsWhichPathAnswered pins the result the CLI prints: the ack
// that wrote the confirmation says AlreadyConfirmed false, and the idempotent
// retry that found its own earlier work says true. The route and the echoed
// binding/seq are the same either way, so one field is the whole difference.
func TestAckPushReportsWhichPathAnswered(t *testing.T) {
	t.Parallel()

	rt := ackRuntime(t)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	admitEntryAt(t, rt, "webshop")

	fresh, err := AckPush(rt, testClaimMasterMind, "webshop", 1)
	if err != nil {
		t.Fatalf("AckPush: %v", err)
	}
	if fresh.AlreadyConfirmed {
		t.Error("AlreadyConfirmed = true on the ack that wrote the confirmation, want false")
	}
	if fresh.Binding != "webshop" || fresh.Seq != 1 || fresh.Route != "push" {
		t.Errorf("result = %+v, want webshop seq 1 route push", fresh)
	}

	retry, err := AckPush(rt, testClaimMasterMind, "webshop", 1)
	if err != nil {
		t.Fatalf("second AckPush of a pushed entry: %v, want nil", err)
	}
	if !retry.AlreadyConfirmed {
		t.Error("AlreadyConfirmed = false on the idempotent retry, want true")
	}
}

// TestAckPushRefusalClaimsNoConfirmation: a refusal settles nothing, so the
// result it hands back must never read as already_confirmed. The identity
// fields are still echoed, but the caller renders the error instead, so only
// the confirmation flag carries a rule.
func TestAckPushRefusalClaimsNoConfirmation(t *testing.T) {
	t.Parallel()

	rt := ackRuntime(t)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	admitEntryAt(t, rt, "webshop")

	res, err := AckPush(rt, "pl_bbbbbbbbbbbb", "webshop", 1)
	if !errors.Is(err, ErrAckForeignBinding) {
		t.Fatalf("AckPush for another mastermind = %v, want ErrAckForeignBinding", err)
	}
	if res.AlreadyConfirmed {
		t.Error("AlreadyConfirmed = true on a refusal, want false: nothing was settled")
	}
	// The entry belongs to another mastermind, so this ack left it untouched.
	e := ackEntry(t, rt, "webshop", 0)
	if e.Confirmed {
		t.Error("a refused ack confirmed the entry anyway")
	}
}

// TestAckPushIdempotentWithoutLiveClaim pins the seeded decision that a retry
// succeeds on an already-pushed entry without a live claim: the entry is
// settled, so the holder's exit in between is not the acker's problem.
func TestAckPushIdempotentWithoutLiveClaim(t *testing.T) {
	t.Parallel()

	rt := ackRuntime(t)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	admitEntryAt(t, rt, "webshop")
	if _, err := AckPush(rt, testClaimMasterMind, "webshop", 1); err != nil {
		t.Fatalf("first AckPush: %v", err)
	}

	// The holder is gone: the claim is no longer live.
	rt.Channels = fakeClaimStore{}
	if _, err := AckPush(rt, testClaimMasterMind, "webshop", 1); err != nil {
		t.Fatalf("retry without a live claim: %v, want nil", err)
	}
	e := ackEntry(t, rt, "webshop", 0)
	if !e.Confirmed || e.Route != "push" {
		t.Errorf("entry = confirmed:%v route:%q, want it still confirmed with route push", e.Confirmed, e.Route)
	}
}

// TestAckPushRefusesForeignBinding: a binding another mastermind owns is
// refused, so one MasterMind cannot confirm another's entry.
func TestAckPushRefusesForeignBinding(t *testing.T) {
	t.Parallel()

	rt := ackRuntime(t)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	admitEntryAt(t, rt, "webshop")

	_, err := AckPush(rt, "pl_bbbbbbbbbbbb", "webshop", 1)
	if !errors.Is(err, ErrAckForeignBinding) {
		t.Fatalf("AckPush for another mastermind = %v, want ErrAckForeignBinding", err)
	}
	if e := ackEntry(t, rt, "webshop", 0); e.Confirmed {
		t.Error("a foreign binding's entry must not be confirmed")
	}
}

// TestAckPushRefusesUnadmittedEntry: no holder wrote the line, so an ack has
// nothing to confirm and the entry stays pending.
func TestAckPushRefusesUnadmittedEntry(t *testing.T) {
	t.Parallel()

	rt := ackRuntime(t)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")

	_, err := AckPush(rt, testClaimMasterMind, "webshop", 1)
	if !errors.Is(err, ErrAckNotAdmitted) {
		t.Fatalf("AckPush on an unadmitted entry = %v, want ErrAckNotAdmitted", err)
	}
	if e := ackEntry(t, rt, "webshop", 0); e.Confirmed {
		t.Error("an unadmitted entry must not be confirmed")
	}
}

// TestAckPushRefusesWithoutLiveClaim: an admitted entry with no live claim is
// refused, and its admit is left for the orphan clear rather than consumed.
func TestAckPushRefusesWithoutLiveClaim(t *testing.T) {
	t.Parallel()

	rt := ackRuntime(t)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	admitEntryAt(t, rt, "webshop")
	rt.Channels = fakeClaimStore{}

	_, err := AckPush(rt, testClaimMasterMind, "webshop", 1)
	if !errors.Is(err, ErrAckNoClaim) {
		t.Fatalf("AckPush with no live claim = %v, want ErrAckNoClaim", err)
	}
	e := ackEntry(t, rt, "webshop", 0)
	if e.Confirmed {
		t.Error("an entry with no live claim must not be confirmed")
	}
	if e.AdmittedAt == nil {
		t.Error("a refused ack must leave the admit for the orphan clear")
	}
	if n := claimableCount(t, rt, "webshop"); n != 0 {
		t.Errorf("claimable = %d, want 0 while the admit is still there", n)
	}
}

// TestAckPushUnknownSeq: a seq no to-mastermind entry carries is refused.
func TestAckPushUnknownSeq(t *testing.T) {
	t.Parallel()

	rt := ackRuntime(t)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	admitEntryAt(t, rt, "webshop")

	_, err := AckPush(rt, testClaimMasterMind, "webshop", 99)
	if !errors.Is(err, ErrAckUnknownSeq) {
		t.Fatalf("AckPush for an unknown seq = %v, want ErrAckUnknownSeq", err)
	}
	if e := ackEntry(t, rt, "webshop", 0); e.Confirmed {
		t.Error("an unknown seq must confirm nothing")
	}
}

// TestAckPushRefusesEntryConfirmedByAnotherRoute: an entry a reader already
// settled is not the push route's to confirm.
func TestAckPushRefusesEntryConfirmedByAnotherRoute(t *testing.T) {
	t.Parallel()

	rt := ackRuntime(t)
	seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	admitEntryAt(t, rt, "webshop")
	if err := rt.Store.ConfirmIndex("webshop", 0, "wait"); err != nil {
		t.Fatalf("ConfirmIndex: %v", err)
	}

	_, err := AckPush(rt, testClaimMasterMind, "webshop", 1)
	if !errors.Is(err, ErrAckAlreadyConfirmed) {
		t.Fatalf("AckPush on a wait-confirmed entry = %v, want ErrAckAlreadyConfirmed", err)
	}
	if e := ackEntry(t, rt, "webshop", 0); e.Route != "wait" {
		t.Errorf("route = %q, want the reader's wait route left alone", e.Route)
	}
}

// TestAckPushUnknownBinding: a binding that does not exist wraps
// store.ErrNotFound, which the CLI already maps to binding_not_found.
func TestAckPushUnknownBinding(t *testing.T) {
	t.Parallel()

	rt := ackRuntime(t)

	_, err := AckPush(rt, testClaimMasterMind, "nosuch", 1)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AckPush for a missing binding = %v, want it to wrap store.ErrNotFound", err)
	}
}

// TestAckPushRefusesBuilderSeq: a seq that only a to-builder entry carries is not
// ackable, because the mod never received that direction.
func TestAckPushRefusesBuilderSeq(t *testing.T) {
	t.Parallel()

	rt := ackRuntime(t)
	b := seedPending(t, rt, "webshop", testClaimMasterMind, "claude")
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	_, err := AckPush(rt, testClaimMasterMind, "webshop", 2)
	if !errors.Is(err, ErrAckUnknownSeq) {
		t.Fatalf("AckPush for a to-builder seq = %v, want ErrAckUnknownSeq", err)
	}
}
