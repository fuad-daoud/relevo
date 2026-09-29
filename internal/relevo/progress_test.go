package relevo

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/store"
)

// progressStart is the round start the pure progress tests sample from, and
// progressRt is the runtime whose zero Policy is the defaults (30s sampling,
// 15m stall, 20m explore).
var progressStart = time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)

func progressRt() Runtime { return Runtime{Policy: policy.Policy{}} }

// TestProgressTreeChangeResetsEverything pins the tree signal's precedence: a
// tree that moves clears both the stall and the exploring label, even when the
// output has been still the whole time. The sample before the change is the
// negative half: with the tree quiet for 25m the stall must already be set.
func TestProgressTreeChangeResetsEverything(t *testing.T) {
	t.Parallel()

	rt := progressRt()
	start := progressStart
	// The tree has already been quiet for 15m when the round's first sample is
	// taken, so by +10m it has been quiet for 25m.
	b := store.Binding{Name: "webshop", Round: 1, RoundStartedAt: start.Add(-15 * time.Minute)}

	for now := start; !now.After(start.Add(10 * time.Minute)); now = now.Add(30 * time.Second) {
		b = progressStep(rt, b, now, signals{tree: "t0"})
	}
	if b.StalledSince.IsZero() {
		t.Fatalf("StalledSince is zero after 25m of a still tree; want it set before the change")
	}

	// The tree moves: both labels go back to zero.
	b = progressStep(rt, b, start.Add(10*time.Minute+30*time.Second), signals{tree: "t1"})
	if !b.StalledSince.IsZero() {
		t.Errorf("StalledSince = %s, want zero once the tree moves", b.StalledSince)
	}
	if !b.ExploringSince.IsZero() {
		t.Errorf("ExploringSince = %s, want zero once the tree moves", b.ExploringSince)
	}
}

// TestProgressScreenOnlyIsExploringAfterThreshold pins the exploring clock: a
// screen that keeps changing while the tree does not is exploring once the tree
// has been still for explore_after_ms, and not one interval before.
//
// Mutation check: swapping rt.Policy.ExploreAfter() for StallAfter() makes the
// t+19m case fail, since 19m is past the 15m stall threshold.
// TestProgressNothingIsStalled pins the plain case: nothing changes at all, so
// at stall_after_ms the stamp lands on the last change (the round's start) and
// the exploring label stays zero -- a stall outranks exploring.
func TestProgressNothingIsStalled(t *testing.T) {
	t.Parallel()

	rt := progressRt()
	start := progressStart

	b := store.Binding{Name: "webshop", Round: 1, RoundStartedAt: start}
	b = progressStep(rt, b, start, signals{tree: "t0"})
	b = progressStep(rt, b, start.Add(15*time.Minute), signals{tree: "t0"})

	if !b.StalledSince.Equal(start) {
		t.Errorf("StalledSince = %s, want the round's start %s", b.StalledSince, start)
	}
	if !b.ExploringSince.IsZero() {
		t.Errorf("ExploringSince = %s, want zero: a stall wins", b.ExploringSince)
	}
}

// TestProgressBlockedNeverStalls pins the dialog case: a builder blocked on a
// human is waiting, not hung, so the stall clock never fires for it.
// TestProgressHeadlessStreamMtimeStillCounts pins #252's signal under the new
// clock: a headless stream whose mtime keeps advancing is not stalled, and a
// tree that has not moved for explore_after_ms while the stream moves is
// exploring.
func TestProgressHeadlessStreamMtimeStillCounts(t *testing.T) {
	t.Parallel()

	rt := progressRt()
	start := progressStart

	b := store.Binding{
		Name: "webshop", Round: 1, RoundStartedAt: start,
		Builder: store.Endpoint{Mode: store.ModeHeadless},
	}
	for now := start; !now.After(start.Add(20 * time.Minute)); now = now.Add(30 * time.Second) {
		b = progressStep(rt, b, now, signals{tree: "t0", outputAt: now})
	}

	if !b.StalledSince.IsZero() {
		t.Errorf("StalledSince = %s, want zero: the stream is still moving", b.StalledSince)
	}
	if !b.ExploringSince.Equal(start) {
		t.Errorf("ExploringSince = %s, want the tree's last change %s", b.ExploringSince, start)
	}
}

// TestProgressNoSignalsNoLabel pins the empty case: no git, no pane and no
// stream is no evidence at all, so no label is ever set (#135 §6).
func TestProgressNoSignalsNoLabel(t *testing.T) {
	t.Parallel()

	rt := progressRt()
	start := progressStart

	b := store.Binding{Name: "webshop", Round: 1, RoundStartedAt: start}
	for now := start; !now.After(start.Add(time.Hour)); now = now.Add(30 * time.Second) {
		b = progressStep(rt, b, now, signals{})
	}

	if !b.StalledSince.IsZero() {
		t.Errorf("StalledSince = %s, want zero with no readable signal", b.StalledSince)
	}
	if !b.ExploringSince.IsZero() {
		t.Errorf("ExploringSince = %s, want zero with no readable signal", b.ExploringSince)
	}
}

// TestProgressSampleCadence pins the 30s throttle: a second call inside the
// interval returns the binding unchanged, so the daemon samples once.
func TestProgressSampleCadence(t *testing.T) {
	t.Parallel()

	rt := progressRt()
	start := progressStart

	b := store.Binding{Name: "webshop", Round: 1, RoundStartedAt: start}
	b = progressStep(rt, b, start, signals{tree: "t0"})
	first := b.Progress.SampledAt

	b = progressStep(rt, b, start.Add(10*time.Second), signals{tree: "t1"})
	if !b.Progress.SampledAt.Equal(first) {
		t.Errorf("SampledAt = %s, want %s: the second call is inside the interval", b.Progress.SampledAt, first)
	}
	if b.Progress.Tree != "t0" {
		t.Errorf("Tree = %q, want t0: a sample inside the interval must not record", b.Progress.Tree)
	}

	// Past the interval, the next call samples the new value.
	b = progressStep(rt, b, start.Add(30*time.Second), signals{tree: "t1"})
	if b.Progress.Tree != "t1" {
		t.Errorf("Tree = %q, want t1 once the interval has passed", b.Progress.Tree)
	}
}

// TestProgressSamplingIsGatedByInterval pins the read the progress clock must
// NOT make (#135 follow-up): a tick inside policy.json's progress_interval_ms
// samples nothing, so neither the tree nor the screen is read. The pane fixture
// is TestReconcilePaneStampsStall's, with a Working builder and nothing else
// that reads a screen, so every recent-unwrapped read counted here is a
// progress sample: ticks at t, t+10s and t+20s fall inside one 30s interval and
// cost one read and one TreeFingerprint; the tick at t+30s is the second of
// each.
//
// Mutation check: without the progressDue gate the sampler runs on every tick,
// so the first three ticks already cost three reads and the test fails.
