package view

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestQueueTextWithoutFacts pins the bare word a queued row shows before the
// server has reported any facts.
func TestQueueTextWithoutFacts(t *testing.T) {
	t.Parallel()

	if got, want := QueueText(nil, "server", time.Unix(0, 0)), "queued"; got != want {
		t.Errorf("QueueText(nil) = %q, want %q", got, want)
	}
}

// TestQueueTextWithFacts pins the full queued picture: the server's load, the
// row's place in line, and how long it has waited.
func TestQueueTextWithFacts(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	q := &store.QueueFacts{Running: 2, Cap: 4, Ahead: 3, Since: now.Add(-5 * time.Minute)}
	if got, want := QueueText(q, "srv", now), "queued (2/4 busy on srv, 3 ahead, 5m)"; got != want {
		t.Errorf("QueueText(%+v) = %q, want %q", q, got, want)
	}
}

// TestIsPayloadKind pins the four kinds that cross between mastermind and
// builder, the legacy prompt spelling included, against relevo's own kinds.
func TestIsPayloadKind(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		kind store.Kind
		want bool
	}{
		{store.KindPrompt, true},
		{store.Kind("plan"), true},
		{store.KindReport, true},
		{store.KindQuestion, true},
		{store.KindAnswer, true},
		{store.KindDiff, false},
		{store.KindSwitch, false},
		{store.Kind(""), false},
	} {
		if got := IsPayloadKind(tc.kind); got != tc.want {
			t.Errorf("IsPayloadKind(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// TestDisplayStateCollapsesEveryStoredState pins the human words: the two
// stalled states read NEEDS YOU, paused and done are their own, and anything
// else is ACTIVE.
func TestDisplayStateCollapsesEveryStoredState(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		state store.State
		want  string
	}{
		{store.StateActive, "ACTIVE"},
		{store.StateNeedsYou, "NEEDS YOU"},
		{store.StateBroken, "NEEDS YOU"},
		{store.StatePaused, "PAUSED"},
		{store.StateDone, "DONE"},
		{store.State(""), "ACTIVE"},
	} {
		if got := DisplayState(tc.state); got != tc.want {
			t.Errorf("DisplayState(%q) = %q, want %q", tc.state, got, tc.want)
		}
	}
}
