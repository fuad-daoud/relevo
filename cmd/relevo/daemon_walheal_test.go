package main

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// walTestHandles builds the handles a heal decision reads: a healthy database
// over a temp path, a store over a temp root, and re-exec enabled.
func walTestHandles(t *testing.T, d *db.DB) *daemonHandles {
	t.Helper()
	h := &daemonHandles{
		d:            d,
		walRequested: &atomic.Bool{},
		walStart:     time.Now(),
		reexecOK:     true,
	}
	h.rt.Store = store.New(t.TempDir())
	return h
}

// walTestDB opens a healthy database the probe succeeds against.
func walTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := openDBDirect(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("openDBDirect: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestWALHealDueStaysQuietOnAHealthyPool pins the probe half of the decision:
// a pool that answers keeps the request unset and never re-execs.
func TestWALHealDueStaysQuietOnAHealthyPool(t *testing.T) {
	h := walTestHandles(t, walTestDB(t))

	if h.walHealDue(context.Background()) {
		t.Fatal("walHealDue on a healthy pool = true, want false")
	}
	if h.walRequested.Load() {
		t.Fatal("healthy probe set the request, want it unset")
	}
	if _, ok, err := h.rt.Store.ReadWALHealFailure(); err != nil || ok {
		t.Fatalf("marker after a healthy probe = (ok=%v, err=%v), want absent", ok, err)
	}
}

// TestWALHealDueResetsTheBudgetAfterAHeal pins the amendment: a probe that
// succeeds after a heal resets the in-process count to zero and clears the
// marker, so a later, unrelated poisoning gets the full budget again. It
// fails if the old count is kept.
func TestWALHealDueResetsTheBudgetAfterAHeal(t *testing.T) {
	h := walTestHandles(t, walTestDB(t))
	h.walAttempt = 2
	if err := h.rt.Store.WriteWALHealFailure(2, time.Now(), walHealExhaustedReason); err != nil {
		t.Fatalf("WriteWALHealFailure: %v", err)
	}

	if h.walHealDue(context.Background()) {
		t.Fatal("walHealDue after a heal = true, want false")
	}
	if h.walAttempt != 0 {
		t.Fatalf("walAttempt = %d after a successful probe, want the reset to 0", h.walAttempt)
	}
	if _, ok, err := h.rt.Store.ReadWALHealFailure(); err != nil || ok {
		t.Fatalf("marker after a successful probe = (ok=%v, err=%v), want cleared", ok, err)
	}
}

// TestWALHealDueKeepsTheBudgetAndMarkerOnAFailedProbe pins the success
// gate: only a probe that answers clears anything. A probe that fails for
// another reason leaves the count, the marker and the request untouched, so
// the next tick retries instead of spending the budget on a healthy pool.
func TestWALHealDueKeepsTheBudgetAndMarkerOnAFailedProbe(t *testing.T) {
	d := walTestDB(t)
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := d.Version(); err == nil || wire.IsShortReadWAL(err) {
		t.Fatalf("Version after Close = %v, want a non-short-read error", err)
	}
	h := walTestHandles(t, d)
	h.walAttempt = 2
	if err := h.rt.Store.WriteWALHealFailure(2, time.Now(), walHealExhaustedReason); err != nil {
		t.Fatalf("WriteWALHealFailure: %v", err)
	}

	if h.walHealDue(context.Background()) {
		t.Fatal("walHealDue on a failed probe = true, want false")
	}
	if h.walAttempt != 2 {
		t.Fatalf("walAttempt = %d after a failed probe, want the kept 2", h.walAttempt)
	}
	if _, ok, err := h.rt.Store.ReadWALHealFailure(); err != nil || !ok {
		t.Fatalf("marker after a failed probe = (ok=%v, err=%v), want still present", ok, err)
	}
	if h.walRequested.Load() {
		t.Fatal("failed probe set the request, want it unset")
	}
}

// TestWALHealDueClearsAStaleMarkerAtAttemptZero pins the restart case: a
// daemon restarted after a give-up inherits no count, so a probe that
// answers at attempt 0 still clears the stale marker.
func TestWALHealDueClearsAStaleMarkerAtAttemptZero(t *testing.T) {
	h := walTestHandles(t, walTestDB(t))
	if err := h.rt.Store.WriteWALHealFailure(2, time.Now(), walHealExhaustedReason); err != nil {
		t.Fatalf("WriteWALHealFailure: %v", err)
	}

	if h.walHealDue(context.Background()) {
		t.Fatal("walHealDue after a heal = true, want false")
	}
	if h.walAttempt != 0 {
		t.Fatalf("walAttempt = %d, want the kept 0", h.walAttempt)
	}
	if _, ok, err := h.rt.Store.ReadWALHealFailure(); err != nil || ok {
		t.Fatalf("marker after a successful probe = (ok=%v, err=%v), want cleared", ok, err)
	}
}

// TestWALHealDueRetriesTheClearWhenItFails pins the clear-first ordering: a
// marker the store cannot remove keeps the count, so the next tick retries
// the clear instead of losing the record of the give-up.
func TestWALHealDueRetriesTheClearWhenItFails(t *testing.T) {
	root := t.TempDir()
	h := walTestHandles(t, walTestDB(t))
	h.rt.Store = store.New(root)
	h.walAttempt = 2
	marker := filepath.Join(root, "wal-heal-failed.json")
	if err := os.Mkdir(marker, 0o755); err != nil {
		t.Fatalf("Mkdir marker dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marker, "lock"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile lock: %v", err)
	}

	if h.walHealDue(context.Background()) {
		t.Fatal("walHealDue when the clear fails = true, want false")
	}
	if h.walAttempt != 2 {
		t.Fatalf("walAttempt = %d after a failed clear, want the kept 2", h.walAttempt)
	}
}

// TestWALHealDueFiresOnceExhaustionIsRecordedOnce pins the request half: a
// request inside the budget re-execs exactly once, and a request past it
// records the give-up once and never re-execs.
func TestWALHealDueFiresOnceExhaustionIsRecordedOnce(t *testing.T) {
	h := walTestHandles(t, walTestDB(t))
	h.walStart = time.Now().Add(-time.Hour)
	h.walRequested.Store(true)

	if !h.walHealDue(context.Background()) {
		t.Fatal("first walHealDue with a request inside the budget = false, want true")
	}
	if !h.walHealFired {
		t.Fatal("walHealFired = false after a heal decision, want true")
	}
	if h.walHealDue(context.Background()) {
		t.Fatal("second walHealDue after firing = true, want false")
	}

	exhausted := walTestHandles(t, walTestDB(t))
	exhausted.walStart = time.Now().Add(-time.Hour)
	exhausted.walAttempt = relevo.MaxWALHealAttempts
	exhausted.walRequested.Store(true)

	if exhausted.walHealDue(context.Background()) {
		t.Fatal("walHealDue past the budget = true, want false")
	}
	failure, ok, err := exhausted.rt.Store.ReadWALHealFailure()
	if err != nil || !ok {
		t.Fatalf("marker after exhaustion = (ok=%v, err=%v), want present", ok, err)
	}
	if failure.Attempts != relevo.MaxWALHealAttempts {
		t.Errorf("marker attempts = %d, want %d", failure.Attempts, relevo.MaxWALHealAttempts)
	}
	if exhausted.walHealDue(context.Background()) {
		t.Fatal("walHealDue after exhaustion = true, want false")
	}
	if _, ok, err := exhausted.rt.Store.ReadWALHealFailure(); err != nil || !ok {
		t.Fatalf("marker after a second exhausted tick = (ok=%v, err=%v), want still present", ok, err)
	}
}

// TestWALHealDueWaitsOutTheBackoff pins the debounce: a request inside the
// budget but before its backoff neither re-execs nor records.
func TestWALHealDueWaitsOutTheBackoff(t *testing.T) {
	h := walTestHandles(t, walTestDB(t))
	h.walAttempt = 1
	h.walRequested.Store(true)

	if h.walHealDue(context.Background()) {
		t.Fatal("walHealDue before the backoff = true, want false")
	}
	if h.walHealFired || h.walMarked {
		t.Fatal("a tick before the backoff fired or recorded, want neither")
	}
}

// TestWALHealDueNeedsReexec pins the gate: without a re-exec path the daemon
// must not ask for one, however poisoned the pool.
func TestWALHealDueNeedsReexec(t *testing.T) {
	h := walTestHandles(t, walTestDB(t))
	h.reexecOK = false
	h.walRequested.Store(true)

	if h.walHealDue(context.Background()) {
		t.Fatal("walHealDue without re-exec = true, want false")
	}
}

// TestInitWALHealArmsTheRequest pins the image's start state: the hook the
// owner installs, the count carried across the exec, and no hook at all
// without a re-exec path.
func TestInitWALHealArmsTheRequest(t *testing.T) {
	t.Setenv(walHealAttemptEnv, "2")
	h := &daemonHandles{reexecOK: true}
	hook := initWALHeal(h)
	if hook == nil {
		t.Fatal("initWALHeal with re-exec = nil hook, want the request hook")
	}
	if h.walAttempt != 2 {
		t.Fatalf("walAttempt = %d, want the inherited 2", h.walAttempt)
	}
	if h.walRequested == nil {
		t.Fatal("walRequested is nil, want the armed request")
	}
	hook()
	if !h.walRequested.Load() {
		t.Fatal("the hook did not set the request")
	}

	plain := &daemonHandles{reexecOK: false}
	if hook := initWALHeal(plain); hook != nil {
		t.Fatal("initWALHeal without re-exec = non-nil hook, want nil")
	}
}

// TestWALHealAttemptFromEnv pins the count's parsing: only a non-negative
// number survives the exec, anything else reads as the full budget.
func TestWALHealAttemptFromEnv(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"0", 0},
		{"2", 2},
		{"3", 3},
		{"nope", 0},
		{"-1", 0},
	} {
		if got := walHealAttemptFromEnv(tc.in); got != tc.want {
			t.Errorf("walHealAttemptFromEnv(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestWALHealReexecEnvCarriesTheCountOnlyAfterAHeal pins the exec boundary:
// a heal hands the next image its incremented count, every other re-exec
// drops a stale one.
func TestWALHealReexecEnvCarriesTheCountOnlyAfterAHeal(t *testing.T) {
	healed := &daemonHandles{walHealFired: true, walAttempt: 1}
	got := walHealReexecEnv([]string{"A=1", walHealAttemptEnv + "=1"}, healed)
	found := false
	for _, e := range got {
		if e == walHealAttemptEnv+"=2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("heal re-exec env = %q, want the count carried as 2", got)
	}

	upgraded := &daemonHandles{}
	got = walHealReexecEnv([]string{"A=1", walHealAttemptEnv + "=2"}, upgraded)
	for _, e := range got {
		if len(e) > len(walHealAttemptEnv) && e[:len(walHealAttemptEnv)+1] == walHealAttemptEnv+"=" {
			t.Fatalf("upgrade re-exec env kept %q, want it dropped", e)
		}
	}
	if len(got) != 1 {
		t.Fatalf("upgrade re-exec env = %q, want only the unrelated entry", got)
	}
}

// TestWithoutEnvDropsTheHealCount pins the upgrade re-exec's cleanup: the
// next image inherits no heal count it must not act on.
func TestWithoutEnvDropsTheHealCount(t *testing.T) {
	env := []string{"A=1", walHealAttemptEnv + "=2", "B=3"}
	got := withoutEnv(env, walHealAttemptEnv)
	for _, e := range got {
		if len(e) > len(walHealAttemptEnv) && e[:len(walHealAttemptEnv)+1] == walHealAttemptEnv+"=" {
			t.Fatalf("withoutEnv kept %q, want it dropped", e)
		}
	}
	if len(got) != 2 {
		t.Fatalf("withoutEnv kept %d entries, want the 2 unrelated ones", len(got))
	}
}
