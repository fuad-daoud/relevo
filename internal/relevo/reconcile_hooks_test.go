package relevo

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/store"
)

type recordDispatcher struct {
	mu     sync.Mutex
	events []hooks.Event
	// probe, when set, runs inside the same lock as the append: it reads the
	// store at dispatch time, which is how a test tells an event that
	// announced committed state from one that announced a proposal (#909).
	probe func(hooks.Event)
}

func (r *recordDispatcher) Dispatch(ctx context.Context, event hooks.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	if r.probe != nil {
		r.probe(event)
	}
}

func (r *recordDispatcher) getEvents() []hooks.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]hooks.Event, len(r.events))
	copy(cp, r.events)
	return cp
}

// tickCommit runs one daemon tick over b and returns the binding as it stands
// on disk afterwards. Hook events belong to that commit (#909), so a hook test
// drives this rather than Reconcile: Reconcile persists nothing, and a listener
// must never be told about a transition the store has not taken.
func tickCommit(t *testing.T, rt Runtime, b store.Binding) store.Binding {
	t.Helper()
	if err := NewDaemon(rt, time.Second).tickOne(context.Background(), b); err != nil {
		t.Fatalf("tickOne: %v", err)
	}
	got, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load after tick: %v", err)
	}
	return got
}

func TestReconcile_EmitsRoundStartedOnReport(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	disp := &recordDispatcher{}
	// Read the binding back at dispatch time: an event fired before the save
	// would still see round 1, and that is the regression this pins.
	var atDispatch []int
	disp.probe = func(hooks.Event) {
		cur, err := rt.Store.Load(b.Name)
		if err != nil {
			atDispatch = append(atDispatch, -1)
			return
		}
		atDispatch = append(atDispatch, cur.Round)
	}
	rt.Hooks = disp

	// Write report file so handleIdleBuilder completes the round
	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	if err := os.WriteFile(reportPath, []byte("round 1 report"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	out := tickCommit(t, rt, b)
	if out.Round != 2 {
		t.Fatalf("expected round 2, got %d", out.Round)
	}

	events := disp.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 event, got %d: %+v", len(events), events)
	}
	e := events[0]
	if e.Type != hooks.EventRoundStarted {
		t.Errorf("expected EventRoundStarted, got %s", e.Type)
	}
	if e.Round != 2 {
		t.Errorf("expected Round 2, got %d", e.Round)
	}
	if e.BindingID != b.Name {
		t.Errorf("expected BindingID %s, got %s", b.Name, e.BindingID)
	}
	if len(atDispatch) != 1 || atDispatch[0] != 2 {
		t.Errorf("round on disk at dispatch = %v, want [2]: the event must follow the save", atDispatch)
	}
}

func TestReconcile_NoEventsWhenUnchanged(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	disp := &recordDispatcher{}
	rt.Hooks = disp

	tickCommit(t, rt, b)

	events := disp.getEvents()
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d: %+v", len(events), events)
	}
}

// TestReconcile_SaveFailureEmitsNothingAndTheRetryEmitsOnce pins #909's
// headline case: a reconcile whose commit was refused announces nothing at all,
// the stored state is untouched, and the retry that commits the same
// transition announces it exactly once.
//
// The provocation is the binding's own directory: replacing it with a regular
// file makes the save at the end of the tick refuse ("create binding dir: ...
// is not a directory"), while the reconcile above it still runs and still
// proposes its transition. The stale stamp is that transition -- the one change
// this tick proposes.
func TestReconcile_SaveFailureEmitsNothingAndTheRetryEmitsOnce(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	disp := &recordDispatcher{}
	rt.Hooks = disp

	haltedAt := baseTime.Add(-5 * time.Hour)
	b.State = store.StateNeedsYou
	b.Halt = "builder blocked at a dialog"
	b.HaltAt = haltedAt
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	dir := rt.Store.Dir(b.Name)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove binding dir: %v", err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("plant file at the binding dir: %v", err)
	}

	d := NewDaemon(rt, time.Second)
	if err := d.tickOne(context.Background(), b); err == nil {
		t.Fatal("tickOne = nil, want the save refused")
	} else if !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("tickOne error = %v, want the refusal to come from the save", err)
	}

	if events := disp.getEvents(); len(events) != 0 {
		t.Fatalf("a refused save announced %d events, want 0: %+v", len(events), events)
	}
	stored, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load after the refused tick: %v", err)
	}
	if !stored.StaleSince.IsZero() {
		t.Errorf("StaleSince = %s on disk, want it unstamped: the save did not commit", stored.StaleSince)
	}

	// The retry commits the same transition, and announces it once.
	if err := os.Remove(dir); err != nil {
		t.Fatalf("remove the planted file: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("restore binding dir: %v", err)
	}
	got := tickCommit(t, rt, stored)
	if !got.StaleSince.Equal(haltedAt) {
		t.Fatalf("StaleSince = %s after the retry, want the halt time %s", got.StaleSince, haltedAt)
	}
	if events := disp.getEvents(); len(events) != 1 || events[0].Type != hooks.EventBindingStale {
		t.Fatalf("retry announced %+v, want exactly one binding_stale", events)
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
	// The stalled round is store state, not an in-memory patch: the tick reads
	// the binding back from disk, as the daemon does.
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save stalled binding: %v", err)
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
		got = tickCommit(t, rt, got)
	}
	if n := stalls(); n != 1 {
		t.Fatalf("builder_stalled events across three stalled ticks = %d, want exactly 1", n)
	}

	// The stream moves: the stall clears and no further event fires.
	moved := now
	if err := os.Chtimes(stream, moved, moved); err != nil {
		t.Fatalf("chtimes back: %v", err)
	}
	tickCommit(t, rt, got)
	if n := stalls(); n != 1 {
		t.Fatalf("builder_stalled events after the stall cleared = %d, want still 1", n)
	}
}

// TestSettleCatchUpAnnouncesAfterItsSave pins the second emit-before-save site
// (#909): the remote catch-up settle collects a closed round in its own lock
// and saves it there, and its round_started event is announced only once that
// save committed -- the same rule tickOne follows.
func TestSettleCatchUpAnnouncesAfterItsSave(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	disp := &recordDispatcher{}
	var atDispatch []int
	disp.probe = func(hooks.Event) {
		cur, err := st.Load("api")
		if err != nil {
			atDispatch = append(atDispatch, -1)
			return
		}
		atDispatch = append(atDispatch, cur.Round)
	}
	rt := Runtime{
		Store: st, Remote: roundClosedRemote(), Transport: &fakeTransport{},
		Hooks: disp, Now: func() time.Time { return baseTime },
	}

	if err := NewDaemon(rt, time.Second).Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want the settled round 2", got.Round)
	}
	events := disp.getEvents()
	if len(events) != 1 || events[0].Type != hooks.EventRoundStarted || events[0].Round != 2 {
		t.Fatalf("settle announced %+v, want one round_started for round 2", events)
	}
	if len(atDispatch) != 1 || atDispatch[0] != 2 {
		t.Errorf("round on disk at dispatch = %v, want [2]: the event must follow the save", atDispatch)
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
