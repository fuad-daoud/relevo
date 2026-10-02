package board

import (
	"bytes"
	"errors"
	"testing"
)

// TestAppendElementLeavesTheRestByteIdentical pins the raw-byte contract: the
// first byte the output differs from the input at starts the inserted element,
// the inserted element is exactly "," + element, and every byte after it is
// the input's own tail unchanged -- nothing else is re-encoded.
func TestAppendElementLeavesTheRestByteIdentical(t *testing.T) {
	scene := []byte(`{"type":"excalidraw","version":2,"source":"local","elements":[{"id":"a","type":"rectangle","x":1,"y":2}],"appState":{"viewBackgroundColor":"#0f1115"},"files":{}}`)
	element := []byte(`{"id":"comment01","type":"text","text":"hi ] there"}`)

	out, err := AppendElement(scene, element)
	if err != nil {
		t.Fatalf("AppendElement: %v", err)
	}
	if bytes.Equal(out, scene) {
		t.Fatal("AppendElement left the scene unchanged")
	}

	inserted := append([]byte(","), element...)
	diff := 0
	for diff < len(scene) && diff < len(out) && scene[diff] == out[diff] {
		diff++
	}
	if diff+len(inserted) > len(out) {
		t.Fatalf("output is too short to hold the inserted element at %d", diff)
	}
	if !bytes.Equal(out[diff:diff+len(inserted)], inserted) {
		t.Fatalf("first difference at %d is not the inserted element:\n got %q\nwant %q", diff, out[diff:diff+len(inserted)], inserted)
	}
	if !bytes.Equal(out[diff+len(inserted):], scene[diff:]) {
		t.Fatalf("the tail after the insertion is not byte-identical:\n got %q\nwant %q", out[diff+len(inserted):], scene[diff:])
	}
}

// TestAppendElementEmptyArray: an empty elements array takes the element with
// no leading comma, and whitespace around the brackets is preserved.
func TestAppendElementEmptyArray(t *testing.T) {
	cases := []struct {
		scene string
		want  string
	}{
		{`{"elements":[]}`, `{"elements":[E]}`},
		{`{ "elements" : [ ] }`, `{ "elements" : [ E] }`},
	}
	for _, tc := range cases {
		out, err := AppendElement([]byte(tc.scene), []byte("E"))
		if err != nil {
			t.Fatalf("AppendElement(%q): %v", tc.scene, err)
		}
		if string(out) != tc.want {
			t.Errorf("AppendElement(%q) = %q, want %q", tc.scene, out, tc.want)
		}
	}
}

// TestAppendElementNonSceneRefuses: anything that is not an object whose
// elements is an array is ErrInvalid.
func TestAppendElementNonSceneRefuses(t *testing.T) {
	for _, scene := range []string{
		``,
		`not json`,
		`[]`,
		`"a string"`,
		`{"type":"excalidraw"}`,
		`{"elements":null}`,
		`{"elements":{}}`,
		`{"elements":"x"}`,
	} {
		if _, err := AppendElement([]byte(scene), []byte("{}")); !errors.Is(err, ErrInvalid) {
			t.Errorf("AppendElement(%q) = %v, want ErrInvalid", scene, err)
		}
	}
}
