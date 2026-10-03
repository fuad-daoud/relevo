package main

import (
	"strings"
	"testing"
	"time"
)

// TestGateUntil pins the manual-gate expiry rule: with no --for, a --reason
// that names its own reset decides when the gate expires, by the same limit-text
// parse the decision point applies to builder output. A reason naming no reset
// the parser trusts stays until cleared, because nothing in it states when the
// limit lifts.
func TestGateUntil(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	for _, c := range []struct {
		name     string
		forFlag  string
		reason   string
		want     time.Time
		wantZero bool
		wantErr  bool
	}{
		{
			name:    "reason names a minutes reset",
			reason:  "RESOURCE_EXHAUSTED 429: rate limit reached. Resets in 51m30s",
			want:    now.Add(51*time.Minute + 30*time.Second),
			wantErr: false,
		},
		{
			name:    "reason names an hours reset",
			reason:  "RESOURCE_EXHAUSTED 429: rate limit reached. Resets in 4h34m37s",
			want:    now.Add(4*time.Hour + 34*time.Minute + 37*time.Second),
			wantErr: false,
		},
		{
			// "resets at 7pm" is the clock form parseReset accepts. The
			// wording "try again at <time>" is NOT one it accepts: clockRe
			// requires the word "resets", so that phrasing still stays until
			// cleared. Recorded here so the limit parser's real reach is the
			// pinned behaviour rather than a surprise.
			name:    "reason names a clock reset",
			reason:  "rate limit reached. Resets at 7pm",
			wantErr: false,
		},
		{
			name:     "try again at a clock time is outside the parser's reach",
			reason:   "try again at 7pm",
			wantZero: true,
		},
		{
			name:     "reason names no reset",
			reason:   "RESOURCE_EXHAUSTED 429: rate limit reached",
			wantZero: true,
		},
		{
			name:     "empty reason",
			reason:   "",
			wantZero: true,
		},
		{
			name:     "a reset beyond the trusted window is not honoured",
			reason:   "rate limit reached. Resets in 200h",
			wantZero: true,
		},
		{
			// An explicit --for is the caller's own statement of the expiry and
			// always wins: the reason is a note beside it, never a second voice.
			name:    "explicit --for wins over the reason",
			forFlag: "130h",
			reason:  "rate limit reached. Resets in 51m30s",
			want:    now.Add(130 * time.Hour),
		},
		{
			name:    "unparseable --for is a usage error",
			forFlag: "soon",
			wantErr: true,
		},
		{
			name:    "non-positive --for is a usage error",
			forFlag: "0s",
			wantErr: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got, err := gateUntil(c.forFlag, c.reason, now)
			if c.wantErr {
				if err == nil {
					t.Fatalf("gateUntil(%q, %q) = %v, want an error", c.forFlag, c.reason, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("gateUntil(%q, %q) = %v", c.forFlag, c.reason, err)
			}
			if c.wantZero {
				if !got.IsZero() {
					t.Fatalf("Until = %v, want the until-cleared zero time", got)
				}
				return
			}
			if c.want.IsZero() {
				// Only "must parse" was asserted.
				if got.IsZero() {
					t.Fatalf("gateUntil(%q, %q) parsed nothing, want a reset", c.forFlag, c.reason)
				}
				if !got.After(now) {
					t.Fatalf("Until = %v, want a future reset", got)
				}
				return
			}
			if !got.Equal(c.want) {
				t.Errorf("Until = %v, want %v", got, c.want)
			}
		})
	}
}

// TestGateUntilUsesTheLimitParser pins that the manual path reuses the limit
// parse rather than a private one: a wording the daemon's parser accepts is a
// wording the manual gate honours too.
func TestGateUntilUsesTheLimitParser(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, reason := range []string{
		"RESOURCE_EXHAUSTED 429: ... Resets in 51m30s",
		"resets in 4h34m37s",
		"try again in 5 min",
	} {
		got, err := gateUntil("", reason, now)
		if err != nil {
			t.Fatalf("gateUntil(%q) = %v", reason, err)
		}
		if got.IsZero() {
			t.Errorf("gateUntil(%q) parsed nothing; the limit parser accepts this wording", reason)
		}
	}
}

// TestParseForStillHoldsTheUntilClearedDefault keeps the pre-existing --for
// contract intact for callers that pass no reason at all.
func TestParseForStillHoldsTheUntilClearedDefault(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	got, err := parseFor("", now)
	if err != nil || !got.IsZero() {
		t.Fatalf("parseFor(\"\") = %v, %v; want the zero time and no error", got, err)
	}
	if _, err := parseFor("nope", now); err == nil || !strings.Contains(err.Error(), "--for") {
		t.Fatalf("parseFor(%q) = %v, want an error naming --for", "nope", err)
	}
}
