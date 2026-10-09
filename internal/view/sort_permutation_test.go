package view

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// sortFixtureRows covers every rank the attention ordering names -- the two
// states that rank with ACTIVE, and the one display word no table names --
// across two owners, so the owner key, the attention groups, the stale-first
// rule, the nil-Last rule and the name tiebreak are all on one input.
func sortFixtureRows() []BindingStatus {
	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	return []BindingStatus{
		{Name: "zeta", Display: "DONE", Owner: "b", OwnerLabel: "b", Last: &LastEvent{TS: t0}},
		{Name: "alpha", Display: "ACTIVE", Owner: "a", OwnerLabel: "a", Last: &LastEvent{TS: t0}},
		{Name: "mid-done", Display: "DONE", Owner: "a", OwnerLabel: "a", Last: &LastEvent{TS: t0}},
		{Name: "you-stale", Display: "NEEDS YOU", Owner: "a", OwnerLabel: "a", Stale: "stale 4h 0m", Last: &LastEvent{TS: t0.Add(-time.Hour)}},
		{Name: "you-fresh", Display: "NEEDS YOU", Owner: "a", OwnerLabel: "a", Last: &LastEvent{TS: t0}},
		{Name: "held", Display: "HALTED", Owner: "a", OwnerLabel: "a"},
		{Name: "stopped", Display: "STOPPED", Owner: "a", OwnerLabel: "a"},
		{Name: "paused", Display: "PAUSED", Owner: "a", OwnerLabel: "a"},
		{Name: "odd", Display: "QUEUED", Owner: "a", OwnerLabel: "a"},
		{Name: "last", Display: "ACTIVE", Owner: "b", OwnerLabel: "b"},
	}
}

// TestSortRowsOrderIsStableUnderShuffle pins the order the rules name, and pins
// it under input order: 50 shuffles land on the same order every time. A sort
// that is merely correct but not stable moves a row whenever the input moves
// it, and the fleet's own order would drift from one frame to the next.
func TestSortRowsOrderIsStableUnderShuffle(t *testing.T) {
	t.Parallel()

	want := map[bool][]string{
		true:  {"you-stale", "you-fresh", "alpha", "held", "stopped", "paused", "mid-done", "odd", "last", "zeta"},
		false: {"alpha", "held", "mid-done", "odd", "paused", "stopped", "you-fresh", "you-stale", "last", "zeta"},
	}
	rng := rand.New(rand.NewSource(1))
	for _, attention := range []bool{true, false} {
		for run := 0; run < 50; run++ {
			in := sortFixtureRows()
			rng.Shuffle(len(in), func(i, j int) { in[i], in[j] = in[j], in[i] })
			got := names(SortRows(in, attention))
			if !equalNames(got, want[attention]) {
				t.Fatalf("attention=%v run %d: got %v want %v", attention, run, got, want[attention])
			}
		}
	}
}

// TestSortRowsDoesNotMutateItsInput: the caller keeps its own slice, and the
// result is a fresh one -- the sort orders a permutation and never writes
// through to the rows it was handed.
func TestSortRowsDoesNotMutateItsInput(t *testing.T) {
	t.Parallel()

	for _, attention := range []bool{true, false} {
		in := sortFixtureRows()
		before := fmt.Sprint(in)
		out := SortRows(in, attention)
		if after := fmt.Sprint(in); after != before {
			t.Errorf("attention=%v: the input was mutated:\nbefore %s\nafter  %s", attention, before, after)
		}
		if len(out) == len(in) && &out[0] == &in[0] {
			t.Errorf("attention=%v: SortRows returned the caller's own slice", attention)
		}
	}
}
