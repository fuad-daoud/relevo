package relevo

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/store"
)

type fakeWaitStore struct {
	loadFailures    int
	readLogFailures int
	transientErr    error
	binding         store.Binding
	entries         []store.LogEntry
	loadCalls       int
	readLogCalls    int
}

func (f *fakeWaitStore) Load(name string) (store.Binding, error) {
	f.loadCalls++
	if f.loadCalls == 1 {
		return f.binding, nil
	}
	if f.loadCalls-1 <= f.loadFailures {
		return store.Binding{}, f.transientErr
	}
	return f.binding, nil
}

func (f *fakeWaitStore) ReadLog(name string) ([]store.LogEntry, error) {
	f.readLogCalls++
	if f.readLogCalls == 1 {
		return f.entries, nil
	}
	if f.readLogCalls-1 <= f.readLogFailures {
		return nil, f.transientErr
	}
	return f.entries, nil
}

func closedRoundEntries(round int, reportPath string) []store.LogEntry {
	return []store.LogEntry{
		{
			Round:     round,
			Direction: store.DirToBuilder,
			Kind:      store.KindPrompt,
		},
		{
			Round:     round,
			Direction: store.DirToMasterMind,
			Kind:      store.KindReport,
			Path:      reportPath,
		},
	}
}

func TestWaitRetryTransientThenSucceeds(t *testing.T) {
	ctx := context.Background()
	transientErr := wire.NewError(1, 10, 10, "turso: error: I/O error (pwrite): quota exceeded")

	b := store.Binding{Name: "worker", Round: 1, State: store.StateActive}
	entries := closedRoundEntries(1, "out/report.md")

	fake := &fakeWaitStore{
		loadFailures:    3,
		readLogFailures: 0,
		transientErr:    transientErr,
		binding:         b,
		entries:         entries,
	}

	var stderr bytes.Buffer
	clock := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	opts := WaitOptions{
		Names:    []string{"worker"},
		Timeout:  10 * time.Minute,
		Interval: 10 * time.Millisecond,
		Peek:     true,
		reader:   fake,
		now:      func() time.Time { return clock },
		sleep: func(ctx context.Context, d time.Duration) error {
			clock = clock.Add(d)
			return nil
		},
		stderr: &stderr,
	}

	name, res, err := Wait(ctx, Runtime{Now: func() time.Time { return clock }}, opts)
	if err != nil {
		t.Fatalf("Wait returned unexpected error: %v", err)
	}
	if name != "worker" {
		t.Errorf("name = %q, want worker", name)
	}
	if res.Code != WaitClosed {
		t.Errorf("res.Code = %d, want %d (WaitClosed)", res.Code, WaitClosed)
	}
	if res.Line != "out/report.md" {
		t.Errorf("res.Line = %q, want out/report.md", res.Line)
	}

	wantNotice := "relevo: db read failed (turso: error: I/O error (pwrite): quota exceeded); retrying\n"
	if got := stderr.String(); got != wantNotice {
		t.Errorf("stderr = %q, want %q", got, wantNotice)
	}
}

func TestWaitRetryTransientReadLogThenSucceeds(t *testing.T) {
	ctx := context.Background()
	transientErr := wire.NewError(1, 10, 10, "turso: error: I/O error (pwrite): quota exceeded")

	b := store.Binding{Name: "worker", Round: 1, State: store.StateActive}
	entries := closedRoundEntries(1, "out/report.md")

	fake := &fakeWaitStore{
		loadFailures:    0,
		readLogFailures: 3,
		transientErr:    transientErr,
		binding:         b,
		entries:         entries,
	}

	var stderr bytes.Buffer
	clock := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	opts := WaitOptions{
		Names:    []string{"worker"},
		Timeout:  10 * time.Minute,
		Interval: 10 * time.Millisecond,
		Peek:     true,
		reader:   fake,
		now:      func() time.Time { return clock },
		sleep: func(ctx context.Context, d time.Duration) error {
			clock = clock.Add(d)
			return nil
		},
		stderr: &stderr,
	}

	name, res, err := Wait(ctx, Runtime{Now: func() time.Time { return clock }}, opts)
	if err != nil {
		t.Fatalf("Wait returned unexpected error: %v", err)
	}
	if name != "worker" {
		t.Errorf("name = %q, want worker", name)
	}
	if res.Code != WaitClosed {
		t.Errorf("res.Code = %d, want %d (WaitClosed)", res.Code, WaitClosed)
	}
	if res.Line != "out/report.md" {
		t.Errorf("res.Line = %q, want out/report.md", res.Line)
	}

	wantNotice := "relevo: db read failed (turso: error: I/O error (pwrite): quota exceeded); retrying\n"
	if got := stderr.String(); got != wantNotice {
		t.Errorf("stderr = %q, want %q", got, wantNotice)
	}
}

