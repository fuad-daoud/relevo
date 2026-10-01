package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestFrameRoundTripCarriesKindHeaderAndRaw(t *testing.T) {
	sent := &Exec{Header: Header{Type: TypeExec, ID: 7}, Query: "SELECT ?"}
	var buf bytes.Buffer
	c := NewConn(&buf)
	payload, err := Encode(KindExec, sent, []byte{1, 2, 3})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := c.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := c.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	kind, err := Kind(got)
	if err != nil {
		t.Fatalf("Kind: %v", err)
	}
	if kind != KindExec {
		t.Fatalf("kind = %d, want %d", kind, KindExec)
	}
	var decoded Exec
	raw, err := Decode(got, &decoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(decoded, *sent) {
		t.Errorf("decoded = %+v, want %+v", decoded, *sent)
	}
	if !bytes.Equal(raw, []byte{1, 2, 3}) {
		t.Errorf("raw = %v, want [1 2 3]", raw)
	}
}

func TestFrameRejectsMalformedLength(t *testing.T) {
	for _, n := range []uint32{0, maxFrameLen + 1, 1 << 31} {
		var buf bytes.Buffer
		if err := binary.Write(&buf, binary.LittleEndian, n); err != nil {
			t.Fatalf("write length: %v", err)
		}
		if _, err := NewConn(&buf).Read(); err == nil {
			t.Errorf("Read accepted length %d", n)
		}
	}
}

// frameSource is a fake io.ReadWriter that serves a 4-byte little-endian
// declared length followed by generated payload bytes, recording the size of
// every read request handed to it. It holds no source buffer proportional to
// the declared length: the header is written from the declared value and each
// payload byte is generated straight into the caller's slice. Once limit bytes
// have been served, Read returns io.EOF, or blocks on hold until the test closes
// it when hold is non-nil.
type frameSource struct {
	mu       sync.Mutex
	declared uint32
	limit    int
	off      int
	hold     chan struct{}
	requests []int
}

func newFrameSource(declared uint32, payload int, hold chan struct{}) *frameSource {
	return &frameSource{declared: declared, limit: 4 + payload, hold: hold}
}

func (s *frameSource) Read(p []byte) (int, error) {
	s.mu.Lock()
	s.requests = append(s.requests, len(p))
	declared, limit, off, hold := s.declared, s.limit, s.off, s.hold
	s.mu.Unlock()

	n := 0
	for n < len(p) && off+n < limit {
		pos := off + n
		if pos < 4 {
			p[n] = byte(declared >> (8 * pos))
		} else {
			p[n] = byte(pos) // generated here, never stored
		}
		n++
	}
	if n > 0 {
		s.mu.Lock()
		s.off += n
		s.mu.Unlock()
	}
	if n == len(p) {
		return n, nil
	}
	if hold != nil {
		<-hold
	}
	return n, io.EOF
}

func (s *frameSource) Write(p []byte) (int, error) { return len(p), nil }

// maxRequest returns the largest buffer size any read request asked for.
func (s *frameSource) maxRequest() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := 0
	for _, r := range s.requests {
		if r > m {
			m = r
		}
	}
	return m
}

