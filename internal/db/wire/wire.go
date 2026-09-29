// Package wire carries the owner protocol: framing, control messages, the five
// SQLite storage classes in binary, and the error values the two roles exchange.
// It knows nothing about sockets, so both roles share it.
package wire

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Version is the protocol this build speaks; hello carries it and a mismatch is
// refused, so an upgrade window is visible rather than silently corrupting.
const Version = 1

// Proto names the protocol in the handshake, so a foreign listener is told
// apart from a foreign version.
const Proto = "relevo-owner"

// maxFrameLen bounds a declared frame length. A batch is at most a few MB, so a
// length past this is a corrupt or hostile stream, not a legitimate message.
const maxFrameLen = 1 << 30

// BatchBudget is the encoded size a rows batch aims for. A value is never split
// to meet it, so one large blob yields a batch larger than the budget.
const BatchBudget = 1 << 20

// Conn reads and writes length-prefixed frames on one stream. Writes are
// serialised: a cancel can be sent while a request is in flight, and the two
// must not interleave on the wire.
type Conn struct {
	rw io.ReadWriter
	mu sync.Mutex
}

func NewConn(rw io.ReadWriter) *Conn { return &Conn{rw: rw} }

// Read returns the next frame payload.
func (c *Conn) Read() ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(c.rw, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n == 0 || n > maxFrameLen {
		return nil, fmt.Errorf("wire: frame length %d out of range", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(c.rw, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// Write sends one frame.
func (c *Conn) Write(payload []byte) error {
	if len(payload) == 0 || len(payload) > maxFrameLen {
		return fmt.Errorf("wire: frame length %d out of range", len(payload))
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(payload)))
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.rw.Write(hdr[:]); err != nil {
		return err
	}
	_, err := c.rw.Write(payload)
	return err
}

// The frame kinds. The kind byte selects the payload shape; the JSON header
// repeats the type for a reader of a captured stream.
const (
	KindHello   byte = 1
	KindWelcome byte = 2
	KindRefuse  byte = 3
	KindExec    byte = 4
	KindQuery   byte = 5
	KindRows    byte = 6
	KindNext    byte = 7
	KindDone    byte = 8
	KindClose   byte = 9
	KindCancel  byte = 10
	KindError   byte = 11
)

// Encode builds a frame: the kind byte, a little-endian 4-byte header length,
// the JSON header, then any raw value bytes. Control frames carry no raw bytes.
func Encode(kind byte, v any, raw []byte) ([]byte, error) {
	header, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("wire: encode: %w", err)
	}
	if len(header) > maxFrameLen {
		return nil, fmt.Errorf("wire: header length %d out of range", len(header))
	}
	out := make([]byte, 0, 5+len(header)+len(raw))
	out = append(out, kind)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(header)))
	out = append(out, header...)
	out = append(out, raw...)
	return out, nil
}

// Kind reports a payload's kind byte.
func Kind(payload []byte) (byte, error) {
	if len(payload) == 0 {
		return 0, fmt.Errorf("wire: empty frame")
	}
	return payload[0], nil
}

// Decode unmarshals a payload's JSON header into v and returns the raw value
// bytes that follow it.
func Decode(payload []byte, v any) ([]byte, error) {
	if len(payload) < 5 {
		return nil, fmt.Errorf("wire: frame too short (%d bytes)", len(payload))
	}
	hlen := int(binary.LittleEndian.Uint32(payload[1:5]))
	if 5+hlen > len(payload) {
		return nil, fmt.Errorf("wire: header length %d out of range", hlen)
	}
	if v != nil {
		if err := json.Unmarshal(payload[5:5+hlen], v); err != nil {
			return nil, fmt.Errorf("wire: decode header: %w", err)
		}
	}
	return payload[5+hlen:], nil
}
