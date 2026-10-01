package relevo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestOOMKilledFalseWithScopesOff pins that oomKilled is always false when
// rt.Scope is nil, and that it never queries the runner.
//
// Mutation check: remove the `rt.Scope == nil` guard in oomKilled and this
// test fails on scopeResultQueries being non-empty.
func TestOOMKilledFalseWithScopesOff(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	fr.scopeResults = map[string]spawn.ScopeResult{
		"relevo-round-local-webshop-1.scope": {Result: scopeResultOOM},
	}
	rt, b := sentHeadless(t, fr)
	rt.Scope = nil // scopes off

	if peak, ok := oomKilled(context.Background(), rt, b); ok || peak != 0 {
		t.Error("oomKilled with scopes off = true, want false")
	}
	if len(fr.scopeResultQueries) != 0 {
		t.Errorf("scopeResultQueries = %v, want none (scopes off must not probe)", fr.scopeResultQueries)
	}
}

// TestOOMKilledFalseForSuccess pins that a scope result of "success" is not
// an oom kill.
func TestOOMKilledFalseForSuccess(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Scope = &spawn.ScopeSpec{Unit: "test", Slice: "relevo.slice", CPUWeight: 100}
	unit := scopeUnitName(b)
	fr.scopeResults = map[string]spawn.ScopeResult{unit: {Result: "success"}}

	if _, ok := oomKilled(context.Background(), rt, b); ok {
		t.Error("oomKilled with result=success = true, want false")
	}
}

// TestOOMKilledTrueForOOMKill pins that a scope result of "oom-kill" returns
// true.
func TestOOMKilledTrueForOOMKill(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Scope = &spawn.ScopeSpec{Unit: "test", Slice: "relevo.slice", CPUWeight: 100}
	unit := scopeUnitName(b)
	fr.scopeResults = map[string]spawn.ScopeResult{unit: {Result: scopeResultOOM, PeakBytes: 8178532352}}

	peak, ok := oomKilled(context.Background(), rt, b)
	if !ok {
		t.Error("oomKilled with result=oom-kill = false, want true")
	}
	if peak != 8178532352 {
		t.Errorf("peak = %d, want the journal's 8178532352", peak)
	}
}

// TestOOMAdmissibleHoldsOnlyAtCooldown pins the exact conditions under which
// oomAdmissible returns true.
func TestOOMAdmissibleHoldsOnlyAtCooldown(t *testing.T) {
	t.Parallel()

	killTime := baseTime
	b := store.Binding{
		Owner:          "",
		QueuedAt:       killTime,
		OOMRequeue:     &store.OOMRequeue{At: killTime, Running: 3},
		State:          store.StateActive,
		RoundStartedAt: time.Time{},
	}

	// Not yet at cooldown.
	if oomAdmissible(b, 2, killTime.Add(oomCooldown-time.Second)) {
		t.Error("oomAdmissible before cooldown = true, want false")
	}

	// Exactly at cooldown, running < recorded.
	if !oomAdmissible(b, 2, killTime.Add(oomCooldown)) {
		t.Error("oomAdmissible at cooldown, running 2 < 3 = false, want true")
	}

	// Running equals recorded: not admissible.
	if oomAdmissible(b, 3, killTime.Add(oomCooldown)) {
		t.Error("oomAdmissible running == recorded = true, want false")
	}

	// Running exceeds recorded: not admissible.
	if oomAdmissible(b, 4, killTime.Add(oomCooldown)) {
		t.Error("oomAdmissible running > recorded = true, want false")
	}

	// Served round: not admissible.
	served := b
	served.Owner = "owner1"
	if oomAdmissible(served, 0, killTime.Add(oomCooldown)) {
		t.Error("oomAdmissible on served round = true, want false")
	}

	// Not queued: not admissible.
	notQueued := b
	notQueued.QueuedAt = time.Time{}
	if oomAdmissible(notQueued, 0, killTime.Add(oomCooldown)) {
		t.Error("oomAdmissible with zero QueuedAt = true, want false")
	}

	// OOMRequeue nil: not admissible.
	notOOM := b
	notOOM.OOMRequeue = nil
	if oomAdmissible(notOOM, 0, killTime.Add(oomCooldown)) {
		t.Error("oomAdmissible with nil OOMRequeue = true, want false")
	}

	// State not active: not admissible.
	inactive := b
	inactive.State = store.StateNeedsYou
	if oomAdmissible(inactive, 0, killTime.Add(oomCooldown)) {
		t.Error("oomAdmissible with state=needs_you = true, want false")
	}
}