// requestCount returns how many read requests have been handed to the fake.
func (s *frameSource) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func TestReadOfADeclaredHugeFrameBoundsEveryReadRequest(t *testing.T) {
	// The declared length is the largest the protocol accepts (maxFrameLen) and
	// is far above one chunk, so the reader must never allocate it up front:
	// every request it hands the stream stays chunk-sized while the peer
	// disappears. A declared length over the cap is refused from the header
	// alone and is pinned by TestFrameOverTheCapIsRefusedFromTheHeaderAlone.
	t.Run("header then EOF", func(t *testing.T) {
		src := newFrameSource(maxFrameLen, 0, nil)
		got, err := NewConn(src).Read()
		if err == nil {
			t.Fatalf("Read of a peer that sent no payload returned %d bytes and no error", len(got))
		}
		if m := src.maxRequest(); m > readChunk {
			t.Fatalf("largest read request = %d, want at most the %d-byte chunk", m, readChunk)
		}
	})

	t.Run("header, one byte, then blocked", func(t *testing.T) {
		release := make(chan struct{})
		src := newFrameSource(maxFrameLen, 1, release)
		errs := make(chan error, 1)
		go func() {
			_, err := NewConn(src).Read()
			errs <- err
		}()

		// Wait for the reader to reach the payload before checking its
		// requests: a blocked peer is waited for, not failed early.
		deadline := time.Now().Add(2 * time.Second)
		for src.maxRequest() <= 4 {
			if time.Now().After(deadline) {
				t.Fatal("reader never requested the payload")
			}
			time.Sleep(time.Millisecond)
		}
		if m := src.maxRequest(); m > readChunk {
			t.Fatalf("largest read request = %d, want at most the %d-byte chunk", m, readChunk)
		}

		close(release)
		select {
		case err := <-errs:
			if err == nil {
				t.Fatal("Read returned no error after the peer sent one byte and ended")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Read did not return after the peer ended")
		}
	})
}

func TestFrameAtTheCapIsAccepted(t *testing.T) {
	src := newFrameSource(maxFrameLen, int(maxFrameLen), nil)
	got, err := NewConn(src).Read()
	if err != nil {
		t.Fatalf("Read at the cap: %v", err)
	}
	if len(got) != maxFrameLen {
		t.Fatalf("payload length = %d, want %d", len(got), maxFrameLen)
	}
	if m := src.maxRequest(); m > readChunk {
		t.Fatalf("largest read request = %d, want at most the %d-byte chunk", m, readChunk)
	}
}

func TestFrameOverTheCapIsRefusedFromTheHeaderAlone(t *testing.T) {
	for _, declared := range []uint32{0, maxFrameLen + 1} {
		src := newFrameSource(declared, 0, nil)
		if _, err := NewConn(src).Read(); err == nil {
			t.Errorf("Read accepted declared length %d", declared)
		}
		if got := src.requestCount(); got != 1 {
			t.Errorf("declared length %d: %d read requests, want only the header", declared, got)
		}
		if m := src.maxRequest(); m != 4 {
			t.Errorf("declared length %d: largest read request = %d, want the 4-byte header", declared, m)
		}
	}
}

func TestFrameAcrossTheChunkBoundaryKeepsTheStreamAligned(t *testing.T) {
	first := bytes.Repeat([]byte{0x11}, readChunk+1)
	second := []byte("second frame")
	var buf bytes.Buffer
	writeFrame(t, &buf, first)
	writeFrame(t, &buf, second)

	c := NewConn(&buf)
	got1, err := c.Read()
	if err != nil {
		t.Fatalf("first Read: %v", err)
	}
	if !bytes.Equal(got1, first) {
		t.Fatalf("first frame = %d bytes, want %d equal bytes", len(got1), len(first))
	}
	got2, err := c.Read()
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if !bytes.Equal(got2, second) {
		t.Fatalf("second frame = %q, want %q", got2, second)
	}
}

func writeFrame(t *testing.T, buf *bytes.Buffer, payload []byte) {
	t.Helper()
	if err := binary.Write(buf, binary.LittleEndian, uint32(len(payload))); err != nil {
		t.Fatalf("write length: %v", err)
	}
	if _, err := buf.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
}

func TestValueClassesRoundTrip(t *testing.T) {
	type tc struct {
		name string
		in   any
		want any
	}
	cases := []tc{
		{"null", nil, nil},
		{"int", int64(-7), int64(-7)},
		{"float", 1.5, 1.5},
		{"text", "héllo", "héllo"},
		{"empty text", "", ""},
		{"blob", []byte{0, 255, 1}, []byte{0, 255, 1}},
		{"empty blob", []byte{}, []byte{}},
		{"bool true", true, int64(1)},
		{"bool false", false, int64(0)},
	}
	var b Builder
	for _, c := range cases {
		if err := b.Add(c.in); err != nil {
			t.Fatalf("Add %s: %v", c.name, err)
		}
	}
	cur := NewCursor(b.Bytes())
	for _, c := range cases {
		got, ok, err := cur.Next()
		if err != nil {
			t.Fatalf("Next %s: %v", c.name, err)
		}
		if !ok {
			t.Fatalf("Next %s: no more values", c.name)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.name, got, c.want)
		}
	}
	if _, ok, err := cur.Next(); ok || err != nil {
		t.Errorf("trailing value: ok=%v err=%v", ok, err)
	}
}

func TestBlobOfFourAndAHalfMegabytes(t *testing.T) {
	blob := bytes.Repeat([]byte{0xAB}, 9<<19)
	var b Builder
	if err := b.Add(blob); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, ok, err := NewCursor(b.Bytes()).Next()
	if err != nil || !ok {
		t.Fatalf("Next: ok=%v err=%v", ok, err)
	}
	out, isBytes := got.([]byte)
	if !isBytes || !bytes.Equal(out, blob) {
		t.Fatalf("blob round trip: got %T of %d bytes", got, len(out))
	}
}

func TestBatchBuilderCursorBoundaryKeepsEveryValueWhole(t *testing.T) {
	// Values that together exceed the batch budget: the cursor must read a
	// value that starts before the boundary and ends after it.
	var b Builder
	want := []any{}
	for i := 0; i < 4; i++ {
		blob := bytes.Repeat([]byte{byte(i)}, BatchBudget/2)
		if err := b.Add(blob); err != nil {
			t.Fatalf("Add: %v", err)
		}
		want = append(want, blob)
	}
	if b.Len() < 2*BatchBudget {
		t.Fatalf("builder only reached %d bytes", b.Len())
	}
	cur := NewCursor(b.Bytes())
	for i, w := range want {
		got, ok, err := cur.Next()
		if err != nil || !ok {
			t.Fatalf("Next %d: ok=%v err=%v", i, ok, err)
		}
		if !bytes.Equal(got.([]byte), w.([]byte)) {
			t.Fatalf("value %d does not match", i)
		}
	}
}

func TestErrorCarriesSQLiteCode(t *testing.T) {
	e := NewError(3, 5, 5, "database is locked")
	if e.Error() != "database is locked" {
		t.Errorf("Error() = %q", e.Error())
	}
	if e.Code() != 5 || e.ExtendedCode() != 5 {
		t.Errorf("codes = %d/%d, want 5/5", e.Code(), e.ExtendedCode())
	}
	if code, ok := CodeOf(e); !ok || code != 5 {
		t.Errorf("CodeOf = %d,%v", code, ok)
	}
	if code, ok := CodeOf(errors.New("plain")); ok || code != 0 {
		t.Errorf("CodeOf(plain) = %d,%v", code, ok)
	}
}

func TestRefusalIsNotASQLiteCode(t *testing.T) {
	r := &Refusal{Header: Header{Type: TypeRefuse}, Code: RefuseRestarting, Message: "restarting"}
	if r.Error() != "restarting" {
		t.Errorf("Error() = %q", r.Error())
	}
	if _, ok := CodeOf(r); ok {
		t.Error("a refusal must not expose Code() int")
	}
}
