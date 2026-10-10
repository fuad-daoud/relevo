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
	"time"
)

// ErrNotFound reports that an object is not in the store. A store reports it
// rather than any other error because a body that is missing and a body whose
// fetch failed call for opposite handling: the first latches the origin and
// names the round, the second is retried.
var ErrNotFound = errors.New("blobstore: object not found")

// Object is one stored body as a listing describes it.
type Object struct {
	Key      string
	Size     int64
	Modified time.Time
}

// BlobStore is where the bodies the log only points at live. Bodies never cross
// the JSON pipe, so an entry names a digest and the bytes arrive out of band;
// every operation is keyed, and no operation reads another origin's prefix.
type BlobStore interface {
	// Put stores body under key. size is the length the caller expects to
	// write, and a body of a different length is refused rather than stored
	// under a digest that no longer describes it.
	Put(ctx context.Context, key string, body io.Reader, size int64) error
	// Get writes the body under key to w and returns how many bytes it wrote,
	// or ErrNotFound when the object is not there.
	Get(ctx context.Context, key string, w io.Writer) (int64, error)
	// Has reports whether key is in the store, without its body.
	Has(ctx context.Context, key string) (bool, error)
	// List returns every object under prefix, in key order.
	List(ctx context.Context, prefix string) ([]Object, error)
	// Delete removes key, and is not an error when it is already absent.
	Delete(ctx context.Context, key string) error
}

// ErrBadOrigin reports an origin that cannot be the first segment of a key.
var ErrBadOrigin = errors.New("blobstore: origin is not an installation id")

// maxOriginLen is far above an installation id's 26 characters; it only stops a
// remote entry from making a key arbitrarily long.
const maxOriginLen = 64

// CheckOrigin refuses an origin that is not letters, digits, '-' and '_'.
// Installation ids are ULIDs, and an origin arriving in a remote log entry is
// not trusted: a '/', '.', '?', '#' or '%' in it would address another key, or
// reshape the signed request URL the key is put into.
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
// checked: a key is a path, and a key built from an origin holding a separator
// would put one machine's objects under another's prefix, where the weekly
// cleanup would delete them. Callers check a remote origin with CheckOrigin
// first, so a value that fails here is a bug rather than a condition to recover
// from, and the call stops instead of returning a key that would address the
// wrong object.
func Key(origin, sha256hex string) string {
	if CheckOrigin(origin) != nil {
		panic("blobstore: Key: origin must be letters, digits, '-' or '_'")
	}
	if len(sha256hex) != 64 || !isLowerHex(sha256hex) {
		panic("blobstore: Key: digest must be 64 lowercase hex characters")
	}
	return origin + "/" + sha256hex
}

// isLowerHex reports whether s is 64 characters of 0-9a-f. Uppercase is
// refused rather than folded: the digest in a key has to be the same string the
// importer computes from the bytes it fetched, and a key that matched only one
// of the two spellings would make a present object look absent.
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
