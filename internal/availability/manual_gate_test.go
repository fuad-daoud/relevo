package availability

import (
	"testing"
	"time"
)

// TestManualGateUntil pins each arm of the hand-recorded gate expiry: an
// explicit --for wins, a credit-classed reason stays until cleared, a reason
// naming a trusted reset expires at it, and a limit reason naming no reset
// falls back to the policy default instead of gating until cleared.
func TestManualGateUntil(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	fallback := time.Hour
	explicit := now.Add(2 * time.Hour)

	cases := []struct {
		name     string
		reason   string
		forUntil time.Time
		want     time.Time
	}{
		{
			name:   "limit reason without a reset takes the fallback",
			reason: "API error 429 (FreeUsageLimitError): Free usage limit reached",
			want:   now.Add(fallback),
		},
		{
			name:   "credit reason stays until cleared",
			reason: "run failed: insufficient credits to complete the request",
			want:   time.Time{},
		},
		{
			name:   "reason reset wins over the fallback",
			reason: "RESOURCE_EXHAUSTED 429: rate limit reached. Resets in 51m30s",
			want:   now.Add(51*time.Minute + 30*time.Second),
		},
		{
			name:     "explicit for wins over a conflicting reason reset",
			reason:   "RESOURCE_EXHAUSTED 429: rate limit reached. Resets in 51m30s",
			forUntil: explicit,
			want:     explicit,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := ManualGateUntil(c.reason, c.forUntil, now, fallback); !got.Equal(c.want) {
				t.Fatalf("ManualGateUntil(%q) = %v, want %v", c.reason, got, c.want)
			}
		})
	}
}
