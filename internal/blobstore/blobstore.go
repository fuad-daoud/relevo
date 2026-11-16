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

// ErrCredentialsRefused reports that the bucket answered and turned these
// credentials down, rather than failing to answer. The way out is the key, or
// waiting for a new one to work, so it is not a defect in this machine.
var ErrCredentialsRefused = errors.New("blobstore: the bucket refused these credentials")

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
	// PutIfAbsent stores body under key only when the key is empty, a
	// create-only write the store itself enforces. An existing key is answered
	// with (false, nil) and the store is left unchanged; size is checked as in
	// Put. A caller that retries after a lost response cannot tell its own first
	// write from another writer's when the answer is created=false, so it must
	// read the object back before it trusts it.
	PutIfAbsent(ctx context.Context, key string, body io.Reader, size int64) (bool, error)
	// Get writes the body under key to w and returns how many bytes it wrote,
	// or ErrNotFound when the key is not there.
	Get(ctx context.Context, key string, w io.Writer) (int64, error)
	// Has reports whether key is in the store, without its body.
	Has(ctx context.Context, key string) (bool, error)
	// List returns every object under prefix, in key order.
	List(ctx context.Context, prefix string) ([]Object, error)
	// ListAfter lists the objects under prefix whose key sorts after
	// startAfter, in key order. startAfter is a full key, exclusive; an empty
	// one lists everything under prefix. A reader fetching only new segments
	// passes the key of the last segment it holds.
	ListAfter(ctx context.Context, prefix, startAfter string) ([]Object, error)
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

// segmentLog is the middle segment of a segment key. With the number it is 16
// characters, never a body key's 64 hex, so no segment key can equal a body key.
const segmentLog = "log/"

// segmentDigits is the width a segment key pads the first sequence number to: a
// fixed width keeps a bucket listing's key order in sequence order, and 12
// digits outlast any sequence the log can reach.
const segmentDigits = 12

// SegmentKey returns the key of one log segment, <origin>/log/<first>, the
// first sequence number zero-padded to segmentDigits digits. It panics like
// Key: callers check a remote origin with CheckOrigin first, so a failure here
// is a bug, and an unchecked origin would address another machine's prefix.
func SegmentKey(origin string, first int) string {
	if CheckOrigin(origin) != nil {
		panic("blobstore: SegmentKey: origin must be letters, digits, '-' or '_'")
	}
	if first < 0 || first > 999999999999 {
		panic("blobstore: SegmentKey: first must be 0 to 999999999999")
	}
	return origin + "/" + segmentLog + fmt.Sprintf("%0*d", segmentDigits, first)
}

// ParseSegmentKey splits a segment key into its origin and first sequence
// number. It accepts exactly <valid origin>/log/<12 ASCII digits>, so
// SegmentKey of its result equals the key again: a shorter or longer number,
// a sign or a non-digit is refused rather than folded. The digits are read by
// hand because strconv.Atoi would take a sign, whose key does not round-trip.
func ParseSegmentKey(key string) (origin string, first int, ok bool) {
	origin, rest, ok := strings.Cut(key, "/")
	if !ok || !strings.HasPrefix(rest, segmentLog) {
		return "", 0, false
	}
	digits := rest[len(segmentLog):]
	if len(digits) != segmentDigits {
		return "", 0, false
	}
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return "", 0, false
		}
		first = first*10 + int(c-'0')
	}
	if CheckOrigin(origin) != nil {
		return "", 0, false
	}
	return origin, first, true
}

// IsSegmentKey reports whether key addresses a log segment.
func IsSegmentKey(key string) bool {
	_, _, ok := ParseSegmentKey(key)
	return ok
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
