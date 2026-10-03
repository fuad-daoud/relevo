package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// rateLimitedEntries is the ledger's live rate-limit entries after one
// /v1/unavailable post.
func rateLimitedEntries(t *testing.T, s *Server) []availability.Entry {
	t.Helper()
	l, err := availability.LoadLedger(s.gates)
	if err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	var out []availability.Entry
	for _, e := range l.Entries {
		if e.Kind == availability.RateLimited {
			out = append(out, e)
		}
	}
	return out
}

// postUnavailable sends one server-wide unavailable and returns the status.
func postUnavailable(t *testing.T, s *Server, kp remote.Keypair, reason string) int {
	t.Helper()
	body, _ := json.Marshal(remote.UnavailableRequest{Token: "claude/anthropic/haiku", Reason: reason})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, signedRequest(t, kp, "POST", "/v1/unavailable", body))
	return rec.Code
}

// TestUnavailableExpiresOnReasonReset pins the server-side half of the gate
// expiry rule: a forwarded reason that names its own reset ends the gate at
// that reset, rather than the hard-coded zero Until that gated the provider
// until someone cleared it by hand.
func TestUnavailableExpiresOnReasonReset(t *testing.T) {
	s, kpA := newAvailableServer(t)

	if code := postUnavailable(t, s, kpA, "RESOURCE_EXHAUSTED 429: rate limit reached. Resets in 51m30s"); code != http.StatusOK {
		t.Fatalf("unavailable status = %d, want 200", code)
	}

	entries := rateLimitedEntries(t, s)
	if len(entries) != 1 {
		t.Fatalf("rate-limit entries = %d, want 1: %+v", len(entries), entries)
	}
	if entries[0].Until.IsZero() {
		t.Fatal("a reason naming \"Resets in 51m30s\" must record a non-zero Until")
	}

	// The recorded Until is the reset the reason stated, not some fixed
	// default: it lies in the future and is well inside a day.
	until := entries[0].Until
	if !until.After(time.Now()) {
		t.Errorf("Until = %v, want a future reset", until)
	}
	if d := time.Until(until); d <= 0 || d > 2*time.Hour {
		t.Errorf("Until is %v away, want inside two hours for a 51m30s reset", d)
	}

	// And the ledger's own expiry prunes it on schedule.
	l, err := availability.LoadLedger(s.gates)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Prune(until.Add(time.Second)); len(got.Entries) != 0 {
		t.Errorf("entries after the reset = %d, want 0: %+v", len(got.Entries), got.Entries)
	}
}

// TestUnavailableWithoutParseableResetStaysUntilCleared is the control: a
// reason that states no reset leaves the gate with a zero Until, so it expires
// only when a human clears it.
func TestUnavailableWithoutParseableResetStaysUntilCleared(t *testing.T) {
	s, kpA := newAvailableServer(t)

	if code := postUnavailable(t, s, kpA, "RESOURCE_EXHAUSTED 429: rate limit reached"); code != http.StatusOK {
		t.Fatalf("unavailable status = %d, want 200", code)
	}

	entries := rateLimitedEntries(t, s)
	if len(entries) != 1 {
		t.Fatalf("rate-limit entries = %d, want 1: %+v", len(entries), entries)
	}
	if !entries[0].Until.IsZero() {
		t.Fatalf("Until = %v, want the zero time for a reason naming no reset", entries[0].Until)
	}

	// It survives any prune, however far in the future.
	l, err := availability.LoadLedger(s.gates)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Prune(time.Now().Add(10000 * time.Hour)); len(got.Entries) != 1 {
		t.Fatalf("a zero Until must never expire: %+v", got.Entries)
	}
}
