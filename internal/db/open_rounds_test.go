package db

import (
	"testing"
	"time"
)

func TestOpenRoundsOnFinishedBindingsCountsDoneAndArchivedOnly(t *testing.T) {
	d := openTestDB(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	done, archived := "done", now

	add := func(name string, mutate func(*Binding), rounds ...string) {
		t.Helper()
		b := newTestBinding(name, now)
		mutate(&b)
		id, err := d.UpsertBinding(b)
		if err != nil {
			t.Fatalf("UpsertBinding %s: %v", name, err)
		}
		for i, outcome := range rounds {
			if _, err := d.UpsertRound(newTestRound(id, i+1, outcome)); err != nil {
				t.Fatalf("UpsertRound %s/%d: %v", name, i+1, err)
			}
		}
	}
	add("finished", func(b *Binding) { b.FinalState = &done }, OutcomeOpen, OutcomeOpen, OutcomeReported)
	add("shelved", func(b *Binding) { b.ArchivedAt = &archived }, OutcomeOpen)
	add("live", func(*Binding) {}, OutcomeOpen)

	rounds, bindings, err := d.OpenRoundsOnFinishedBindings()
	if err != nil {
		t.Fatalf("OpenRoundsOnFinishedBindings: %v", err)
	}
	if rounds != 3 || bindings != 2 {
		t.Errorf("got %d rounds on %d bindings, want 3 on 2", rounds, bindings)
	}
}
