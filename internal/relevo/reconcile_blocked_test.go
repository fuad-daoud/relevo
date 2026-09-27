package relevo

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestReconcileFlagsRoundTimeout(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	// Pin the budget explicitly: this exercises the timeout mechanism, not
	// whatever the store's default happens to be.
	b.RoundTimeoutMS = int((30 * time.Minute).Milliseconds())
	b.RoundStartedAt = time.Unix(1757000000, 0).UTC().Add(-31 * time.Minute)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you after the round timeout", got.State)
	}
	if want := "round 1 has run past 30m0s"; got.Halt != want {
		t.Errorf("Halt = %q, want %q", got.Halt, want)
	}
	if !got.HaltAt.Equal(rt.Now().UTC()) {
		t.Errorf("HaltAt = %v, want %v", got.HaltAt, rt.Now().UTC())
	}
	if got.HaltNotifiedRound != got.Round {
		t.Errorf("HaltNotifiedRound = %d, want %d: the halt is recorded once per round", got.HaltNotifiedRound, got.Round)
	}
	haltAt := got.HaltAt

	// A second tick against the same already-halted binding must not re-record
	// the halt: haltBinding's guard is per-transition, not per-tick.
	second, err := reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if second.HaltNotifiedRound != second.Round {
		t.Errorf("HaltNotifiedRound = %d after a second tick, want %d", second.HaltNotifiedRound, second.Round)
	}
	if !second.HaltAt.Equal(haltAt) {
		t.Errorf("HaltAt = %v after a second tick, want unchanged %v", second.HaltAt, haltAt)
	}

	// Even once the clock has moved on, a still-halted binding's HaltAt must
	// stay pinned to the halting tick, not drift to a later poll.
	third, err := reconcile(t, at(rt, time.Minute), second)
	if err != nil {
		t.Fatalf("third Reconcile: %v", err)
	}
	if !third.HaltAt.Equal(haltAt) {
		t.Errorf("HaltAt = %v after a later tick, want unchanged %v", third.HaltAt, haltAt)
	}
}

// TestReconcileStopsAtRoundCap checks the cap's decision point: a round past
// the cap halts instead of being relayed further.
func TestReconcileStopsAtRoundCap(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	b.Round = b.RoundCap + 1

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you at the cap", got.State)
	}
	if got.Halt == "" {
		t.Error("hitting the round cap must record its reason")
	}
	if got.HaltNotifiedRound != got.Round {
		t.Errorf("HaltNotifiedRound = %d, want %d", got.HaltNotifiedRound, got.Round)
	}
}

// TestReconcileTimeoutNotifiesOnceInEveryMasterMindState is the regression test
// for the halt storm: at a 2s poll, a halt that re-records is a notification
// every couple of seconds, forever, on a live desktop. Five ticks against a
// timed-out binding must leave exactly one halt recorded for the round.
func TestReconcileTimeoutNotifiesOnceInEveryMasterMindState(t *testing.T) {
	t.Parallel()

	rt, b := timedOutBinding(t)

	for i := 0; i < 5; i++ {
		var err error
		b, err = reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}

	if b.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you to survive every tick", b.State)
	}
	if b.HaltNotifiedRound != b.Round {
		t.Errorf("HaltNotifiedRound = %d, want %d", b.HaltNotifiedRound, b.Round)
	}
	if b.HaltAt.IsZero() {
		t.Error("HaltAt is zero, want the tick that halted")
	}
}

// TestReconcileTimeoutNotifiesAgainInALaterRound is the other half of
// the dedup: one halt per round that goes wrong, not one per binding.
func TestReconcileTimeoutNotifiesAgainInALaterRound(t *testing.T) {
	t.Parallel()

	rt, b := timedOutBinding(t)

	b, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// The next round is sent, runs long too, and must get its own halt.
	b.Round++
	b.HaltNotifiedRound = 0
	b.State = store.StateActive
	b.RoundTimeoutMS = int((30 * time.Minute).Milliseconds())
	b.RoundStartedAt = baseTime.Add(-31 * time.Minute)
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		TS: baseTime, Round: b.Round, Direction: store.DirToBuilder, Kind: store.KindPrompt,
		Path: rt.Store.PromptPath(b.Name, b.Round), Confirmed: true,
	}); err != nil {
		t.Fatalf("seed round %d plan: %v", b.Round, err)
	}

	second, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("second round Reconcile: %v", err)
	}
	if second.Round != 2 {
		t.Fatalf("round = %d, want 2", second.Round)
	}
	if second.HaltNotifiedRound != second.Round {
		t.Errorf("HaltNotifiedRound = %d, want %d: one halt per timed-out round", second.HaltNotifiedRound, second.Round)
	}
	if want := "round 2 has run past 30m0s"; second.Halt != want {
		t.Errorf("Halt = %q, want %q: the later round gets its own halt", second.Halt, want)
	}
}

// TestReconcileRoundCapNotifiesOnce guards the same dedup on the other halt.
func TestReconcileRoundCapNotifiesOnce(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	b.Round = b.RoundCap + 1

	for i := 0; i < 5; i++ {
		var err error
		b, err = reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}

	if b.HaltNotifiedRound != b.Round {
		t.Errorf("HaltNotifiedRound = %d over 5 ticks at the cap, want %d", b.HaltNotifiedRound, b.Round)
	}
	if b.Halt == "" {
		t.Error("Halt is empty over 5 ticks at the cap, want the reason recorded")
	}
}
