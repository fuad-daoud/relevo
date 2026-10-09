package relevo

import (
	"bytes"
	"os"
	"path/filepath"
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

// TestDedupedRoundCapHaltKeepsNotifiedArtifactCapReason pins the cross-halt
// stamp: a reader round that closes over the artifact cap stamps the dedup key
// with the round the binding has just advanced to, so the NEXT tick's round-cap
// halt finds the key already equal and is deduped. A deduped halt queues
// nothing, so writing b.Halt on that path overwrites the artifact-cap reason
// with a reason no entry ever carried -- the MasterMind is left holding the
// round-cap text and the artifact-cap text is gone from the binding.
//
// The tick trace it pins, with RoundCap = 1:
//
//	N   : round 1 closes, b.Round++ makes it 2, the stamp is zeroed, and the
//	      artifact cap halts: HaltNotifiedRound = 2, Halt = the cap reason,
//	      one entry filed under round 1.
//	N+1 : 2 > RoundCap 1, so the round-cap halt halts. HaltNotifiedRound == 2
//	      already, so the guard is false: no entry, and -- the point of this
//	      test -- Halt still the cap reason.
//
// Mutation check: restore the unconditional `b.Halt =` write above the
// HaltNotifiedRound guard in haltBinding and the Halt assertion below fails,
// with the log still holding exactly one halt entry -- the reason the binding
// shows and the reason the mastermind was told have drifted apart.
func TestDedupedRoundCapHaltKeepsNotifiedArtifactCapReason(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)

	// The cap the round-cap halt will compare against after the close advances
	// the binding past it. Pinned explicitly so the test exercises the cap
	// halt rather than whatever default the store happens to carry.
	b.RoundCap = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir := rt.Store.ArtifactDir("reader-bind", 1, "reviewer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	big := bytes.Repeat([]byte("x"), 2<<20)
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o644); err != nil {
		t.Fatalf("write big.bin: %v", err)
	}
	oneMB := 1
	rt.Policy.ArtifactMaxMB = &oneMB

	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))
	exitReaderRunner(t, rt, b)

	// Tick N: the close advances the round and the artifact cap halts.
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	capReason := "artifacts over the cap: 2 > 1 MB; raise policy.artifact_max_mb to seal them"
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2: the close advances before the cap halt runs", got.Round)
	}
	if got.Halt != capReason {
		t.Fatalf("Halt = %q after the artifact-cap tick, want %q", got.Halt, capReason)
	}
	if got.HaltNotifiedRound != got.Round {
		t.Fatalf("HaltNotifiedRound = %d, want %d: the post-advance halt stamps the new round",
			got.HaltNotifiedRound, got.Round)
	}

	// Tick N+1: the round-cap halt finds the stamp already equal to the round,
	// so it is deduped.
	capped, err := reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("cap Reconcile: %v", err)
	}
	if capped.State != store.StateNeedsYou {
		t.Errorf("State = %q, want %q: the cap halt still asks for a human", capped.State, store.StateNeedsYou)
	}
	if capped.Halt != capReason {
		t.Errorf("Halt = %q after the deduped cap halt, want the notified reason %q: a halt that queued no entry must not overwrite the reason one did",
			capped.Halt, capReason)
	}
	if capped.HaltAt != got.HaltAt {
		t.Errorf("HaltAt = %v after the deduped cap halt, want unchanged %v", capped.HaltAt, got.HaltAt)
	}
	if halts := haltEntriesFor(t, rt, "reader-bind"); len(halts) != 1 {
		t.Errorf("halt entries = %d, want exactly 1: a deduped halt queues nothing: %+v", len(halts), halts)
	}
}
