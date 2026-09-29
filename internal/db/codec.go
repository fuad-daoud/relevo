package db

import (
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// A stored column carries a codec value beside it: 0 keeps the plain value, 1
// keeps a zstd frame. A read refuses any other codec rather than returning
// garbage.
const (
	codecPlain = 0
	codecZstd  = 1
)

// One encoder and one decoder are shared by every row. NewWriter(nil) and
// NewReader(nil) pass no option and cannot fail, and EncodeAll and DecodeAll
// are documented safe for concurrent calls, so reads and writes share these
// without a lock.
var (
	zstdEncoder, _ = zstd.NewWriter(nil)
	zstdDecoder, _ = zstd.NewReader(nil)
)

// encodeColumn returns value with the codec to store it under: an empty value,
// or one whose zstd frame is not strictly shorter, stays plain, so the guard
// never grows a row.
func encodeColumn(value []byte) ([]byte, int) {
	if len(value) == 0 {
		return value, codecPlain
	}
	frame := zstdEncoder.EncodeAll(value, nil)
	if len(frame) >= len(value) {
		return value, codecPlain
	}
	return frame, codecZstd
}

// decodeColumn returns the plaintext a stored value and its codec describe.
func decodeColumn(value []byte, codec int) ([]byte, error) {
	switch codec {
	case codecPlain:
		return value, nil
	case codecZstd:
		out, err := zstdDecoder.DecodeAll(value, nil)
		if err != nil {
			return nil, fmt.Errorf("db: decode column: zstd: %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("db: decode column: unknown codec %d: %w", codec, ErrInvalid)
	}
}

// encodeText is encodeColumn for a TEXT column: while plain it returns the
// original Go string, so the column's storage class and typeof stay text, and
// only a frame is stored as a blob.
func encodeText(s string) (any, int) {
	value, codec := encodeColumn([]byte(s))
	if codec == codecPlain {
		return s, codecPlain
	}
	return value, codec
}
