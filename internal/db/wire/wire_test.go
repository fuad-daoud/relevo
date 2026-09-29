package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
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
