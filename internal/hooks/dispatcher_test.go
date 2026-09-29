package hooks

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

var _ Dispatcher = (*LocalDispatcher)(nil)

type executionCall struct {
	ctx   context.Context
	argv  []string
	event Event
}

type mockExecutor struct {
	mu    sync.Mutex
	calls []executionCall
	ch    chan executionCall
}

func newMockExecutor() *mockExecutor {
	return &mockExecutor{
		ch: make(chan executionCall, 10),
	}
}

func (m *mockExecutor) Execute(ctx context.Context, argv []string, event Event) error {
	call := executionCall{
		ctx:   ctx,
		argv:  argv,
		event: event,
	}
	m.mu.Lock()
	m.calls = append(m.calls, call)
	m.mu.Unlock()

	select {
	case m.ch <- call:
	default:
	}
	return nil
}

func (m *mockExecutor) getCalls() []executionCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]executionCall, len(m.calls))
	copy(copied, m.calls)
	return copied
}

func (m *mockExecutor) waitCall(t *testing.T, timeout time.Duration) executionCall {
	t.Helper()
	select {
	case call := <-m.ch:
		return call
	case <-time.After(timeout):
		t.Fatal("timed out waiting for executor call")
		return executionCall{}
	}
}

func (m *mockExecutor) assertNoCalls(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case call := <-m.ch:
		t.Fatalf("unexpected call to Executor: %+v", call)
	case <-time.After(wait):
	}
}

func TestLocalDispatcher_Dispatch_Routing(t *testing.T) {
	first := []string{"/bin/hook-one", "--flag", "value"}
	second := []string{"/bin/hook-two"}

	mock := newMockExecutor()
	dispatcher := NewLocalDispatcher(Config{
		Hooks: map[string][][]string{
			string(EventStateChanged): {first, second},
			string(EventRoundStarted): {[]string{"/bin/round"}},
		},
	}, mock)

	now := time.Now()
	event := Event{
		Type:      EventStateChanged,
		BindingID: "binding-xyz",
		State:     "active",
		OldState:  "idle",
		Round:     5,
		Timestamp: now,
	}

	dispatcher.Dispatch(context.Background(), event)

	// The argv lists are launched in order but each runs in its own
	// goroutine, so their arrival order is not deterministic: collect both.
	got := [][]string{
		mock.waitCall(t, 2*time.Second).argv,
		mock.waitCall(t, 2*time.Second).argv,
	}
	if !containsArgv(got, first) || !containsArgv(got, second) {
		t.Errorf("argv calls = %v, want %v and %v", got, first, second)
	}

	mock.assertNoCalls(t, 100*time.Millisecond)

	calls := mock.getCalls()
	if len(calls) != 2 {
		t.Fatalf("expected exactly 2 calls, got %d", len(calls))
	}
	for _, call := range calls {
		if call.event.Type != event.Type {
			t.Errorf("event.Type = %v, want %v", call.event.Type, event.Type)
		}
		if call.event.BindingID != event.BindingID {
			t.Errorf("event.BindingID = %q, want %q", call.event.BindingID, event.BindingID)
		}
		if call.event.State != event.State {
			t.Errorf("event.State = %q, want %q", call.event.State, event.State)
		}
		if call.event.OldState != event.OldState {
			t.Errorf("event.OldState = %q, want %q", call.event.OldState, event.OldState)
		}
		if call.event.Round != event.Round {
			t.Errorf("event.Round = %d, want %d", call.event.Round, event.Round)
		}
		if !call.event.Timestamp.Equal(event.Timestamp) {
			t.Errorf("event.Timestamp = %v, want %v", call.event.Timestamp, event.Timestamp)
		}
	}
}

func containsArgv(calls [][]string, want []string) bool {
	for _, argv := range calls {
		if reflect.DeepEqual(argv, want) {
			return true
		}
	}
	return false
}

func TestLocalDispatcher_Dispatch_GracefulAbsence(t *testing.T) {
	t.Run("no Hooks entry at all", func(t *testing.T) {
		mock := newMockExecutor()
		dispatcher := NewLocalDispatcher(Config{}, mock)

		dispatcher.Dispatch(context.Background(), Event{Type: EventStateChanged})
		mock.assertNoCalls(t, 50*time.Millisecond)
	})

	t.Run("no argv list for the event", func(t *testing.T) {
		mock := newMockExecutor()
		dispatcher := NewLocalDispatcher(Config{
			Hooks: map[string][][]string{
				string(EventRoundStarted): {[]string{"/bin/round"}},
			},
		}, mock)

		dispatcher.Dispatch(context.Background(), Event{Type: EventStateChanged})
		mock.assertNoCalls(t, 50*time.Millisecond)
	})
}

func TestLocalDispatcher_Dispatch_NilSafety(t *testing.T) {
	t.Run("nil LocalDispatcher receiver", func(t *testing.T) {
		var d *LocalDispatcher
		d.Dispatch(context.Background(), Event{Type: EventStateChanged})
	})

	t.Run("nil Executor", func(t *testing.T) {
		d := NewLocalDispatcher(Config{
			Hooks: map[string][][]string{string(EventStateChanged): {[]string{"/bin/true"}}},
		}, nil)
		d.Dispatch(context.Background(), Event{Type: EventStateChanged})
	})

	t.Run("empty Hooks", func(t *testing.T) {
		mock := newMockExecutor()
		d := NewLocalDispatcher(Config{}, mock)
		d.Dispatch(context.Background(), Event{Type: EventStateChanged})
		mock.assertNoCalls(t, 50*time.Millisecond)
	})
}
