package synclog

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const testDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// TestBlobRefThresholdPins4KiBStored pins the threshold against the stored size
// rather than the plaintext, at the boundary itself: a value of exactly 4 KiB
// stays in the log and one byte more travels as a ref, and the ref carries the
// codec the value was stored under so the fetch can tell a frame from text.
func TestBlobRefThresholdPins4KiBStored(t *testing.T) {
	atThreshold := make([]byte, BlobRefThreshold)
	overThreshold := make([]byte, BlobRefThreshold+1)

	if NeedsRef(atThreshold) {
		t.Errorf("NeedsRef(%d bytes) = true, want false at the threshold", len(atThreshold))
	}
	if !NeedsRef(overThreshold) {
		t.Errorf("NeedsRef(%d bytes) = false, want true one byte over", len(overThreshold))
	}
	if BlobRefThreshold != 4<<10 {
		t.Errorf("BlobRefThreshold = %d, want %d", BlobRefThreshold, 4<<10)
	}

	// The threshold is on the stored value: a plaintext far larger than the
	// threshold that compressed below it stays inline.
	smallStored := make([]byte, 512)
	if NeedsRef(smallStored) {
		t.Error("NeedsRef(512 stored bytes) = true, want the decision made on the stored size")
	}

	for _, codec := range []int{0, 1} {
		ref := RefFor(overThreshold, codec)
		if ref.Codec != codec {
			t.Errorf("RefFor codec = %d, want %d", ref.Codec, codec)
		}
		if ref.Bytes != int64(len(overThreshold)) {
			t.Errorf("RefFor bytes = %d, want %d", ref.Bytes, len(overThreshold))
		}
		if ref.SHA256 == "" || len(ref.SHA256) != 64 {
			t.Errorf("RefFor digest = %q, want 64 hex characters", ref.SHA256)
		}
	}
}

// TestRefRoundTrip pins that a ref survives the trip through JSON unchanged,
// and that a value carrying one decodes back to the same fields it was encoded
// from -- the digest is what a fetch checks the bytes against, so a round trip
// that altered any field would let a wrong body through.
func TestRefRoundTrip(t *testing.T) {
	stored := []byte(strings.Repeat("runner transcript body ", 400))
	want := RefFor(stored, 1)

	encoded := EncodeRef(want)
	if !strings.Contains(string(encoded), `"$ref"`) {
		t.Fatalf("EncodeRef = %s, want a $ref object", encoded)
	}

	got, ok, err := DecodeRef(encoded)
	if err != nil || !ok {
		t.Fatalf("DecodeRef = (ok %v, err %v), want (true, nil)", ok, err)
	}
	if got != want {
		t.Errorf("DecodeRef = %+v, want %+v", got, want)
	}

	// A plain value is not a ref, and says so without an error: that is the
	// ordinary case for every body still travelling inline.
	for _, plain := range []string{`{"a":1}`, `"text"`, `123`, `null`, `[]`, `{`} {
		if _, ok, err := DecodeRef(json.RawMessage(plain)); ok || err != nil {
			t.Errorf("DecodeRef(%s) = (ok %v, err %v), want (false, nil)", plain, ok, err)
		}
	}
}

// TestDecodeRefRejectsMalformed pins the three ways a tagged value can name an
// object that cannot exist. Each has to be refused rather than handed to the
// importer, which would otherwise go looking for an object no key ever built.
func TestDecodeRefRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"short digest", `{"$ref":{"sha256":"abcd","bytes":10,"codec":1}}`},
		{"long digest", `{"$ref":{"sha256":"` + strings.Repeat("a", 65) + `","bytes":10,"codec":1}}`},
		{"non-hex digest", `{"$ref":{"sha256":"` + strings.Repeat("z", 64) + `","bytes":10,"codec":1}}`},
		{"uppercase digest", `{"$ref":{"sha256":"` + strings.Repeat("A", 64) + `","bytes":10,"codec":1}}`},
		{"negative bytes", `{"$ref":{"sha256":"` + testDigest + `","bytes":-1,"codec":1}}`},
		{"ref is not an object", `{"$ref":"not-a-ref"}`},
	}
	for _, c := range cases {
		ref, ok, err := DecodeRef(json.RawMessage(c.body))
		if !ok {
			t.Errorf("%s: ok = false, want true: the value is tagged as a ref", c.name)
			continue
		}
		if err == nil {
			t.Errorf("%s: err = nil for %+v, want a refusal", c.name, ref)
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want it to wrap ErrInvalid", c.name, err)
		}
	}
}
