package board

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeScene writes body to a fresh scene path and returns it.
func writeScene(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "board.excalidraw")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write scene: %v", err)
	}
	return p
}

func TestTextElementsInSceneOrder(t *testing.T) {
	path := writeScene(t, `{"type":"excalidraw","elements":[`+
		`{"id":"a","type":"text","x":1,"y":2,"text":"one"},`+
		`{"id":"r","type":"rectangle","x":0,"y":0},`+
		`{"id":"b","type":"text","x":3.5,"y":4,"text":"two"}]}`)

	got, err := TextElements(path)
	if err != nil {
		t.Fatalf("TextElements: %v", err)
	}
	want := []TextElement{
		{ID: "a", X: 1, Y: 2, Text: "one"},
		{ID: "b", X: 3.5, Y: 4, Text: "two"},
	}
	if len(got) != len(want) {
		t.Fatalf("TextElements = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("element %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestTextElementsSkipsDeleted(t *testing.T) {
	path := writeScene(t, `{"type":"excalidraw","elements":[`+
		`{"id":"a","type":"text","x":1,"y":2,"text":"keep"},`+
		`{"id":"b","type":"text","x":3,"y":4,"text":"gone","isDeleted":true}]}`)

	got, err := TextElements(path)
	if err != nil {
		t.Fatalf("TextElements: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("TextElements = %+v, want only the live element", got)
	}
}

func TestTextElementsRefusesInvalid(t *testing.T) {
	for name, body := range map[string]string{
		"wrong type":  `{"type":"other","elements":[]}`,
		"bad element": `{"type":"excalidraw","elements":[5]}`,
		"not json":    `not json`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeScene(t, body)
			if _, err := TextElements(path); !errors.Is(err, ErrInvalid) {
				t.Errorf("TextElements(%s) = %v, want ErrInvalid", body, err)
			}
		})
	}
}