package synclog

import (
	"context"
	"testing"
	"time"
)

// TestClaimIdentityMatchesTheRemotesRule pins the fake against the worker's
// compare-and-set: the first live claim stands, the same token refreshes it, an
// empty token only reads, and a claim older than ClaimTTL gives way.
func TestClaimIdentityMatchesTheRemotesRule(t *testing.T) {
	a := NewMemTransport("a")
	b := a.OnLog("b")
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	ctx := context.Background()

	if won, _, err := a.ClaimIdentity(ctx, "x", "ta", start); err != nil || won != "ta" {
		t.Fatalf("first claim = %q, %v", won, err)
	}
	if won, _, _ := b.ClaimIdentity(ctx, "x", "tb", start.Add(time.Hour)); won != "ta" {
		t.Errorf("a second token within the day won: %q", won)
	}
	if won, _, _ := b.ClaimIdentity(ctx, "x", "", start.Add(time.Hour)); won != "ta" {
		t.Errorf("a read returned %q, want ta", won)
	}
	if won, _, _ := b.ClaimIdentity(ctx, "y", "", start); won != "" {
		t.Errorf("a read of an unclaimed identity returned %q", won)
	}
	if won, _, _ := b.ClaimIdentity(ctx, "x", "tb", start.Add(ClaimTTL+time.Minute)); won != "tb" {
		t.Errorf("a claim over a stale one = %q, want tb", won)
	}
	if _, _, err := a.ClaimIdentity(ctx, "", "t", start); err == nil {
		t.Error("a claim on no identity was accepted")
	}
}

// TestOverwriteClaimLaysDownADoubleWin pins the test seam: a claim another
// machine's push replaced reads as that machine's, and Claims names the
// identities that hold one.
func TestOverwriteClaimLaysDownADoubleWin(t *testing.T) {
	a := NewMemTransport("a")
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if won, _, _ := a.ClaimIdentity(context.Background(), "x", "ta", start); won != "ta" {
		t.Fatalf("claim = %q", won)
	}
	a.OverwriteClaim("x", "tb", start.Add(time.Minute))
	if won, _, _ := a.ClaimIdentity(context.Background(), "x", "", start); won != "tb" {
		t.Errorf("read after the overwrite = %q, want tb", won)
	}
	if claims := a.Claims(); len(claims) != 1 || claims[0] != "x" {
		t.Errorf("Claims = %v, want [x]", claims)
	}
}