// TestAdmitOOMQueuedAdmitsOldest pins that admitOOMQueued picks the binding
// with the oldest QueuedAt when multiple are admissible.
//
// Mutation check: drop the running < b.OOMRequeue.Running condition in
// oomAdmissible (always return true) and TestAdmitOOMQueuedWaitsForLowerRunning
// fails because it admits despite running being equal.
func TestAdmitOOMQueuedAdmitsOldest(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)

	// Queue two oom-queued bindings on the same runtime store.
	killTime := baseTime.Add(-oomCooldown - time.Minute) // well past cooldown
	bindAndOOMQueue := func(name string, queuedAt time.Time) {
		t.Helper()
		if _, err := Bind(context.Background(), rt, BindOptions{
			Name: name, Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo-" + name,
		}); err != nil {
			t.Fatalf("Bind %s: %v", name, err)
		}
		b, err := rt.Store.Load(name)
		if err != nil {
			t.Fatalf("Load %s: %v", name, err)
		}
		b.OOMRequeue = &store.OOMRequeue{At: killTime, Running: 2}
		b.QueuedAt = queuedAt
		b.State = store.StateActive
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save %s: %v", name, err)
		}
	}

	older := baseTime.Add(-2 * time.Minute)
	newer := baseTime.Add(-time.Minute)
	bindAndOOMQueue("alpha", older)
	bindAndOOMQueue("beta", newer)

	bindings, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// running = 0 < 2 (recorded), past cooldown: admissible.
	admitOOMQueued(context.Background(), at(rt, 2*time.Hour), bindings)

	// The oldest-queued (alpha) must have been admitted (PID != 0).
	alpha, err := rt.Store.Load("alpha")
	if err != nil {
		t.Fatalf("Load alpha: %v", err)
	}
	if alpha.Builder.PID == 0 {
		t.Errorf("alpha was not admitted; want the oldest-queued binding to start")
	}

	// beta must still be queued.
	beta, err := rt.Store.Load("beta")
	if err != nil {
		t.Fatalf("Load beta: %v", err)
	}
	if beta.Builder.PID != 0 {
		t.Errorf("beta was admitted; want only one per call")
	}
}

// TestAdmitOOMQueuedWaitsForLowerRunning pins that admitOOMQueued does not
// admit when running is not below the recorded count.
//
// Mutation check: drop the running < b.OOMRequeue.Running condition in
// oomAdmissible (make it always return true regardless of running count) and
// this test fails because it admits despite running being equal.
func TestAdmitOOMQueuedWaitsForLowerRunning(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)

	// Create one oom-queued binding with Running=2.
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "shop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo-shop",
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	b, err := rt.Store.Load("shop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	killTime := baseTime.Add(-oomCooldown - time.Minute)
	b.OOMRequeue = &store.OOMRequeue{At: killTime, Running: 2}
	b.QueuedAt = baseTime.Add(-2 * time.Minute)
	b.State = store.StateActive
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Simulate 2 live local rounds (running == recorded, not fewer).
	// Inject two fake live bindings into the list by loading a snapshot with PID set.
	bindings, err := rt.Store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Add a fake live local headless binding so running==2.
	liveFake := store.Binding{
		Name:  "live-fake",
		State: store.StateActive,
	}
	liveFake.Builder.PID = 9999
	liveFake.Builder.Mode = "headless"
	bindings = append(bindings, liveFake)
	liveFake2 := store.Binding{Name: "live-fake2", State: store.StateActive}
	liveFake2.Builder.PID = 9998
	liveFake2.Builder.Mode = "headless"
	bindings = append(bindings, liveFake2)

	admitOOMQueued(context.Background(), at(rt, 2*time.Hour), bindings)

	// shop must not have been admitted (running==2 is not < 2).
	shop, err := rt.Store.Load("shop")
	if err != nil {
		t.Fatalf("Load shop: %v", err)
	}
	if shop.Builder.PID != 0 {
		t.Errorf("shop was admitted with running==recorded; want no admit")
	}
}

