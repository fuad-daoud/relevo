package syncworker

import (
	"errors"
	"testing"
	"time"
)

// TestClaimIdentityKeepsTheFirstLiveClaim pins the worker's compare-and-set: a
// claim stands against another token for a day, the same token refreshes it, and
// a read with no token writes nothing.
func TestClaimIdentityKeepsTheFirstLiveClaim(t *testing.T) {
	b := backendFor(t, &localDriver{path: tempReplica(t)}, "origin-a")
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

	won, _, err := b.ClaimIdentity("inst-x", "token-a", start)
	if err != nil || won != "token-a" {
		t.Fatalf("first claim = %q, %v, want its own token", won, err)
	}
	held, _, err := b.ClaimIdentity("inst-x", "token-b", start.Add(time.Hour))
	if err != nil || held != "token-a" {
		t.Fatalf("a second token within the day = %q, %v, want the first claim to stand", held, err)
	}
	read, at, err := b.ClaimIdentity("inst-x", "", start.Add(2*time.Hour))
	if err != nil || read != "token-a" || !at.Equal(start) {
		t.Fatalf("a read = %q at %v, %v, want token-a at %v and no write", read, at, err, start)
	}
	stale, _, err := b.ClaimIdentity("inst-x", "token-b", start.Add(25*time.Hour))
	if err != nil || stale != "token-b" {
		t.Fatalf("a claim over a stale one = %q, %v, want the new token", stale, err)
	}
}

// TestClaimIdentityVerbRefusesNoOrigin pins that a claim on no identity is
// refused at the pipe, and that a read of an unclaimed identity answers empty.
func TestClaimIdentityVerbRefusesNoOrigin(t *testing.T) {
	s := startWorker(t, backendFor(t, &localDriver{path: tempReplica(t)}, "origin-a"))
	if resp := s.ask(helloRequest("h")); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}
	if resp := s.ask(Request{ID: "c1", Verb: VerbClaim, ClaimToken: "t"}); resp.OK {
		t.Error("claim_identity with no origin came back as a success")
	}
	resp := s.ask(Request{ID: "c2", Verb: VerbClaim, Origin: "inst-x"})
	if !resp.OK || resp.ClaimToken != "" {
		t.Errorf("claim_identity read of an unclaimed identity = %+v, want an empty claim", resp)
	}
	now := time.Now()
	resp = s.ask(Request{ID: "c3", Verb: VerbClaim, Origin: "inst-x", ClaimToken: "t", At: &now})
	if !resp.OK || resp.ClaimToken != "t" || resp.ClaimAt == nil {
		t.Errorf("claim_identity = %+v, want the token and its time", resp)
	}
}

// TestClaimIdentityRefusesATransferThatFails pins the pull the claim is decided
// against and the push that lands it: a refusal on either is the caller's error,
// and a claim whose push refused is not reported as held.
func TestClaimIdentityRefusesATransferThatFails(t *testing.T) {
	driver := &localDriver{path: tempReplica(t), pullErr: errors.New("the pull does not answer")}
	b := backendFor(t, driver, "origin-a")
	now := time.Now()
	if _, _, err := b.ClaimIdentity("inst-x", "t", now); err == nil {
		t.Error("a claim over a pull that refused came back as a success")
	}
	driver.pullErr = nil
	driver.pushErr = errors.New("the push does not answer")
	if _, _, err := b.ClaimIdentity("inst-x", "t", now); err == nil {
		t.Error("a claim landed by a push that refused came back as a success")
	}
}

func TestParseClaimReadsAnUnparseableRowAsAHeldClaim(t *testing.T) {
	token, at := parseClaim("tok not-a-time")
	if token != "tok" || !at.IsZero() {
		t.Errorf("parseClaim = %q at %v, want the token held since the zero time", token, at)
	}
}
