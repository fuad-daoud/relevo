package board

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// fixSeams pins the three non-deterministic values for one test.
func fixSeams(t *testing.T) {
	t.Helper()
	origID, origSeed, origNow := newElementID, newElementSeed, elementNow
	newElementID = func() (string, error) { return "00112233445566778899aabbccddeeff", nil }
	newElementSeed = func() int64 { return 42 }
	elementNow = func() int64 { return 1700000000000 }
	t.Cleanup(func() { newElementID, newElementSeed, elementNow = origID, origSeed, origNow })
}

// writeScene writes body to a fresh scene path and returns it.
func writeScene(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "board.excalidraw")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write scene: %v", err)
	}
	return p
}

// cockpitTheme is the default theme, looked up so a test names it once.
func cockpitTheme(t *testing.T) *Theme {
	t.Helper()
	th, err := Lookup("cockpit")
	if err != nil {
		t.Fatalf("Lookup(cockpit): %v", err)
	}
	return th
}

// decodeElement unmarshals one raw element into a generic object.
func decodeElement(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var el map[string]any
	if err := json.Unmarshal(raw, &el); err != nil {
		t.Fatalf("decode element: %v", err)
	}
	return el
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

// fixedElement is the exact byte sequence the builder emits under the fixed
// seams, for text "hi" at (1, 2) under cockpit.
const fixedElement = `{"id":"00112233445566778899aabbccddeeff","type":"text","x":1,"y":2,` +
	`"width":24,"height":24,"angle":0,"strokeColor":"#e6e8ec","backgroundColor":"transparent",` +
	`"fillStyle":"solid","strokeWidth":2,"strokeStyle":"solid","roughness":0,` +
	`"opacity":100,"groupIds":[],"frameId":null,"index":null,"roundness":null,` +
	`"seed":42,"version":1,"versionNonce":0,"isDeleted":false,"boundElements":null,` +
	`"updated":1700000000000,"link":null,"locked":false,"text":"hi","fontSize":20,` +
	`"fontFamily":3,"textAlign":"left","verticalAlign":"top","containerId":null,` +
	`"originalText":"hi","autoResize":true,"lineHeight":1.2}`

func TestAnnotateElementFields(t *testing.T) {
	fixSeams(t)
	_, raw, err := buildElementBytes("hi", 1, 2, cockpitTheme(t))
	if err != nil {
		t.Fatalf("buildElementBytes: %v", err)
	}
	if string(raw) != fixedElement {
		t.Errorf("element = %s\nwant    %s", raw, fixedElement)
	}
}

func TestAnnotateWidthCoversTheEstimate(t *testing.T) {
	fixSeams(t)
	for _, text := range []string{"hello", "hi\nthere", "wide\tlabel"} {
		_, raw, err := buildElementBytes(text, 0, 0, cockpitTheme(t))
		if err != nil {
			t.Fatalf("buildElementBytes(%q): %v", text, err)
		}
		el := decodeElement(t, raw)
		if w, _ := el["width"].(float64); w < estimateTextWidth(text) {
			t.Errorf("%q: width = %v, want >= %v", text, w, estimateTextWidth(text))
		}
		if el["autoResize"] != true {
			t.Errorf("%q: autoResize = %v, want true", text, el["autoResize"])
		}
		if el["fontFamily"] != float64(3) {
			t.Errorf("%q: fontFamily = %v, want 3", text, el["fontFamily"])
		}
	}
}

func TestAnnotateEstimatePinsValues(t *testing.T) {
	if got := estimateTextWidth("hello"); got != 60 {
		t.Errorf("estimateTextWidth(hello) = %v, want 60", got)
	}
	if got := estimateTextHeight("hello"); got != 24 {
		t.Errorf("estimateTextHeight(hello) = %v, want 24", got)
	}
	if got := estimateTextWidth("hi\nthere"); got != 60 {
		t.Errorf("estimateTextWidth(hi\\nthere) = %v, want 60", got)
	}
	if got := estimateTextHeight("hi\nthere"); got != 48 {
		t.Errorf("estimateTextHeight(hi\\nthere) = %v, want 48", got)
	}
}

func TestAnnotateRandomFieldsAreWellFormed(t *testing.T) {
	id, raw, err := buildElementBytes("hi", 0, 0, cockpitTheme(t))
	if err != nil {
		t.Fatalf("buildElementBytes: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
		t.Errorf("id = %q, want 32 hex characters", id)
	}
	el := decodeElement(t, raw)
	if seed, _ := el["seed"].(float64); seed < 0 || seed >= float64(int64(1)<<31) {
		t.Errorf("seed = %v, want [0, 2^31)", el["seed"])
	}
	if updated, _ := el["updated"].(float64); updated <= 0 {
		t.Errorf("updated = %v, want a positive unix ms", el["updated"])
	}
}

func TestAnnotateStrokeIsThemeInk(t *testing.T) {
	fixSeams(t)
	for _, name := range []string{"cockpit", "blueprint"} {
		th, err := Lookup(name)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", name, err)
		}
		_, raw, err := buildElementBytes("hi", 0, 0, th)
		if err != nil {
			t.Fatalf("buildElementBytes: %v", err)
		}
		if got := decodeElement(t, raw)["strokeColor"]; got != th.Ink {
			t.Errorf("%s: strokeColor = %v, want %v", name, got, th.Ink)
		}
	}
}
