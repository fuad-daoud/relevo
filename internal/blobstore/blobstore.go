// Package blobstore holds the bodies sync moves outside the log: one object per
// stored value, addressed by its origin and digest.
package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound reports that an object is not in the store, rather than any other
// error: a missing body and a body whose fetch failed call for opposite handling,
// the first latching the origin and naming the round, the second retried.
var ErrNotFound = errors.New("blobstore: object not found")

// Object is one stored body as a listing describes it.
type Object struct {
	Key      string
	Size     int64
	Modified time.Time
}

// BlobStore is where the bodies the log only points at live. Bodies never cross
// the JSON pipe: an entry names a digest and the bytes arrive out of band. Every
// operation is keyed, and none reads another origin's prefix.
type BlobStore interface {
	// Put stores body under key. size is the length the caller expects to write;
	// a body of a different length is refused rather than stored under a digest
	// that no longer describes it.
	Put(ctx context.Context, key string, body io.Reader, size int64) error
	// Get writes the body under key to w and returns how many bytes it wrote,
	// or ErrNotFound when the key is not there.
	Get(ctx context.Context, key string, w io.Writer) (int64, error)
	// Has reports whether key is in the store, without its body.
	Has(ctx context.Context, key string) (bool, error)
	// List returns every object under prefix, in key order.
	List(ctx context.Context, prefix string) ([]Object, error)
	// Delete removes key, already absent being no error.
	Delete(ctx context.Context, key string) error
}

// ErrBadOrigin reports an origin that cannot be the first segment of a key.
var ErrBadOrigin = errors.New("blobstore: origin is not an installation id")

// maxOriginLen is far above an installation id's 26 characters; it only stops a
// remote entry from making a key arbitrarily long.
const maxOriginLen = 64

// CheckOrigin refuses an origin that is not letters, digits, '-' and '_'. An
// origin arriving in a remote log entry is not trusted: a '/', '.', '?', '#' or
// '%' in it would address another key, or reshape the signed request URL the key
// is put into.
func CheckOrigin(origin string) error {
	if origin == "" || len(origin) > maxOriginLen {
		return fmt.Errorf("%w: %d characters, want 1 to %d", ErrBadOrigin, len(origin), maxOriginLen)
	}
	for i := 0; i < len(origin); i++ {
		c := origin[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return fmt.Errorf("%w: it holds %q", ErrBadOrigin, c)
		}
	}
	return nil
}

// Key returns the object key for one value from one origin. Both halves are
// checked: a key is a path, and an origin holding a separator would put one
// machine's objects under another's prefix, where the weekly cleanup would delete
// them. Callers check a remote origin with CheckOrigin first, so a failure here is
// a bug, and the call stops instead of addressing the wrong object.
func Key(origin, sha256hex string) string {
	if CheckOrigin(origin) != nil {
		panic("blobstore: Key: origin must be letters, digits, '-' or '_'")
	}
	if len(sha256hex) != 64 || !isLowerHex(sha256hex) {
		panic("blobstore: Key: digest must be 64 lowercase hex characters")
	}
	return origin + "/" + sha256hex
}

// sizeName is the last segment of an origin's published log size. It is not 64
// hex characters, so no body key can equal it.
const sizeName = "log-size.json"

func SizeKey(origin string) string {
	if CheckOrigin(origin) != nil {
		panic("blobstore: SizeKey: origin must be letters, digits, '-' or '_'")
	}
	return origin + "/" + sizeName
}

// SizeOrigin returns the origin a size key belongs to, and whether key is one.
func SizeOrigin(key string) (string, bool) {
	origin, name, ok := strings.Cut(key, "/")
	if !ok || name != sizeName || CheckOrigin(origin) != nil {
		return "", false
	}
	return origin, true
}

// IsBodyKey reports whether key addresses a body: an origin, then 64 lowercase
// hex characters. A bucket listing counts only these as bodies.
func IsBodyKey(key string) bool {
	origin, digest, ok := strings.Cut(key, "/")
	return ok && CheckOrigin(origin) == nil && len(digest) == 64 && isLowerHex(digest)
}

// isLowerHex reports whether s is 64 characters of 0-9a-f. Uppercase is refused
// rather than folded: the key's digest must equal the string the importer
// computes from the fetched bytes, and matching one spelling only would make a
// present object look absent.
func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ErrInsecureEndpoint reports an endpoint a signed request would cross in the
// clear.
var ErrInsecureEndpoint = errors.New("blobstore: the endpoint is not https")

// CheckEndpoint refuses an endpoint that is not an https URL with a host. Plain
// http is allowed only on loopback, where a local fake store runs: anywhere else
// the key id and a replayable signature would cross the network in the clear,
// and the bodies would arrive unauthenticated.
func CheckEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: %q is not a URL with a host", ErrInsecureEndpoint, endpoint)
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	if u.Scheme == "http" && (host == "localhost" || net.ParseIP(host).IsLoopback()) {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrInsecureEndpoint, endpoint)
}