func TestWaitRetryPersistentFailure(t *testing.T) {
	ctx := context.Background()
	transientErr := wire.NewError(1, 10, 10, "turso: error: I/O error (pwrite): quota exceeded")

	b := store.Binding{Name: "worker", Round: 1, State: store.StateActive}
	entries := closedRoundEntries(1, "out/report.md")

	// 1000 failures exceeds the 60s bounded retry window.
	fake := &fakeWaitStore{
		loadFailures:    1000,
		readLogFailures: 0,
		transientErr:    transientErr,
		binding:         b,
		entries:         entries,
	}

	var stderr bytes.Buffer
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	clock := start

	opts := WaitOptions{
		Names:    []string{"worker"},
		Timeout:  10 * time.Minute,
		Interval: 10 * time.Millisecond,
		Peek:     true,
		reader:   fake,
		now:      func() time.Time { return clock },
		sleep: func(ctx context.Context, d time.Duration) error {
			clock = clock.Add(d)
			return nil
		},
		stderr: &stderr,
	}

	_, _, err := Wait(ctx, Runtime{Now: func() time.Time { return clock }}, opts)
	if err == nil {
		t.Fatal("Wait succeeded, want persistent failure error")
	}
	if !errors.Is(err, transientErr) && !strings.Contains(err.Error(), "I/O error") {
		t.Errorf("Wait err = %v, want transient error", err)
	}

	elapsed := clock.Sub(start)
	if elapsed < 60*time.Second {
		t.Errorf("elapsed fake time = %v, want >= 60s", elapsed)
	}

	// Verify notice was logged only once despite multiple retry attempts.
	noticeCount := strings.Count(stderr.String(), "relevo: db read failed")
	if noticeCount != 1 {
		t.Errorf("logged notice count = %d, want 1", noticeCount)
	}
}

func TestWaitNonTransientFailureNoRetry(t *testing.T) {
	ctx := context.Background()
	nonTransientErr := errors.New("table does not exist")

	b := store.Binding{Name: "worker", Round: 1, State: store.StateActive}
	entries := closedRoundEntries(1, "out/report.md")

	// Upfront call (call 1) succeeds, but poll loop (call 2) fails with non-transient error.
	calls := 0
	fake := &dynamicWaitStore{
		loadFn: func(name string) (store.Binding, error) {
			calls++
			if calls == 1 {
				return b, nil
			}
			return store.Binding{}, nonTransientErr
		},
		readLogFn: func(name string) ([]store.LogEntry, error) {
			return entries, nil
		},
	}

	var stderr bytes.Buffer
	clock := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	opts := WaitOptions{
		Names:    []string{"worker"},
		Timeout:  10 * time.Minute,
		Interval: 10 * time.Millisecond,
		Peek:     true,
		reader:   fake,
		now:      func() time.Time { return clock },
		stderr:   &stderr,
	}

	_, _, err := Wait(ctx, Runtime{Now: func() time.Time { return clock }}, opts)
	if err == nil {
		t.Fatal("Wait succeeded, want non-transient error")
	}
	if !errors.Is(err, nonTransientErr) {
		t.Errorf("Wait err = %v, want %v", err, nonTransientErr)
	}
	if calls != 2 {
		t.Errorf("load calls = %d, want 2 (1 up-front, 1 poll attempt)", calls)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr written = %q, want empty", stderr.String())
	}
}

func TestWaitContextCancelledMidBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transientErr := wire.NewError(1, 10, 10, "turso: error: I/O error (pwrite): quota exceeded")

	b := store.Binding{Name: "worker", Round: 1, State: store.StateActive}
	entries := closedRoundEntries(1, "out/report.md")

	calls := 0
	fake := &dynamicWaitStore{
		loadFn: func(name string) (store.Binding, error) {
			calls++
			if calls == 1 {
				return b, nil
			}
			return store.Binding{}, transientErr
		},
		readLogFn: func(name string) ([]store.LogEntry, error) {
			return entries, nil
		},
	}

	clock := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	opts := WaitOptions{
		Names:    []string{"worker"},
		Timeout:  10 * time.Minute,
		Interval: 10 * time.Millisecond,
		Peek:     true,
		reader:   fake,
		now:      func() time.Time { return clock },
		sleep: func(ctx context.Context, d time.Duration) error {
			cancel()
			return ctx.Err()
		},
	}

	_, _, err := Wait(ctx, Runtime{Now: func() time.Time { return clock }}, opts)
	if err == nil {
		t.Fatal("Wait succeeded, want context cancelled error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Wait err = %v, want context.Canceled", err)
	}
}

type dynamicWaitStore struct {
	loadFn    func(name string) (store.Binding, error)
	readLogFn func(name string) ([]store.LogEntry, error)
}

func (d *dynamicWaitStore) Load(name string) (store.Binding, error) {
	return d.loadFn(name)
}

func (d *dynamicWaitStore) ReadLog(name string) ([]store.LogEntry, error) {
	return d.readLogFn(name)
}
