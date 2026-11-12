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
// row's place in line, and how long it has waited. A held round appends its
// wait word; an unstated or unknown hold keeps today's words byte-identical.
//
// Mutation: drop the QueueWaitText call and the memory rows read with no wait.
func TestQueueTextWithFacts(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	const gb = int64(1 << 30)
	for _, tc := range []struct {
		name  string
		facts store.QueueFacts
		want  string
	}{
		{"no wait", store.QueueFacts{Running: 2, Cap: 4, Ahead: 3, Since: now.Add(-5 * time.Minute)},
			"queued (2/4 busy on srv, 3 ahead, 5m)"},
		{"memory with bytes", store.QueueFacts{Running: 2, Cap: 4, Ahead: 3, Since: now.Add(-5 * time.Minute), Wait: "memory", WaitBytes: 2 * gb},
			"queued (2/4 busy on srv, 3 ahead, 5m), waiting on memory (needs 2.0 GiB more)"},
		{"memory without bytes", store.QueueFacts{Running: 2, Cap: 4, Ahead: 3, Since: now.Add(-5 * time.Minute), Wait: "memory"},
			"queued (2/4 busy on srv, 3 ahead, 5m), waiting on memory"},
		{"ceiling", store.QueueFacts{Running: 2, Cap: 4, Ahead: 3, Since: now.Add(-5 * time.Minute), Wait: "ceiling"},
			"queued (2/4 busy on srv, 3 ahead, 5m), waiting on ceiling"},
		{"pressure", store.QueueFacts{Running: 2, Cap: 4, Ahead: 3, Since: now.Add(-5 * time.Minute), Wait: "pressure"},
			"queued (2/4 busy on srv, 3 ahead, 5m), waiting on pressure"},
		{"unknown", store.QueueFacts{Running: 2, Cap: 4, Ahead: 3, Since: now.Add(-5 * time.Minute), Wait: "stale"},
			"queued (2/4 busy on srv, 3 ahead, 5m)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := QueueText(&tc.facts, "srv", now); got != tc.want {
				t.Errorf("QueueText(%+v) = %q, want %q", tc.facts, got, tc.want)
			}
		})
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

// TestQueueWaitTextRendersEveryHold pins the held reason a queued row names:
// the memory hold carries its shortfall in human bytes, a fairness skip with
// no measured bytes names only the hold, the ceiling and pressure holds name
// themselves, and an unstated or unknown reason reads as nothing.
func TestQueueWaitTextRendersEveryHold(t *testing.T) {
	t.Parallel()

	const gb = int64(1 << 30)
	for _, tc := range []struct {
		wait  string
		bytes int64
		want  string
	}{
		{"memory", 2 * gb, "waiting on memory (needs 2.0 GiB more)"},
		{"memory", 0, "waiting on memory"},
		{"memory", -1, "waiting on memory"},
		{"ceiling", 0, "waiting on ceiling"},
		{"pressure", 0, "waiting on pressure"},
		{"", 0, ""},
		{"stale", 3, ""},
	} {
		if got := QueueWaitText(tc.wait, tc.bytes); got != tc.want {
			t.Errorf("QueueWaitText(%q, %d) = %q, want %q", tc.wait, tc.bytes, got, tc.want)
		}
	}
}
