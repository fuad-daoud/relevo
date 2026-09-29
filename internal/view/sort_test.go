package view

import (
	"testing"
	"time"
)

func names(rows []BindingStatus) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

func equalNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAttentionRankPaused: PAUSED sorts after ACTIVE and before DONE.
func TestAttentionRankPaused(t *testing.T) {
	t.Parallel()

	if attentionRank["PAUSED"] <= attentionRank["ACTIVE"] {
		t.Errorf("PAUSED rank %d must follow ACTIVE rank %d", attentionRank["PAUSED"], attentionRank["ACTIVE"])
	}
	if attentionRank["PAUSED"] >= attentionRank["DONE"] {
		t.Errorf("PAUSED rank %d must precede DONE rank %d", attentionRank["PAUSED"], attentionRank["DONE"])
	}

	rows := []BindingStatus{
		{Name: "d", Display: "DONE"},
		{Name: "p", Display: "PAUSED"},
		{Name: "a", Display: "ACTIVE"},
	}
	got := names(SortRows(rows, true))
	want := []string{"a", "p", "d"}
	if !equalNames(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// TestSortRowsAttentionOrder: every display state, in every input order,
// lands NEEDS YOU, HELD, ACTIVE, PAUSED, DONE.
func TestSortRowsAttentionOrder(t *testing.T) {
	t.Parallel()

	rows := []BindingStatus{
		{Name: "d", Display: "DONE"},
		{Name: "a", Display: "ACTIVE"},
		{Name: "n", Display: "NEEDS YOU"},
		{Name: "h", Display: "HELD"},
	}
	want := []string{"n", "h", "a", "d"}
	// Rotate the input through every starting point: four permutations
	// that each begin with a different state.
	for shift := 0; shift < len(rows); shift++ {
		in := append(append([]BindingStatus{}, rows[shift:]...), rows[:shift]...)
		got := names(SortRows(in, true))
		if !equalNames(got, want) {
			t.Errorf("shift %d: got %v want %v", shift, got, want)
		}
	}
}

// TestSortRowsWithinGroup: newest Last.TS first, nil Last last, name as
// the tiebreak.
func TestSortRowsWithinGroup(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2026, 9, 17, 14, 0, 0, 0, time.UTC)
	rows := []BindingStatus{
		{Name: "old", Display: "ACTIVE", Last: &LastEvent{TS: t0.Add(-time.Hour)}},
		{Name: "none", Display: "ACTIVE"},
		{Name: "new", Display: "ACTIVE", Last: &LastEvent{TS: t0}},
		{Name: "tie-b", Display: "ACTIVE", Last: &LastEvent{TS: t0.Add(-2 * time.Hour)}},
		{Name: "tie-a", Display: "ACTIVE", Last: &LastEvent{TS: t0.Add(-2 * time.Hour)}},
	}
	got := names(SortRows(rows, true))
	want := []string{"new", "old", "tie-a", "tie-b", "none"}
	if !equalNames(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// TestSortRowsOwnerFirst: OwnerLabel is the primary key, before every
// today's comparison -- a client's cards stay contiguous under a header.
func TestSortRowsOwnerFirst(t *testing.T) {
	t.Parallel()

	rows := []BindingStatus{
		{Name: "zz", Owner: "b", OwnerLabel: "b", Display: "NEEDS YOU"},
		{Name: "aa", Owner: "a", OwnerLabel: "a", Display: "ACTIVE"},
		{Name: "zz", Owner: "a", OwnerLabel: "a", Display: "DONE"},
	}
	want := []string{"a/aa", "a/zz", "b/zz"}
	for _, attention := range []bool{false, true} {
		got := make([]string, 0, len(rows))
		for _, r := range SortRows(rows, attention) {
			got = append(got, r.Key())
		}
		if !equalNames(got, want) {
			t.Errorf("attention=%v: got %v want %v", attention, got, want)
		}
	}
}

// TestSortRowsOwnerEmptyLabelsPinsLegacyOrder: with every OwnerLabel empty
// (a mastermind) SortRows must return exactly what it always returned. The
// expected orders below are read from the existing tests' expectations, not
// from the new implementation.
func TestSortRowsOwnerEmptyLabelsPinsLegacyOrder(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2026, 9, 17, 14, 0, 0, 0, time.UTC)

	// TestSortRowsAttentionOrder's input and expectation.
	attention := []BindingStatus{
		{Name: "d", Display: "DONE"},
		{Name: "a", Display: "ACTIVE"},
		{Name: "n", Display: "NEEDS YOU"},
		{Name: "h", Display: "HELD"},
	}
	want := []string{"n", "h", "a", "d"}
	if got := names(SortRows(attention, true)); !equalNames(got, want) {
		t.Errorf("attention order: got %v want %v", got, want)
	}

	// TestSortRowsWithinGroup's input and expectation.
	within := []BindingStatus{
		{Name: "old", Display: "ACTIVE", Last: &LastEvent{TS: t0.Add(-time.Hour)}},
		{Name: "none", Display: "ACTIVE"},
		{Name: "new", Display: "ACTIVE", Last: &LastEvent{TS: t0}},
		{Name: "tie-b", Display: "ACTIVE", Last: &LastEvent{TS: t0.Add(-2 * time.Hour)}},
		{Name: "tie-a", Display: "ACTIVE", Last: &LastEvent{TS: t0.Add(-2 * time.Hour)}},
	}
	want = []string{"new", "old", "tie-a", "tie-b", "none"}
	if got := names(SortRows(within, true)); !equalNames(got, want) {
		t.Errorf("within-group order: got %v want %v", got, want)
	}

	// TestSortRowsNameOrder's input and expectation.
	plain := []BindingStatus{
		{Name: "b", Display: "DONE"},
		{Name: "a", Display: "NEEDS YOU"},
		{Name: "c", Display: "ACTIVE"},
	}
	want = []string{"a", "b", "c"}
	if got := names(SortRows(plain, false)); !equalNames(got, want) {
		t.Errorf("name order: got %v want %v", got, want)
	}
}

// TestSortRowsNameOrder: attention=false is plain name order regardless
// of state, and the input slice is untouched either way.
func TestSortRowsNameOrder(t *testing.T) {
	t.Parallel()

	rows := []BindingStatus{
		{Name: "b", Display: "DONE"},
		{Name: "a", Display: "NEEDS YOU"},
		{Name: "c", Display: "ACTIVE"},
	}
	before := names(rows)
	got := names(SortRows(rows, false))
	if !equalNames(got, []string{"a", "b", "c"}) {
		t.Errorf("name order: got %v", got)
	}
	SortRows(rows, true)
	if !equalNames(names(rows), before) {
		t.Errorf("input mutated: %v -> %v", before, names(rows))
	}
}

// TestSortStaleFirst pins the ordering rule: inside one attention group a
// stale row sorts before a fresh one, even when the fresh row has a newer
// Last.TS. attention=false still ignores the flag and orders by name.
func TestSortStaleFirst(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2026, 9, 17, 14, 0, 0, 0, time.UTC)
	rows := []BindingStatus{
		{Name: "fresh", Display: "NEEDS YOU", Last: &LastEvent{TS: t0}},
		{Name: "stale", Display: "NEEDS YOU", Stale: "stale 4h 0m", Last: &LastEvent{TS: t0.Add(-time.Hour)}},
		{Name: "held-stale", Display: "HELD", Stale: "stale 5h 0m", Last: &LastEvent{TS: t0.Add(-2 * time.Hour)}},
		{Name: "held", Display: "HELD", Last: &LastEvent{TS: t0.Add(-3 * time.Hour)}},
	}
	if got, want := names(SortRows(rows, true)), []string{"stale", "fresh", "held-stale", "held"}; !equalNames(got, want) {
		t.Errorf("attention order: got %v want %v", got, want)
	}
	if got, want := names(SortRows(rows, false)), []string{"fresh", "held", "held-stale", "stale"}; !equalNames(got, want) {
		t.Errorf("name order: got %v want %v", got, want)
	}
}
