// Package blobstore holds the bodies sync moves outside the log: one object per
// stored value, addressed by its origin and digest.
package blobstore

import (
	"context"
	"errors"
	"io"
	"strings"
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

// Key returns the object key for one value from one origin. Both halves are
// checked: a key is a path, and a key built from an origin holding a separator
// would put one machine's objects under another's prefix, where the weekly
// cleanup would delete them. The arguments come from the exporter and the
// importer, so a value that fails either check is a bug rather than a condition
// to recover from, and the call stops instead of returning a key that would
// address the wrong object.
func Key(origin, sha256hex string) string {
	if origin == "" || strings.Contains(origin, "/") {
		panic("blobstore: Key: origin must be non-empty and hold no '/'")
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
