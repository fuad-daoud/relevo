package synclog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// refTag is the one JSON shape a body may carry in place of its value. It is a
// tag rather than a column because a body holding only {"$ref": …} is not a
// row: it says the row is somewhere else, and the importer has to fetch it
// before the row can be applied. A row whose own column is named "$ref" is
// refused by the column-name check long before this is read.
const refTag = "$ref"

// BlobRefThreshold is the stored size above which a value stops travelling
// through the log as its bytes. Measured on the laptop's round files on
// 2026-10-10: 4 KiB moves 97% of the bytes while touching 45% of the rows, so
// most of the saving comes from the few rows that are large. It also keeps the
// many values that are one line or two out of the bucket entirely, which is
// where the per-object overhead would cost more than the bytes do.
const BlobRefThreshold = 4 << 10

// BlobRef is a value that lives in the blob store rather than in the log: the
// digest of the bytes as they are stored, their length, and the codec they are
// stored under. The digest covers the stored bytes and not the plaintext,
// because that is what a fetch checks against and what two machines must agree
// on without exchanging either the plaintext or its codec.
type BlobRef struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Codec  int    `json:"codec"`
}

// NeedsRef reports whether a stored value of this size travels as a ref. The
// threshold is on the stored value because that is what leaves the log: a value
// that compresses below the threshold stays inline, however large it was before
// the codec ran.
func NeedsRef(stored []byte) bool { return len(stored) > BlobRefThreshold }

// RefFor names the object holding these stored bytes.
func RefFor(stored []byte, codec int) BlobRef {
	sum := sha256.Sum256(stored)
	return BlobRef{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(stored)), Codec: codec}
}

// EncodeRef wraps a ref as the tagged object an entry carries in place of its
// value.
func EncodeRef(r BlobRef) json.RawMessage {
	tagged, err := json.Marshal(map[string]BlobRef{refTag: r})
	if err != nil {
		// BlobRef holds a string and two numbers, none of which can fail to
		// marshal; a body that cannot be encoded here would be a body this
		// machine cannot write at all, which no caller could act on.
		panic("blobref: encode: " + err.Error())
	}
	return tagged
}

// DecodeRef reads the ref out of a tagged value. ok is false when v is an
// ordinary value rather than a ref, which is the ordinary case and not a
// failure; a value that is a ref but malformed is an error, because a body that
// looks like a ref and is not one would otherwise be applied as a row whose
// every column is missing.
func DecodeRef(v json.RawMessage) (BlobRef, bool, error) {
	var tagged map[string]json.RawMessage
	if err := json.Unmarshal(v, &tagged); err != nil {
		return BlobRef{}, false, nil
	}
	raw, ok := tagged[refTag]
	if !ok {
		return BlobRef{}, false, nil
	}
	var ref BlobRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return BlobRef{}, true, fmt.Errorf("synclog: ref: %w: %w", ErrInvalid, err)
	}
	if err := checkRef(ref); err != nil {
		return BlobRef{}, true, err
	}
	return ref, true, nil
}

// checkRef refuses a ref whose digest is not 64 lowercase hex characters or
// whose length is negative. Both describe an object that cannot exist: a
// malformed digest names no object to fetch, and a negative length is a number
// some other machine wrote. Catching it here leaves the origin rather than
// sending the importer to ask a bucket about a key that was never built.
func checkRef(ref BlobRef) error {
	if len(ref.SHA256) != 64 {
		return fmt.Errorf("synclog: ref: digest is %d characters, want 64: %w", len(ref.SHA256), ErrInvalid)
	}
	for i := 0; i < len(ref.SHA256); i++ {
		c := ref.SHA256[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("synclog: ref: digest is not lowercase hex: %w", ErrInvalid)
		}
	}
	if ref.Bytes < 0 {
		return fmt.Errorf("synclog: ref: negative length %d: %w", ref.Bytes, ErrInvalid)
	}
	return nil
}
