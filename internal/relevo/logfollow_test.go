package relevo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// followedBinding saves a bare binding with two log entries and returns its
// name: enough to prove FollowLog drains what is already there, then follows.
func followedBinding(t *testing.T, rt Runtime) string {
	t.Helper()
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, State: store.StateActive}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := rt.Store.AppendLog(b.Name, store.LogEntry{
			TS: rt.Now().UTC(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true,
		}); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	return b.Name
}

func TestFollowLogEmitsNewEntriesThenStopsOnDone(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	name := followedBinding(t, rt)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	emitted := make(chan int, 16)
	done := make(chan error, 1)
	go func() {
		done <- FollowLog(ctx, rt, name, 0, time.Millisecond, func(e store.LogEntry) {
			emitted <- e.Seq
		})
	}()

	// The first two entries are already in the log: FollowLog must emit them
	// before it starts waiting for the third.
	for want := 1; want <= 2; want++ {
		select {
		case seq := <-emitted:
			if seq != want {
				t.Fatalf("emit %d = %d, want %d", want, seq, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for entry %d", want)
		}
	}

	// Append the third, then close the round. The poll that sees DONE also
	// sees the third entry, and the closing entry must come out before the
	// loop returns.
	if err := rt.Store.AppendLog(name, store.LogEntry{
		TS: rt.Now().UTC(), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog third: %v", err)
	}
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.State = store.StateDone
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save done: %v", err)
	}

	select {
	case seq := <-emitted:
		if seq != 3 {
			t.Fatalf("emit 3 = %d, want 3", seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("entry 3 never emitted; DONE must be checked after draining")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("FollowLog: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FollowLog did not return after the binding was marked DONE")
	}

	select {
	case seq := <-emitted:
		t.Fatalf("extra emit %d; each entry must be emitted exactly once", seq)
	default:
	}
}

func TestFollowLogStopsWhenBindingRemoved(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	name := followedBinding(t, rt)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- FollowLog(ctx, rt, name, 0, time.Millisecond, func(store.LogEntry) {})
	}()

	// Let a few polls run, then remove the binding: a binding that disappears
	// mid-follow ends the loop with a nil error.
	time.Sleep(20 * time.Millisecond)
	if err := rt.Store.Delete(name); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("FollowLog: %v, want nil for a removed binding", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FollowLog did not return after the binding was removed")
	}
}

func TestFollowLogHonoursCancel(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	name := followedBinding(t, rt)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- FollowLog(ctx, rt, name, 0, time.Millisecond, func(store.LogEntry) {})
	}()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("FollowLog = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FollowLog did not return after cancel")
	}
}
