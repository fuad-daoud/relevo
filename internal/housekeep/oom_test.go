package housekeep

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// stubProber is the journal probe OOMKilled drives: the scripted scope result
// per unit, with every query and since recorded so a test can prove what the
// probe asked for -- or that none ran.
type stubProber struct {
	results map[string]spawn.ScopeResult
	queries []string
	sinces  []time.Time
	err     error
}

func (s *stubProber) ScopeResult(_ context.Context, unit string, since time.Time) (spawn.ScopeResult, error) {
	s.queries = append(s.queries, unit)
	s.sinces = append(s.sinces, since)
	if s.err != nil {
		return spawn.ScopeResult{}, s.err
	}
	return s.results[unit], nil
}

// oomBinding is a local headless binding with a live process, the shape the
// oom probe and the admit rule read.
func oomBinding(name string, startedAt int64) store.Binding {
	b := store.Binding{Name: name, State: store.StateActive}
	b.Builder.PID = 4001
	b.Builder.StartedAt = startedAt
	b.Builder.Mode = "headless"
	return b
}

// oomDeps is a Deps with scopes on, the unit name fixed and the probe stubbed.
func oomDeps(st *store.Store, prober *stubProber, now time.Time) Deps {
	return Deps{
		Store:       st,
		Scope:       &spawn.ScopeSpec{},
		UnitName:    func(b store.Binding) string { return "relevo-round-local-" + b.Name + "-1" },
		ScopeProber: prober,
		Now:         func() time.Time { return now },
	}
}

// TestOOMKilledFalseWithScopesOff pins that OOMKilled is always false when
// scopes are off, and that it never queries the runner.
func TestOOMKilledFalseWithScopesOff(t *testing.T) {
	t.Parallel()

	prober := &stubProber{results: map[string]spawn.ScopeResult{
		"relevo-round-local-webshop-1": {Result: scopeResultOOM},
	}}
	b := oomBinding("webshop", baseTime.Unix())
	deps := oomDeps(store.New(t.TempDir()), prober, baseTime)
	deps.Scope = nil // scopes off

	if peak, ok := OOMKilled(context.Background(), deps, b); ok || peak != 0 {
		t.Error("OOMKilled with scopes off = true, want false")
	}
	if len(prober.queries) != 0 {
		t.Errorf("queries = %v, want none (scopes off must not probe)", prober.queries)
	}
}

// TestOOMKilledFalseForSuccess pins that a scope result of "success" is not
// an oom kill.
func TestOOMKilledFalseForSuccess(t *testing.T) {
	t.Parallel()

	prober := &stubProber{results: map[string]spawn.ScopeResult{
		"relevo-round-local-webshop-1": {Result: "success"},
	}}
	b := oomBinding("webshop", baseTime.Unix())

	if _, ok := OOMKilled(context.Background(), oomDeps(store.New(t.TempDir()), prober, baseTime), b); ok {
		t.Error("OOMKilled with result=success = true, want false")
	}
}

// TestOOMKilledTrueForOOMKill pins that a scope result of "oom-kill" returns
// true.
func TestOOMKilledTrueForOOMKill(t *testing.T) {
	t.Parallel()

	prober := &stubProber{results: map[string]spawn.ScopeResult{
		"relevo-round-local-webshop-1": {Result: scopeResultOOM, PeakBytes: 8178532352},
	}}
	b := oomBinding("webshop", baseTime.Unix())

	peak, ok := OOMKilled(context.Background(), oomDeps(store.New(t.TempDir()), prober, baseTime), b)
	if !ok {
		t.Error("OOMKilled with result=oom-kill = false, want true")
	}
	if peak != 8178532352 {
		t.Errorf("peak = %d, want the journal's 8178532352", peak)
	}
}

