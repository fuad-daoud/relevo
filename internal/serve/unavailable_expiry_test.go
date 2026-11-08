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
	return postUnavailableReq(t, s, kp, remote.UnavailableRequest{Token: "claude/anthropic/haiku", Reason: reason})
}

// postUnavailableReq sends one server-wide unavailable request and returns
// the status.
func postUnavailableReq(t *testing.T, s *Server, kp remote.Keypair, req remote.UnavailableRequest) int {
	t.Helper()
	body, _ := json.Marshal(req)
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

// TestUnavailableWithoutParseableResetTakesThePolicyDefault is the control: a
// reason that states no reset takes the serve policy's limit-gate default
// instead of holding until cleared, so an old head's zero-Until forward of
// an unparseable limit still bounds at this node.
func TestUnavailableWithoutParseableResetTakesThePolicyDefault(t *testing.T) {
	s, kpA := newAvailableServer(t)

	before := time.Now()
	if code := postUnavailable(t, s, kpA, "RESOURCE_EXHAUSTED 429: rate limit reached"); code != http.StatusOK {
		t.Fatalf("unavailable status = %d, want 200", code)
	}

	entries := rateLimitedEntries(t, s)
	if len(entries) != 1 {
		t.Fatalf("rate-limit entries = %d, want 1: %+v", len(entries), entries)
	}
	if entries[0].Until.IsZero() {
		t.Fatal("a reason naming no reset must record a finite Until, not until cleared")
	}

	// The recorded Until is the policy default measured from the post, not a
	// reset the reason stated.
	until := entries[0].Until
	if until.Before(before.Add(time.Hour)) || until.After(time.Now().Add(time.Hour)) {
		t.Errorf("Until = %v, want about an hour out for the default limit gate", until)
	}
}

// TestUnavailableExplicitUntilBeatsReasonReset pins the precedence: the
// expiry the wire carried wins over a conflicting reset the reason names.
func TestUnavailableExplicitUntilBeatsReasonReset(t *testing.T) {
	s, kpA := newAvailableServer(t)

	explicit := time.Now().Add(2 * time.Hour).UTC()
	req := remote.UnavailableRequest{
		Token:  "claude/anthropic/haiku",
		Reason: "RESOURCE_EXHAUSTED 429: rate limit reached. Resets in 51m30s",
		Until:  explicit,
	}
	if code := postUnavailableReq(t, s, kpA, req); code != http.StatusOK {
		t.Fatalf("unavailable status = %d, want 200", code)
	}

	entries := rateLimitedEntries(t, s)
	if len(entries) != 1 {
		t.Fatalf("rate-limit entries = %d, want 1: %+v", len(entries), entries)
	}
	if !entries[0].Until.Equal(explicit) {
		t.Fatalf("Until = %v, want the wire's %v, not the reason's reset", entries[0].Until, explicit)
	}
}

// TestUnavailableCreditReasonStaysUntilCleared pins the class exception: a
// credit-classed reason holds the zero Until however it arrived, because no
// top-up arrives on a clock.
func TestUnavailableCreditReasonStaysUntilCleared(t *testing.T) {
	s, kpA := newAvailableServer(t)

	if code := postUnavailable(t, s, kpA, "run failed: insufficient credits to complete the request"); code != http.StatusOK {
		t.Fatalf("unavailable status = %d, want 200", code)
	}

	entries := rateLimitedEntries(t, s)
	if len(entries) != 1 {
		t.Fatalf("rate-limit entries = %d, want 1: %+v", len(entries), entries)
	}
	if !entries[0].Until.IsZero() {
		t.Fatalf("Until = %v, want the zero time for a credit-classed reason", entries[0].Until)
	}
}

// TestUnavailableDoublePostRecordsOneGate pins the idempotent receive: two
// posts of the same gate record one ledger entry, so a resend after a head
// restart is a no-op on the node.
func TestUnavailableDoublePostRecordsOneGate(t *testing.T) {
	s, kpA := newAvailableServer(t)

	req := remote.UnavailableRequest{
		Token:  "claude/anthropic/haiku",
		Reason: "RESOURCE_EXHAUSTED 429: rate limit reached. Resets in 51m30s",
		Until:  time.Now().Add(2 * time.Hour).UTC(),
	}
	if code := postUnavailableReq(t, s, kpA, req); code != http.StatusOK {
		t.Fatalf("first unavailable status = %d, want 200", code)
	}
	if code := postUnavailableReq(t, s, kpA, req); code != http.StatusOK {
		t.Fatalf("second unavailable status = %d, want 200", code)
	}

	if entries := rateLimitedEntries(t, s); len(entries) != 1 {
		t.Fatalf("rate-limit entries = %d, want 1 after the same gate twice: %+v", len(entries), entries)
	}
}

// TestUnavailableRegateNewUntilWritesAgain pins the other half of the
// idempotency key: the same reason with a new expiry is a re-gate, not a
// re-heard gate, so it writes a second entry.
func TestUnavailableRegateNewUntilWritesAgain(t *testing.T) {
	s, kpA := newAvailableServer(t)

	base := time.Now().Add(2 * time.Hour).UTC()
	first := remote.UnavailableRequest{
		Token:  "claude/anthropic/haiku",
		Reason: "RESOURCE_EXHAUSTED 429: rate limit reached. Resets in 51m30s",
		Until:  base,
	}
	if code := postUnavailableReq(t, s, kpA, first); code != http.StatusOK {
		t.Fatalf("first unavailable status = %d, want 200", code)
	}
	second := first
	second.Until = base.Add(time.Hour)
	if code := postUnavailableReq(t, s, kpA, second); code != http.StatusOK {
		t.Fatalf("second unavailable status = %d, want 200", code)
	}

	if entries := rateLimitedEntries(t, s); len(entries) != 2 {
		t.Fatalf("rate-limit entries = %d, want 2 after a re-gate with a new expiry: %+v", len(entries), entries)
	}
}
