package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"
)

const (
	HeaderClient = "Relevo-Client"

	HeaderTimestamp = "Relevo-Timestamp"

	HeaderNonce = "Relevo-Nonce"

	HeaderSignature = "Relevo-Signature"

	// It is also part of the signed string, so rewriting it breaks the signature.
	HeaderAudience = "Relevo-Audience"

	// A client that knows the scheme can tell an old server's refusal from a
	// wrong-audience one.
	HeaderAuthScheme = "Relevo-Auth"

	AuthSchemeAudience = "audience"
)

// MaxClockSkew defines the maximum allowed difference between request timestamp and server time.
const MaxClockSkew = 5 * time.Minute

// KeyStatus represents the status of a client key in the enrollment database.
type KeyStatus int

const (
	KeyUnknown KeyStatus = iota

	KeyActive

	KeyRevoked
)

// When status is KeyUnknown, pub is nil.
type KeyLookup func(id ClientID) (pub ed25519.PublicKey, status KeyStatus)

// Auth sentinels carrying exactly the wire messages named in the spec.
var (
	ErrUnknownClient = errors.New("unknown client")
	ErrRevoked       = errors.New("revoked")
	ErrBadSignature  = errors.New("bad signature")
	ErrStale         = errors.New("stale or replayed")
	ErrNoAudience    = errors.New("no audience")
	ErrWrongAudience = errors.New("wrong audience")
)

// Canonical returns the exact bytes to be signed for an HTTP request:
//
//	audience + "\n" + method + "\n" + target + "\n" + timestamp + "\n" + nonce + "\n" + hex(sha256(body))
//
// audience is the server identity the request is bound to. It is signed, so a
// signature made for one audience never verifies for another: the header cannot
// be rewritten to point the request at a different server.
//
// target is the request path plus "?" + RawQuery when the query is non-empty,
// exactly as it appears on the HTTP request line (including query parameters such
// as ?since=<sha> on the bundle endpoint, so that query parameters are protected by
// the signature).
func Canonical(audience, method, target, timestamp, nonce string, bodySHA256 []byte) []byte {
	var bodyHex string
	if len(bodySHA256) == 0 {
		sum := sha256.Sum256(nil)
		bodyHex = hex.EncodeToString(sum[:])
	} else {
		bodyHex = hex.EncodeToString(bodySHA256)
	}

	canon := audience + "\n" + method + "\n" + target + "\n" + timestamp + "\n" + nonce + "\n" + bodyHex
	return []byte(canon)
}

func NewNonce() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf[:]), nil
}

func Sign(k Keypair, audience, method, target string, bodySHA256 []byte, now time.Time, nonce string) http.Header {
	ts := strconv.FormatInt(now.Unix(), 10)
	canon := Canonical(audience, method, target, ts, nonce, bodySHA256)
	sig := ed25519.Sign(k.Private, canon)

	h := make(http.Header)
	h.Set(HeaderAudience, audience)
	h.Set(HeaderClient, string(IDOf(k.Public)))
	h.Set(HeaderTimestamp, ts)
	h.Set(HeaderNonce, nonce)
	h.Set(HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	return h
}

// NonceWindow prevents replay attacks.
type NonceWindow struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]time.Time
}

func NewNonceWindow(ttl time.Duration) *NonceWindow {
	return &NonceWindow{
		ttl:     ttl,
		entries: make(map[string]time.Time),
	}
}

// Seen is safe for concurrent use.
func (w *NonceWindow) Seen(nonce string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Prune expired entries on every call
	for k, exp := range w.entries {
		if !exp.After(now) {
			delete(w.entries, k)
		}
	}

	if exp, ok := w.entries[nonce]; ok && exp.After(now) {
		return true
	}

	w.entries[nonce] = now.Add(w.ttl)
	return false
}

// audiences is the set of server identities this server accepts. An empty set
// accepts nothing: a server that names no audience cannot tell which of its own
// identities a request was signed for, so it refuses rather than guessing.
// The audience checks sit before the signature and touch no state, so a refusal
// for the wrong audience never consumes the request's nonce.
func Verify(h http.Header, method, target string, bodySHA256 []byte, now time.Time, lookup KeyLookup, nonces *NonceWindow, audiences []string) (ClientID, error) {
	id := ClientID(h.Get(HeaderClient))
	if id == "" {
		return "", ErrUnknownClient
	}

	pub, status := lookup(id)
	switch status {
	case KeyUnknown:
		return "", ErrUnknownClient
	case KeyRevoked:
		return "", ErrRevoked
	case KeyActive:
		// continue
	default:
		return "", ErrUnknownClient
	}
	if len(pub) != ed25519.PublicKeySize {
		return "", ErrUnknownClient
	}

	audience := h.Get(HeaderAudience)
	if audience == "" {
		return "", ErrNoAudience
	}

	if !slices.Contains(audiences, audience) {
		return "", ErrWrongAudience
	}

	sigStr := h.Get(HeaderSignature)
	sig, err := base64.StdEncoding.DecodeString(sigStr)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return "", ErrBadSignature
	}

	canon := Canonical(audience, method, target, h.Get(HeaderTimestamp), h.Get(HeaderNonce), bodySHA256)
	if !ed25519.Verify(pub, canon, sig) {
		return "", ErrBadSignature
	}

	tsStr := h.Get(HeaderTimestamp)
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", ErrStale
	}
	diff := now.Unix() - ts
	if diff < 0 {
		diff = -diff
	}
	if time.Duration(diff)*time.Second > MaxClockSkew {
		return "", ErrStale
	}

	if nonces != nil && nonces.Seen(h.Get(HeaderNonce), now) {
		return "", ErrStale
	}

	return id, nil
}
