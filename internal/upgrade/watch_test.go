package upgrade

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

var watchBase = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func id(ino uint64) store.FileID {
	return store.FileID{Dev: 1, Ino: ino, Size: 100, ModTime: watchBase}
}

type statResult struct {
	id  store.FileID
	err error
}

func scriptedWatcher(t *testing.T, stats []statResult, preflights []error, want []Action) *Watcher {
	t.Helper()

	w := &Watcher{Path: "/usr/local/bin/relevo", Started: id(1)}
	i, pf := 0, 0
	w.Stat = func(string) (store.FileID, error) {
		if i >= len(stats) {
			t.Fatalf("Stat called %d times, only %d results scripted", i+1, len(stats))
		}
		s := stats[i]
		i++
		return s.id, s.err
	}
	w.Preflight = func(context.Context, string) error {
		if pf >= len(preflights) {
			t.Fatalf("Preflight called %d times, only %d results scripted", pf+1, len(preflights))
		}
		e := preflights[pf]
		pf++
		return e
	}

	for n, wantAction := range want {
		got := w.Check(context.Background())
		if got.Action != wantAction {
			t.Errorf("Check %d = %v, want %v", n+1, got.Action, wantAction)
		}
		if wantAction == Refused && got.Reason == "" {
			t.Errorf("Check %d refused with an empty reason", n+1)
		}
	}
	if i != len(stats) {
		t.Errorf("Stat calls = %d, want %d", i, len(stats))
	}
	if pf != len(preflights) {
		t.Errorf("Preflight calls = %d, want %d", pf, len(preflights))
	}
	return w
}

func TestWatcherCheck(t *testing.T) {
	first, second := id(2), id(3)
	boom := errors.New("policy.json: unknown field")

	tests := []struct {
		name       string
		stats      []statResult
		preflights []error
		want       []Action
	}{
		{
			name:  "unchanged is none",
			stats: []statResult{{id: id(1)}},
			want:  []Action{None},
		},
		{
			name:       "first change waits, the same identity then reexecs",
			stats:      []statResult{{id: first}, {id: first}},
			preflights: []error{nil},
			want:       []Action{Wait, Reexec},
		},
		{
			name:  "a different identity restarts the debounce",
			stats: []statResult{{id: first}, {id: second}},
			want:  []Action{Wait, Wait},
		},
		{
			name:       "a failed preflight is refused, and the same identity is not tried again",
			stats:      []statResult{{id: first}, {id: first}, {id: first}},
			preflights: []error{boom},
			want:       []Action{Wait, Refused, None},
		},
		{
			name:       "a refused identity followed by a newer one waits, then reexecs",
			stats:      []statResult{{id: first}, {id: first}, {id: second}, {id: second}},
			preflights: []error{boom, nil},
			want:       []Action{Wait, Refused, Wait, Reexec},
		},
		{
			name:  "a stat error waits and clears the pending identity",
			stats: []statResult{{id: first}, {err: errors.New("no such file")}, {id: first}},
			want:  []Action{Wait, Wait, Wait},
		},
		{
			name:  "back to our own binary is none, and clears pending",
			stats: []statResult{{id: first}, {id: id(1)}, {id: first}},
			want:  []Action{Wait, None, Wait},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scriptedWatcher(t, tc.stats, tc.preflights, tc.want)
		})
	}
}

func TestWatcherRefusedExposesIdentity(t *testing.T) {
	cur := id(2)
	w := scriptedWatcher(t,
		[]statResult{{id: cur}, {id: cur}},
		[]error{errors.New("boom")},
		[]Action{Wait, Refused},
	)
	got := w.Refused()
	if got == nil {
		t.Fatal("Refused() = nil after a refusal")
	}
	if *got != cur {
		t.Errorf("Refused() = %+v, want %+v", *got, cur)
	}
}

// Mutation: without `w.refused = nil` in Check, the fourth Check below
// returns None, not Wait.
func TestWatcherRollbackClearsRefusal(t *testing.T) {
	refused := id(2)
	w := scriptedWatcher(t,
		[]statResult{{id: refused}, {id: refused}, {id: id(1)}, {id: refused}},
		[]error{errors.New("boom")},
		[]Action{Wait, Refused, None, Wait},
	)
	if got := w.Refused(); got != nil {
		t.Errorf("Refused() = %+v after a rollback, want nil", *got)
	}
}

func TestWatcherPreflightGetsATimeoutDeadline(t *testing.T) {
	cur := id(2)
	w := &Watcher{Path: "/relevo", Started: id(1)}
	w.Stat = func(string) (store.FileID, error) { return cur, nil }

	var deadline bool
	w.Preflight = func(ctx context.Context, _ string) error {
		_, deadline = ctx.Deadline()
		return nil
	}
	w.Check(context.Background()) // debounce
	w.Check(context.Background()) // preflight
	if !deadline {
		t.Error("Preflight ran without a deadline; PreflightTimeout was not applied")
	}
}