// TestOOMAdmissibleHoldsOnlyAtCooldown pins the exact conditions under which
// OOMAdmissible returns true.
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
	if OOMAdmissible(b, 2, killTime.Add(oomCooldown-time.Second)) {
		t.Error("OOMAdmissible before cooldown = true, want false")
	}

	// Exactly at cooldown, running < recorded.
	if !OOMAdmissible(b, 2, killTime.Add(oomCooldown)) {
		t.Error("OOMAdmissible at cooldown, running 2 < 3 = false, want true")
	}

	// Running equals recorded: not admissible.
	if OOMAdmissible(b, 3, killTime.Add(oomCooldown)) {
		t.Error("OOMAdmissible running == recorded = true, want false")
	}

	// Running exceeds recorded: not admissible.
	if OOMAdmissible(b, 4, killTime.Add(oomCooldown)) {
		t.Error("OOMAdmissible running > recorded = true, want false")
	}

	// Served round: not admissible.
	served := b
	served.Owner = "owner1"
	if OOMAdmissible(served, 0, killTime.Add(oomCooldown)) {
		t.Error("OOMAdmissible on served round = true, want false")
	}

	// Not queued: not admissible.
	notQueued := b
	notQueued.QueuedAt = time.Time{}
	if OOMAdmissible(notQueued, 0, killTime.Add(oomCooldown)) {
		t.Error("OOMAdmissible with zero QueuedAt = true, want false")
	}

	// OOMRequeue nil: not admissible.
	notOOM := b
	notOOM.OOMRequeue = nil
	if OOMAdmissible(notOOM, 0, killTime.Add(oomCooldown)) {
		t.Error("OOMAdmissible with nil OOMRequeue = true, want false")
	}

	// State not active: not admissible.
	inactive := b
	inactive.State = store.StateNeedsYou
	if OOMAdmissible(inactive, 0, killTime.Add(oomCooldown)) {
		t.Error("OOMAdmissible with state=needs_you = true, want false")
	}
}

// admitStub is the engine admission AdmitOOMQueued drives: it records the name
// and marks the binding live, the way a real start would.
type admitStub struct {
	t        *testing.T
	store    *store.Store
	admitted []string
}

func (f *admitStub) admit(_ context.Context, name string) error {
	f.admitted = append(f.admitted, name)
	return f.store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		b.Builder.PID = 5001
		return tx.Save(b)
	})
}

// oomQueueBinding saves an oom-queued binding: re-queued at killTime, queued
// since queuedAt, running the recorded count at the kill.
func oomQueueBinding(t *testing.T, st *store.Store, name string, queuedAt time.Time) {
	t.Helper()
	killTime := baseTime.Add(-oomCooldown - time.Minute) // well past cooldown
	b := store.Binding{
		Name:           name,
		CWD:            "/repo-" + name,
		State:          store.StateActive,
		QueuedAt:       queuedAt,
		OOMRequeue:     &store.OOMRequeue{At: killTime, Running: 2},
		RoundStartedAt: time.Time{},
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save %s: %v", name, err)
	}
}

// TestAdmitOOMQueuedAdmitsOldest pins that AdmitOOMQueued picks the binding
// with the oldest QueuedAt when multiple are admissible.
func TestAdmitOOMQueuedAdmitsOldest(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	older := baseTime.Add(-2 * time.Minute)
	newer := baseTime.Add(-time.Minute)
	oomQueueBinding(t, st, "alpha", older)
	oomQueueBinding(t, st, "beta", newer)

	stub := &admitStub{t: t, store: st}
	deps := Deps{
		Store: st,
		Now:   func() time.Time { return baseTime.Add(2 * time.Hour) },
		Admit: stub.admit,
	}

	bindings, err := st.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// running = 0 < 2 (recorded), past cooldown: admissible.
	AdmitOOMQueued(context.Background(), deps, bindings)

	if len(stub.admitted) != 1 || stub.admitted[0] != "alpha" {
		t.Errorf("admitted = %v, want [alpha] (the oldest-queued binding)", stub.admitted)
	}

	// The oldest-queued (alpha) must have been admitted (PID != 0).
	alpha, err := st.Load("alpha")
	if err != nil {
		t.Fatalf("Load alpha: %v", err)
	}
	if alpha.Builder.PID == 0 {
		t.Errorf("alpha was not admitted; want the oldest-queued binding to start")
	}

	// beta must still be queued.
	beta, err := st.Load("beta")
	if err != nil {
		t.Fatalf("Load beta: %v", err)
	}
	if beta.Builder.PID != 0 {
		t.Errorf("beta was admitted; want only one per call")
	}
}

