package relevo

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/store"
)

type recordDispatcher struct {
	mu     sync.Mutex
	events []hooks.Event
}

func (r *recordDispatcher) Dispatch(ctx context.Context, event hooks.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recordDispatcher) getEvents() []hooks.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]hooks.Event, len(r.events))
	copy(cp, r.events)
	return cp
}

func TestReconcile_EmitsRoundStartedOnReport(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	disp := &recordDispatcher{}
	rt.Hooks = disp

	// Write report file so handleIdleBuilder completes the round
	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	if err := os.WriteFile(reportPath, []byte("round 1 report"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		out, err = Reconcile(context.Background(), rt, tx, b)
		return err
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if out.Round != 2 {
		t.Fatalf("expected round 2, got %d", out.Round)
	}

	events := disp.getEvents()
	var hasRoundStarted bool
	for _, e := range events {
		if e.Type == hooks.EventRoundStarted {
			hasRoundStarted = true
			if e.Round != 2 {
				t.Errorf("expected Round 2, got %d", e.Round)
			}
			if e.BindingID != b.Name {
				t.Errorf("expected BindingID %s, got %s", b.Name, e.BindingID)
			}
		}
	}
	if !hasRoundStarted {
		t.Errorf("expected EventRoundStarted in events: %+v", events)
	}
}

func TestReconcile_NoEventsWhenUnchanged(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	disp := &recordDispatcher{}
	rt.Hooks = disp

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, err := Reconcile(context.Background(), rt, tx, b)
		return err
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	events := disp.getEvents()
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d: %+v", len(events), events)
	}
}

// TestBuilderStalledHookFiresOncePerEpisode pins #252's hook contract: the
// builder_stalled event fires exactly once when a stall is first stamped,
// never again while it persists, and never when it clears.

// TestBuilderStalledHookFiresOncePerEpisode pins #252's hook contract: the
// builder_stalled event fires exactly once when a stall is first stamped,
// never again while it persists, and never when it clears.
func TestBuilderStalledHookFiresOncePerEpisode(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	disp := &recordDispatcher{}
	rt.Hooks = disp

	now := baseTime.Add(10 * time.Minute)
	rt = at(rt, 10*time.Minute)
	b.RoundStartedAt = now.Add(-30 * time.Minute)
	stream := rt.Store.RunnerStreamPath(b.Name, b.Round)
	if err := os.WriteFile(stream, []byte("line\n"), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	quietAt := now.Add(-20 * time.Minute)
	if err := os.Chtimes(stream, quietAt, quietAt); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	stalls := func() int {
		n := 0
		for _, e := range disp.getEvents() {
			if e.Type == hooks.EventBuilderStalled {
				n++
			}
		}
		return n
	}

	got := b
	for i := 0; i < 3; i++ {
		var err error
		got, err = reconcile(t, rt, got)
		if err != nil {
			t.Fatalf("Reconcile (stalled tick %d): %v", i+1, err)
		}
	}
	if n := stalls(); n != 1 {
		t.Fatalf("builder_stalled events across three stalled ticks = %d, want exactly 1", n)
	}

	// The stream moves: the stall clears and no further event fires.
	moved := now
	if err := os.Chtimes(stream, moved, moved); err != nil {
		t.Fatalf("chtimes back: %v", err)
	}
	if _, err := reconcile(t, rt, got); err != nil {
		t.Fatalf("Reconcile (cleared): %v", err)
	}
	if n := stalls(); n != 1 {
		t.Fatalf("builder_stalled events after the stall cleared = %d, want still 1", n)
	}
}

func TestDone_EmitsStateChanged(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	disp := &recordDispatcher{}
	rt.Hooks = disp

	if _, err := Done(context.Background(), rt, b.Name); err != nil {
		t.Fatalf("Done: %v", err)
	}

	events := disp.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(events), events)
	}
	if events[0].Type != hooks.EventStateChanged {
		t.Errorf("expected EventStateChanged, got %s", events[0].Type)
	}
	if events[0].State != string(store.StateDone) {
		t.Errorf("expected State DONE, got %s", events[0].State)
	}
	if events[0].OldState != string(store.StateActive) {
		t.Errorf("expected OldState ACTIVE, got %s", events[0].OldState)
	}
}
