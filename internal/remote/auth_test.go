package remote

import (
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

// testAudience is the audience every sign/verify test binds its requests to.
const testAudience = "host:test"

func setupAuthFixture(t *testing.T) (Keypair, KeyLookup, *NonceWindow) {
	t.Helper()
	kp, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	clientID := IDOf(kp.Public)
	lookup := func(id ClientID) (ed25519.PublicKey, KeyStatus) {
		if id == clientID {
			return kp.Public, KeyActive
		}
		return nil, KeyUnknown
	}
	nonces := NewNonceWindow(10 * time.Minute)
	return kp, lookup, nonces
}

func TestSignVerifyRoundTrip(t *testing.T) {
	kp, lookup, nonces := setupAuthFixture(t)
	now := time.Now()
	nonce, err := NewNonce()
	if err != nil {
		t.Fatalf("NewNonce: %v", err)
	}
	body := []byte("hello world")
	bodySum := sha256.Sum256(body)

	h := Sign(kp, testAudience, "POST", "/api/rounds", bodySum[:], now, nonce)
	id, err := Verify(h, "POST", "/api/rounds", bodySum[:], now, lookup, nonces, []string{testAudience})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id != IDOf(kp.Public) {
		t.Fatalf("Verify returned id %q, want %q", id, IDOf(kp.Public))
	}
}

func TestVerifyUnknownClient(t *testing.T) {
	kp, _, nonces := setupAuthFixture(t)
	emptyLookup := func(id ClientID) (ed25519.PublicKey, KeyStatus) {
		return nil, KeyUnknown
	}
	now := time.Now()
	nonce, _ := NewNonce()
	h := Sign(kp, testAudience, "GET", "/whoami", nil, now, nonce)

	_, err := Verify(h, "GET", "/whoami", nil, now, emptyLookup, nonces, []string{testAudience})
	if !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("Verify unknown client: got %v, want ErrUnknownClient", err)
	}

	// Missing HeaderClient
	h2 := h.Clone()
	h2.Del(HeaderClient)
	_, err = Verify(h2, "GET", "/whoami", nil, now, emptyLookup, nonces, []string{testAudience})
	if !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("Verify missing HeaderClient: got %v, want ErrUnknownClient", err)
	}
}

func TestVerifyRevoked(t *testing.T) {
	kp, _, nonces := setupAuthFixture(t)
	revokedLookup := func(id ClientID) (ed25519.PublicKey, KeyStatus) {
		return kp.Public, KeyRevoked
	}
	now := time.Now()
	nonce, _ := NewNonce()
	h := Sign(kp, testAudience, "GET", "/whoami", nil, now, nonce)

	_, err := Verify(h, "GET", "/whoami", nil, now, revokedLookup, nonces, []string{testAudience})
	if !errors.Is(err, ErrRevoked) {
		t.Fatalf("Verify revoked client: got %v, want ErrRevoked", err)
	}
}

func TestVerifyBodyTamper(t *testing.T) {
	kp, lookup, nonces := setupAuthFixture(t)
	now := time.Now()
	nonce, _ := NewNonce()
	body := []byte("real body")
	sum := sha256.Sum256(body)

	h := Sign(kp, testAudience, "POST", "/submit", sum[:], now, nonce)

	// Flip one byte of the body hash
	tamperedSum := sum
	tamperedSum[0] ^= 0xff

	_, err := Verify(h, "POST", "/submit", tamperedSum[:], now, lookup, nonces, []string{testAudience})
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify tampered body: got %v, want ErrBadSignature", err)
	}
}

func TestVerifyTimestampTamperIsBadSignature(t *testing.T) {
	kp, lookup, nonces := setupAuthFixture(t)
	now := time.Now()
	nonce, _ := NewNonce()
	h := Sign(kp, testAudience, "GET", "/test", nil, now, nonce)

	// Tamper with timestamp header
	h.Set(HeaderTimestamp, "123456789")

	_, err := Verify(h, "GET", "/test", nil, now, lookup, nonces, []string{testAudience})
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify tampered timestamp: got %v, want ErrBadSignature (not ErrStale)", err)
	}
}

func TestVerifyStaleClock(t *testing.T) {
	kp, lookup, nonces := setupAuthFixture(t)
	now := time.Now()

	// Signed with now - 6m -> ErrStale
	nonce1, _ := NewNonce()
	h1 := Sign(kp, testAudience, "GET", "/test", nil, now.Add(-6*time.Minute), nonce1)
	_, err := Verify(h1, "GET", "/test", nil, now, lookup, nonces, []string{testAudience})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("Verify now-6m: got %v, want ErrStale", err)
	}

	// Signed with now - 4m59s -> ok
	nonce2, _ := NewNonce()
	h2 := Sign(kp, testAudience, "GET", "/test", nil, now.Add(-4*time.Minute-59*time.Second), nonce2)
	_, err = Verify(h2, "GET", "/test", nil, now, lookup, nonces, []string{testAudience})
	if err != nil {
		t.Fatalf("Verify now-4m59s: %v", err)
	}
}

func TestVerifyReplay(t *testing.T) {
	kp, lookup, nonces := setupAuthFixture(t)
	now := time.Now()
	nonce, _ := NewNonce()
	h := Sign(kp, testAudience, "POST", "/action", nil, now, nonce)

	// First verify succeeds
	_, err := Verify(h, "POST", "/action", nil, now, lookup, nonces, []string{testAudience})
	if err != nil {
		t.Fatalf("first Verify: %v", err)
	}

	// Same headers twice -> second is ErrStale
	_, err = Verify(h, "POST", "/action", nil, now, lookup, nonces, []string{testAudience})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("second Verify (replay): got %v, want ErrStale", err)
	}
}