// TestAdmitOOMQueuedWaitsForLowerRunning pins that AdmitOOMQueued does not
// admit when running is not below the recorded count.
func TestAdmitOOMQueuedWaitsForLowerRunning(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	oomQueueBinding(t, st, "shop", baseTime.Add(-2*time.Minute))

	stub := &admitStub{t: t, store: st}
	deps := Deps{
		Store: st,
		Now:   func() time.Time { return baseTime.Add(2 * time.Hour) },
		Admit: stub.admit,
	}

	bindings, err := st.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Two live local headless rounds, so running == 2 == recorded: not fewer.
	for _, name := range []string{"live-fake", "live-fake2"} {
		b := oomBinding(name, baseTime.Unix())
		b.Builder.PID = 6000 + len(name)
		bindings = append(bindings, b)
	}

	AdmitOOMQueued(context.Background(), deps, bindings)

	if len(stub.admitted) != 0 {
		t.Errorf("admitted = %v, want none with running==recorded", stub.admitted)
	}
	shop, err := st.Load("shop")
	if err != nil {
		t.Fatalf("Load shop: %v", err)
	}
	if shop.Builder.PID != 0 {
		t.Errorf("shop was admitted with running==recorded; want no admit")
	}
}

// TestOOMNoteContainsKeyWords pins that OOMNote includes the key context: the
// time, the oom-kill mention, and the git instructions.
func TestOOMNoteContainsKeyWords(t *testing.T) {
	t.Parallel()

	note := OOMNote(baseTime)
	for _, want := range []string{"out of memory", "cgroup scope", "git status", "git diff"} {
		if !strings.Contains(note, want) {
			t.Errorf("OOMNote missing %q:\n%s", want, note)
		}
	}
}

// TestOOMNoteSaysScopeNotHost pins that the note names the round's cgroup
// scope and the kernel's killer, never a host-wide systemd-oomd kill.
func TestOOMNoteSaysScopeNotHost(t *testing.T) {
	t.Parallel()

	note := OOMNote(baseTime)
	for _, bad := range []string{"systemd-oomd", "host out of memory", "host ran out of memory"} {
		if strings.Contains(note, bad) {
			t.Errorf("OOMNote contains %q, want a scope/kernel cause only:\n%s", bad, note)
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
		if got := OOMWords(peak); got != want {
			t.Errorf("OOMWords(%d) = %q, want %q", peak, got, want)
		}
	}
}

// TestOOMKilledProbesSinceTheProcessStart pins the since the probe passes: the
// start of the current process's second, so a re-queued round that reused the
// unit name does not read the previous attempt's oom-kill as its own.
func TestOOMKilledProbesSinceTheProcessStart(t *testing.T) {
	t.Parallel()

	prober := &stubProber{results: map[string]spawn.ScopeResult{
		"relevo-round-local-webshop-1": {Result: scopeResultOOM, PeakBytes: 8178532352},
	}}
	b := oomBinding("webshop", baseTime.Unix())
	deps := oomDeps(store.New(t.TempDir()), prober, baseTime)

	if _, ok := OOMKilled(context.Background(), deps, b); !ok {
		t.Fatal("OOMKilled = false, want the scripted oom kill")
	}
	if len(prober.sinces) != 1 {
		t.Fatalf("sinces = %v, want one query", prober.sinces)
	}
	if want := time.Unix(b.Builder.StartedAt, 0).UTC(); !prober.sinces[0].Equal(want) {
		t.Errorf("since = %v, want the process start %v", prober.sinces[0], want)
	}
}

// TestOOMKilledSkipsAnAnchorlessProcess pins the other guard: a round with no
// recorded process start has no since to filter by, so nothing is probed and
// the result is not an oom kill.
func TestOOMKilledSkipsAnAnchorlessProcess(t *testing.T) {
	t.Parallel()

	prober := &stubProber{}
	b := oomBinding("webshop", 0)
	deps := oomDeps(store.New(t.TempDir()), prober, baseTime)

	if peak, ok := OOMKilled(context.Background(), deps, b); ok || peak != 0 {
		t.Error("OOMKilled with no process start = true, want false")
	}
	if len(prober.queries) != 0 {
		t.Errorf("queries = %v, want none", prober.queries)
	}
}
