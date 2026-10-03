package availability

import (
	"testing"
	"time"
)

// TestResetFromReason: the same parse the decision point applies to builder
// output decides when a manually recorded gate expires. A reason naming its own
// reset yields that instant; a reason naming none -- or one outside the window
// a vaguer form may claim -- leaves the gate until cleared rather than
// inventing an expiry no provider stated.
func TestResetFromReason(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	for _, c := range []struct {
		name     string
		reason   string
		want     time.Time
		wantOK   bool
		wantNote string
	}{
		{
			name:   "429 with a minutes reset",
			reason: "RESOURCE_EXHAUSTED 429: rate limit reached. Resets in 51m30s",
			want:   now.Add(51*time.Minute + 30*time.Second),
			wantOK: true,
		},
		{
			name:   "429 with an hours reset",
			reason: "RESOURCE_EXHAUSTED 429: rate limit reached. Resets in 4h34m37s",
			want:   now.Add(4*time.Hour + 34*time.Minute + 37*time.Second),
			wantOK: true,
		},
		{
			name:   "lowercase reset",
			reason: "usage limit reached, resets in 2h",
			want:   now.Add(2 * time.Hour),
			wantOK: true,
		},
		{
			name:   "no parseable reset stays until cleared",
			reason: "RESOURCE_EXHAUSTED 429: rate limit reached",
		},
		{
			name:   "a reset beyond the trusted window is not honoured",
			reason: "rate limit reached. Resets in 200h",
		},
		{
			name:   "a nonsense reset is not honoured",
			reason: "rate limit reached. Resets in 90000000h",
		},
		{
			name:   "empty reason",
			reason: "",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got, ok := ResetFromReason(c.reason, now)
			if ok != c.wantOK {
				t.Fatalf("ResetFromReason ok = %v, want %v (got %v)", ok, c.wantOK, got)
			}
			if !ok {
				// A refused parse must leave the caller with the until-cleared
				// zero time, never a partial one.
				if !got.IsZero() {
					t.Errorf("refused parse returned %v, want the zero time", got)
				}
				return
			}
			if !got.Equal(c.want) {
				t.Errorf("Until = %v, want %v", got, c.want)
			}
		})
	}
}

// TestResetFromReasonExpiresLedger: a gate recorded with a parsed Until is
// pruned by the ledger's own expiry on schedule, while one with no parseable
// reset survives every prune until it is cleared by hand.
func TestResetFromReasonExpiresLedger(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	until, ok := ResetFromReason("RESOURCE_EXHAUSTED 429: ... Resets in 51m30s", now)
	if !ok {
		t.Fatal("a Resets-in reason must parse")
	}

	l := Ledger{Entries: []Entry{
		{Kind: RateLimited, Subject: "opencode", At: now, Until: until, Source: "planner"},
		{Kind: RateLimited, Subject: "claude", At: now, Source: "planner"},
	}}

	// Just before the reset the parsed gate is still live.
	if got := l.Prune(now.Add(50 * time.Minute)); len(got.Entries) != 2 {
		t.Fatalf("entries before the reset = %d, want 2: %+v", len(got.Entries), got.Entries)
	}
	// After it, only the reason with no reset is left.
	got := l.Prune(now.Add(52 * time.Minute))
	if len(got.Entries) != 1 {
		t.Fatalf("entries after the reset = %d, want 1: %+v", len(got.Entries), got.Entries)
	}
	if got.Entries[0].Subject != "claude" {
		t.Fatalf("survivor = %q, want the unparseable-reason gate", got.Entries[0].Subject)
	}
	// And it stays until cleared, however long the ledger is pruned.
	if still := got.Prune(now.Add(1000 * time.Hour)); len(still.Entries) != 1 {
		t.Fatalf("a zero Until must never expire: %+v", still.Entries)
	}
}
