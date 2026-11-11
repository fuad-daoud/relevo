package relevo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store/storetest"
)

// The WAL hook is consulted exactly once: there is no second tick after it
// returns true to end Run with ErrReexec.
func TestRunReturnsErrReexecOnWALHeal(t *testing.T) {
	t.Parallel()

	rt := Runtime{Store: storetest.New(t)}

	calls := 0
	d := NewDaemon(rt, 10*time.Millisecond).WithWALHeal(func(context.Context) bool {
		calls++
		return true
	})

	if err := d.Run(context.Background()); !errors.Is(err, ErrReexec) {
		t.Fatalf("Run = %v, want ErrReexec", err)
	}
	if calls != 1 {
		t.Errorf("wal heal hook calls = %d, want 1", calls)
	}
}

// A daemon with no WAL hook never re-execs for a poisoned pool: the hook is
// what turns the poison into a heal.
func TestRunIgnoresWALHealWhenUnset(t *testing.T) {
	t.Parallel()

	rt := Runtime{Store: storetest.New(t)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	d := NewDaemon(rt, 10*time.Millisecond)
	go func() { done <- d.Run(ctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

// TestWALHealExhausted pins the give-up boundary: only a spent budget reads
// as exhausted, so a tick before its backoff stays quiet while one past the
// budget is recorded.
func TestWALHealExhausted(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		attempt int
		want    bool
	}{
		{0, false},
		{2, false},
		{3, true},
		{99, true},
	} {
		if got := WALHealExhausted(tc.attempt); got != tc.want {
			t.Errorf("WALHealExhausted(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

// TestWALHealDue pins the heal budget: the first heal fires at once, later
// ones wait out their backoff, and past the attempt cap nothing fires.
func TestWALHealDue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		attempt    int
		sinceStart time.Duration
		want       bool
	}{
		{"first heal fires at once", 0, 0, true},
		{"second heal waits ten seconds", 1, 9 * time.Second, false},
		{"second heal fires at ten seconds", 1, 10 * time.Second, true},
		{"third heal waits a minute", 2, 59 * time.Second, false},
		{"third heal fires at a minute", 2, time.Minute, true},
		{"past the cap never fires", 3, time.Hour, false},
		{"negative attempt never fires", -1, time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := WALHealDue(tc.attempt, tc.sinceStart); got != tc.want {
				t.Errorf("WALHealDue(%d, %v) = %v, want %v", tc.attempt, tc.sinceStart, got, tc.want)
			}
		})
	}
}

// TestWALHealSettled pins the settle window: a heal is forgotten only after
// the image answered for the whole window, so a pool that repoisons right
// after each heal still spends the budget.
func TestWALHealSettled(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		attempt    int
		healthyFor time.Duration
		want       bool
	}{
		{0, 0, true},
		{1, 0, false},
		{2, walHealSettle - time.Second, false},
		{2, walHealSettle, true},
	} {
		if got := WALHealSettled(tc.attempt, tc.healthyFor); got != tc.want {
			t.Errorf("WALHealSettled(%d, %v) = %v, want %v", tc.attempt, tc.healthyFor, got, tc.want)
		}
	}
}
