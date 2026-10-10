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
//
// The encoder is built at the compression level rather than the default: in a
// measurement on real runner streams the higher level came out about 12%
// smaller, for a cost paid once when the value is sealed and then never again.
// zstd's own level 9 has no numeric equivalent in klauspost, which exposes
// named levels, so the numeric level is mapped onto the nearest name.
var (
	zstdEncoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(9)))
	zstdDecoder, _ = zstd.NewReader(nil)
)

// zstdMaxInput is the largest value encodeColumn compresses. Above it the value
// stays plain: the shared encoder's history buffer grows to the largest input it
// ever encodes and keeps that size for the process's life, so one outsized value
// -- a fetched round bundle rather than a row -- would pin hundreds of megabytes
// until the daemon restarts. Legitimate columns are far below this: the largest
// stored row is a few megabytes.
const zstdMaxInput = 8 << 20

// encodeColumn returns value with the codec to store it under: an empty value,
// an oversized one, or one whose zstd frame is not strictly shorter, stays
// plain, so the guard never grows a row.
func encodeColumn(value []byte) ([]byte, int) {
	if len(value) == 0 || len(value) > zstdMaxInput {
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