func TestVerifyReplayNotBurnedByBadSig(t *testing.T) {
	kp, lookup, nonces := setupAuthFixture(t)
	now := time.Now()
	nonce, _ := NewNonce()

	// Sign a request
	h := Sign(kp, testAudience, "POST", "/action", nil, now, nonce)

	// Make signature invalid
	badH := h.Clone()
	badH.Set(HeaderSignature, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	// Verify fails with bad signature
	_, err := Verify(badH, "POST", "/action", nil, now, lookup, nonces, []string{testAudience})
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify with bad signature: got %v, want ErrBadSignature", err)
	}

	// Now verify the valid request with the SAME nonce: must succeed!
	_, err = Verify(h, "POST", "/action", nil, now, lookup, nonces, []string{testAudience})
	if err != nil {
		t.Fatalf("Verify valid request after bad sig: %v", err)
	}
}

// TestVerifyMissingAudience pins the new refusal: a request whose
// Relevo-Audience header is absent predates audience-bound signatures.
func TestVerifyMissingAudience(t *testing.T) {
	kp, lookup, nonces := setupAuthFixture(t)
	now := time.Now()
	nonce, _ := NewNonce()
	h := Sign(kp, testAudience, "GET", "/whoami", nil, now, nonce)
	h.Del(HeaderAudience)

	_, err := Verify(h, "GET", "/whoami", nil, now, lookup, nonces, []string{testAudience})
	if !errors.Is(err, ErrNoAudience) {
		t.Fatalf("Verify missing audience: got %v, want ErrNoAudience", err)
	}
}

// TestVerifyWrongAudienceNotBurned pins that a request signed for another
// server is refused, and that the refusal never consumes the nonce: the same
// signed request still verifies once the audience matches.
func TestVerifyWrongAudienceNotBurned(t *testing.T) {
	kp, lookup, nonces := setupAuthFixture(t)
	now := time.Now()
	nonce, _ := NewNonce()
	h := Sign(kp, "host:other", "GET", "/test", nil, now, nonce)

	_, err := Verify(h, "GET", "/test", nil, now, lookup, nonces, []string{testAudience})
	if !errors.Is(err, ErrWrongAudience) {
		t.Fatalf("Verify wrong audience: got %v, want ErrWrongAudience", err)
	}

	_, err = Verify(h, "GET", "/test", nil, now, lookup, nonces, []string{"host:other"})
	if err != nil {
		t.Fatalf("Verify after wrong-audience refusal: %v (the nonce must not be burned)", err)
	}
}

// TestVerifyTamperedAudienceIsBadSignature pins the case the audience exists
// to stop: a request signed for A, with the header rewritten to B, where both
// A and B are accepted here. The rewrite breaks the signature.
func TestVerifyTamperedAudienceIsBadSignature(t *testing.T) {
	kp, lookup, nonces := setupAuthFixture(t)
	now := time.Now()
	nonce, _ := NewNonce()
	h := Sign(kp, testAudience, "GET", "/test", nil, now, nonce)
	h.Set(HeaderAudience, "host:other")

	_, err := Verify(h, "GET", "/test", nil, now, lookup, nonces, []string{testAudience, "host:other"})
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify rewritten audience: got %v, want ErrBadSignature", err)
	}
}

// TestVerifyEmptyAudiences pins that a server with no audience accepts
// nothing: even a well-formed request is refused.
func TestVerifyEmptyAudiences(t *testing.T) {
	kp, lookup, nonces := setupAuthFixture(t)
	now := time.Now()
	nonce, _ := NewNonce()
	h := Sign(kp, testAudience, "GET", "/test", nil, now, nonce)

	_, err := Verify(h, "GET", "/test", nil, now, lookup, nonces, nil)
	if !errors.Is(err, ErrWrongAudience) {
		t.Fatalf("Verify with no accepted audiences: got %v, want ErrWrongAudience", err)
	}
}

func TestNonceWindowPrunes(t *testing.T) {
	ttl := 100 * time.Millisecond
	window := NewNonceWindow(ttl)
	now := time.Now()
	nonce := "test-nonce-123"

	if window.Seen(nonce, now) {
		t.Fatal("first Seen returned true, want false")
	}
	if !window.Seen(nonce, now.Add(50*time.Millisecond)) {
		t.Fatal("second Seen before ttl returned false, want true")
	}

	// After ttl, nonce is unseen again
	if window.Seen(nonce, now.Add(150*time.Millisecond)) {
		t.Fatal("Seen after ttl returned true, want false (pruned)")
	}
}

func TestCanonicalIncludesAudience(t *testing.T) {
	a := Canonical("host:a", "GET", "/repo/bundle", "1000", "nonce", nil)
	b := Canonical("host:b", "GET", "/repo/bundle", "1000", "nonce", nil)
	if string(a) == string(b) {
		t.Fatalf("Canonical strings should differ with different audiences: %q vs %q", a, b)
	}
	if string(a[:len("host:a\n")]) != "host:a\n" {
		t.Fatalf("Canonical string should start with the audience line, got %q", a)
	}
}

func TestCanonicalIncludesQuery(t *testing.T) {
	t1 := Canonical(testAudience, "GET", "/repo/bundle?since=abc", "1000", "nonce", nil)
	t2 := Canonical(testAudience, "GET", "/repo/bundle?since=def", "1000", "nonce", nil)
	if string(t1) == string(t2) {
		t.Fatalf("Canonical strings should differ with different query parameters: %q vs %q", t1, t2)
	}

	t3 := Canonical(testAudience, "GET", "/repo/bundle", "1000", "nonce", nil)
	if string(t1) == string(t3) {
		t.Fatalf("Canonical string with query should differ from without query: %q vs %q", t1, t3)
	}
}
