package synclog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// refTag marks a body holding a ref instead of its value: the row is elsewhere
// and the importer fetches before applying. A column literally named "$ref" is
// refused by the column-name check first.
const refTag = "$ref"

// BlobRefThreshold is the stored size above which a value travels as a ref.
// Measured on round files on 2026-10-10: 4 KiB moves 97% of the bytes in 45% of
// the rows, and keeps one-line values out of the bucket where per-object
// overhead exceeds the bytes.
const BlobRefThreshold = 4 << 10

// MaxBlobBytes is the largest stored value a ref may name. The largest round
// file body measured was 835 KB stored, so this bounds what one remote ref can
// make a machine download.
const MaxBlobBytes = 64 << 20

// MaxBatchBlobBytes bounds the distinct bodies one batch names. A batch applies
// in one transaction, so every named body is in memory at once with its
// re-encoded copy; the exporter splits first, so only a forged batch is refused.
const MaxBatchBlobBytes = 512 << 20

// BlobRef is a value living in the blob store: digest and length of the stored
// bytes and their codec. The digest covers stored bytes, not plaintext, which is
// what a fetch checks and what two machines agree on.
type BlobRef struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Codec  int    `json:"codec"`
}

// NeedsRef reports whether a stored value of this size travels as a ref. The
// threshold is on the stored value, which is what leaves the log: a value
// compressing below it stays inline.
func NeedsRef(stored []byte) bool { return len(stored) > BlobRefThreshold }

func RefFor(stored []byte, codec int) BlobRef {
	sum := sha256.Sum256(stored)
	return BlobRef{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(stored)), Codec: codec}
}

func EncodeRef(r BlobRef) json.RawMessage {
	tagged, err := json.Marshal(map[string]BlobRef{refTag: r})
	if err != nil {
		// BlobRef holds a string and two numbers that cannot fail to marshal; a body
		// that cannot be encoded here is one this machine cannot write at all, which
		// no caller could act on.
		panic("blobref: encode: " + err.Error())
	}
	return tagged
}

// DecodeRef reads the ref out of a tagged value. ok is false for an ordinary
// value, which is not a failure; a malformed ref is an error, or a lookalike
// body would apply as a row with every column missing.
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

// checkRef refuses a ref naming an object that cannot exist: a malformed digest
// or a negative length. Catching it here leaves the origin rather than asking a
// bucket about a key never built.
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
	if ref.Bytes < 0 || ref.Bytes > MaxBlobBytes {
		return fmt.Errorf("synclog: ref: length %d is outside 0 to %d: %w", ref.Bytes, MaxBlobBytes, ErrInvalid)
	}
	return nil
}
