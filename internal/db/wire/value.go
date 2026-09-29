package wire

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// SQLite's five storage classes, one type byte each. Text and blob carry a
// 4-byte little-endian length then raw bytes, so a value needs no base64 and no
// size cap below the frame bound.
const (
	ValueNull  byte = 0
	ValueInt   byte = 1
	ValueFloat byte = 2
	ValueText  byte = 3
	ValueBlob  byte = 4
)

// timeLayout is how a bound time.Time crosses the wire: the database's own text
// encoding, so the owner stores what a direct bind would.
const timeLayout = "2006-01-02T15:04:05.000Z"

// Builder accumulates encoded values for one value frame.
type Builder struct {
	b []byte
}

func (b *Builder) Len() int { return len(b.b) }

func (b *Builder) Bytes() []byte { return b.b }

func (b *Builder) Reset() { b.b = b.b[:0] }

// Add encodes one Go value. It accepts what database/sql hands a driver plus
// int, so callers never pre-convert.
func (b *Builder) Add(v any) error {
	out, err := appendValue(b.b, v)
	if err != nil {
		return err
	}
	b.b = out
	return nil
}

func appendValue(dst []byte, v any) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return append(dst, ValueNull), nil
	case int64:
		return appendInt(dst, x), nil
	case int:
		return appendInt(dst, int64(x)), nil
	case int32:
		return appendInt(dst, int64(x)), nil
	case bool:
		if x {
			return appendInt(dst, 1), nil
		}
		return appendInt(dst, 0), nil
	case float64:
		dst = append(dst, ValueFloat)
		return binary.LittleEndian.AppendUint64(dst, math.Float64bits(x)), nil
	case float32:
		dst = append(dst, ValueFloat)
		return binary.LittleEndian.AppendUint64(dst, math.Float64bits(float64(x))), nil
	case string:
		return appendBytes(dst, ValueText, []byte(x)), nil
	case []byte:
		return appendBytes(dst, ValueBlob, x), nil
	case time.Time:
		return appendBytes(dst, ValueText, []byte(x.UTC().Format(timeLayout))), nil
	default:
		return nil, fmt.Errorf("wire: cannot encode %T", v)
	}
}

func appendInt(dst []byte, v int64) []byte {
	dst = append(dst, ValueInt)
	return binary.LittleEndian.AppendUint64(dst, uint64(v))
}

func appendBytes(dst []byte, kind byte, b []byte) []byte {
	dst = append(dst, kind)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(b)))
	return append(dst, b...)
}

// Cursor reads the values a Builder wrote.
type Cursor struct {
	b   []byte
	off int
}

func NewCursor(b []byte) *Cursor { return &Cursor{b: b} }

// Next returns the next value; ok is false at the end of the batch.
func (c *Cursor) Next() (any, bool, error) {
	if c.off >= len(c.b) {
		return nil, false, nil
	}
	kind := c.b[c.off]
	c.off++
	switch kind {
	case ValueNull:
		return nil, true, nil
	case ValueInt:
		n, err := c.take(8)
		if err != nil {
			return nil, false, err
		}
		return int64(binary.LittleEndian.Uint64(n)), true, nil
	case ValueFloat:
		n, err := c.take(8)
		if err != nil {
			return nil, false, err
		}
		return math.Float64frombits(binary.LittleEndian.Uint64(n)), true, nil
	case ValueText:
		n, err := c.bytes()
		if err != nil {
			return nil, false, err
		}
		return string(n), true, nil
	case ValueBlob:
		n, err := c.bytes()
		if err != nil {
			return nil, false, err
		}
		return n, true, nil
	default:
		return nil, false, fmt.Errorf("wire: unknown value kind %d", kind)
	}
}

func (c *Cursor) take(n int) ([]byte, error) {
	if c.off+n > len(c.b) {
		return nil, fmt.Errorf("wire: truncated value")
	}
	out := c.b[c.off : c.off+n]
	c.off += n
	return out, nil
}

func (c *Cursor) bytes() ([]byte, error) {
	head, err := c.take(4)
	if err != nil {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint32(head))
	if c.off+n > len(c.b) {
		return nil, fmt.Errorf("wire: value length %d out of range", n)
	}
	return c.take(n)
}
