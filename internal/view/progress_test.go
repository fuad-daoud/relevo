package view

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestBindingShape pins the row's shape word: a reader names itself, while a
// writer -- explicit or absent -- reads as "" so its document is unchanged.
func TestBindingShape(t *testing.T) {
	t.Parallel()

	if got := BindingShape(store.Binding{Shape: store.ShapeReader}); got != store.ShapeReader {
		t.Errorf("BindingShape(reader) = %q, want %q", got, store.ShapeReader)
	}
	if got := BindingShape(store.Binding{Shape: store.ShapeWriter}); got != "" {
		t.Errorf("BindingShape(writer) = %q, want empty", got)
	}
	if got := BindingShape(store.Binding{}); got != "" {
		t.Errorf("BindingShape(absent) = %q, want empty", got)
	}
}

// TestProgressDue pins the sampling gate: a binding that never sampled is due
// at once, a recent sample waits out the interval, and one exactly on the
// interval is due again.
func TestProgressDue(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	interval := 30 * time.Second

	if !ProgressDue(store.Binding{}, now, interval) {
		t.Error("ProgressDue without progress = false, want true: never sampled is due at once")
	}
	recent := store.Binding{Progress: &store.Progress{SampledAt: now.Add(-10 * time.Second)}}
	if ProgressDue(recent, now, interval) {
		t.Error("ProgressDue 10s after sample = true, want false inside the 30s interval")
	}
	due := store.Binding{Progress: &store.Progress{SampledAt: now.Add(-interval)}}
	if !ProgressDue(due, now, interval) {
		t.Error("ProgressDue exactly on the interval = false, want true")
	}
}

// TestLabelsOf pins the status row's progress words: a stall outranks
// exploring, each word carries its age, the stale word rides alongside either,
// and a binding with no label renders "".
func TestLabelsOf(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)

	stalled := store.Binding{
		StalledSince:   now.Add(-5 * time.Minute),
		ExploringSince: now.Add(-20 * time.Minute),
	}
	working, stale := LabelsOf(stalled, now)
	if working != "stalled 5m" {
		t.Errorf("LabelsOf stalled working = %q, want %q", working, "stalled 5m")
	}
	if stale != "" {
		t.Errorf("LabelsOf stalled stale = %q, want empty", stale)
	}

	exploring := store.Binding{
		ExploringSince: now.Add(-25 * time.Minute),
		StaleSince:     now.Add(-90 * time.Second),
	}
	working, stale = LabelsOf(exploring, now)
	if working != "exploring 25m" {
		t.Errorf("LabelsOf exploring working = %q, want %q", working, "exploring 25m")
	}
	if stale != "stale 1m" {
		t.Errorf("LabelsOf exploring stale = %q, want %q", stale, "stale 1m")
	}

	working, stale = LabelsOf(store.Binding{}, now)
	if working != "" || stale != "" {
		t.Errorf("LabelsOf plain = (%q, %q), want empty words", working, stale)
	}
}