// TestOOMNoteContainsKeyWords pins that oomNote includes the key context: the
// time, the oom-kill mention, and the git instructions.
func TestOOMNoteContainsKeyWords(t *testing.T) {
	t.Parallel()

	note := oomNote(baseTime)
	for _, want := range []string{"out of memory", "cgroup scope", "git status", "git diff"} {
		if !strings.Contains(note, want) {
			t.Errorf("oomNote missing %q:\n%s", want, note)
		}
	}
}

// TestOOMNoteSaysScopeNotHost pins the fourth wording surface: the note in the
// builder's own prompt names the round's cgroup scope and the kernel's killer,
// never a host-wide systemd-oomd kill that would send the builder looking at
// the wrong layer.
func TestOOMNoteSaysScopeNotHost(t *testing.T) {
	t.Parallel()

	note := oomNote(baseTime)
	for _, bad := range []string{"systemd-oomd", "host out of memory", "host ran out of memory"} {
		if strings.Contains(note, bad) {
			t.Errorf("oomNote contains %q, want a scope/kernel cause only:\n%s", bad, note)
		}
	}
}

// TestOOMWordsRendersThePeak pins the human rendering of the incident's peak:
// 8178532352 bytes is 7.6 GiB, and an unknown peak says so rather than
// printing zero.
func TestOOMWordsRendersThePeak(t *testing.T) {
	t.Parallel()

	cases := map[int64]string{
		0:          "killed: out of memory (peak unknown)",
		8178532352: "killed: out of memory (peak 7.6 GiB)",
	}
	for peak, want := range cases {
		if got := oomWords(peak); got != want {
			t.Errorf("oomWords(%d) = %q, want %q", peak, got, want)
		}
	}
}

// TestOOMKilledProbesSinceTheProcessStart pins the since the probe passes: the
// start of the current process's second, so a re-queued round that reused the
// unit name does not read the previous attempt's oom-kill as its own.
func TestOOMKilledProbesSinceTheProcessStart(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt = oomRT(rt, fr, b, scopeOOM)
	if b.Builder.StartedAt == 0 {
		t.Fatal("fixture left StartedAt zero; the probe would be skipped")
	}

	if _, ok := oomKilled(context.Background(), rt, b); !ok {
		t.Fatal("oomKilled = false, want the scripted oom kill")
	}
	if len(fr.scopeResultSince) != 1 {
		t.Fatalf("scopeResultSince = %v, want one query", fr.scopeResultSince)
	}
	if want := time.Unix(b.Builder.StartedAt, 0).UTC(); !fr.scopeResultSince[0].Equal(want) {
		t.Errorf("since = %v, want the process start %v", fr.scopeResultSince[0], want)
	}
}

// TestOOMKilledSkipsAnAnchorlessProcess pins the other guard: a round with no
// recorded process start has no since to filter by, so nothing is probed and
// the result is not an oom kill.
func TestOOMKilledSkipsAnAnchorlessProcess(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt = oomRT(rt, fr, b, scopeOOM)
	b.Builder.StartedAt = 0

	if peak, ok := oomKilled(context.Background(), rt, b); ok || peak != 0 {
		t.Error("oomKilled with no process start = true, want false")
	}
	if len(fr.scopeResultQueries) != 0 {
		t.Errorf("scopeResultQueries = %v, want none", fr.scopeResultQueries)
	}
}
