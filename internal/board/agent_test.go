package board

import (
	"encoding/json"
	"errors"
	"math"
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

// sceneElements reads path's elements as raw messages.
func sceneElements(t *testing.T, path string) []json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read scene: %v", err)
	}
	var doc struct {
		Elements []json.RawMessage `json:"elements"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode scene: %v", err)
	}
	return doc.Elements
}

// lastElement reads path's last element as a generic object.
func lastElement(t *testing.T, path string) map[string]any {
	t.Helper()
	els := sceneElements(t, path)
	if len(els) == 0 {
		t.Fatalf("scene %s has no elements", path)
	}
	var el map[string]any
	if err := json.Unmarshal(els[len(els)-1], &el); err != nil {
		t.Fatalf("decode element: %v", err)
	}
	return el
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

// fixedElement is the exact byte sequence one annotation appends under the
// fixed seams, for text "hi" at (1, 2) under cockpit.
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
	path := writeScene(t, `{"type":"excalidraw","elements":[]}`)
	if _, err := Annotate(path, AnnotateOptions{
		Text: "hi", X: 1, Y: 2, HasX: true, HasY: true, Theme: cockpitTheme(t),
	}); err != nil {
		t.Fatalf("Annotate: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read scene: %v", err)
	}
	want := `{"type":"excalidraw","elements":[` + fixedElement + `]}`
	if string(got) != want {
		t.Errorf("scene = %s\nwant    %s", got, want)
	}
}

func TestAnnotateWidthCoversTheEstimate(t *testing.T) {
	fixSeams(t)
	for _, text := range []string{"hello", "hi\nthere", "wide\tlabel"} {
		path := writeScene(t, `{"type":"excalidraw","elements":[]}`)
		if _, err := Annotate(path, AnnotateOptions{Text: text, Theme: cockpitTheme(t)}); err != nil {
			t.Fatalf("Annotate(%q): %v", text, err)
		}
		el := lastElement(t, path)
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

func TestAnnotateKeepsEveryOtherByte(t *testing.T) {
	fixSeams(t)
	compact := `{"type":"excalidraw","elements":[{"id":"a","type":"text","x":1,"y":2,"text":"hi"}],"appState":{"zoom":1}}`
	pretty := "{\n  \"type\": \"excalidraw\",\n  \"elements\": [\n    {\n      \"id\": \"a\"\n    }\n  ],\n  \"appState\": {}\n}"

	cases := []struct{ name, in, want string }{
		{
			"compact",
			compact,
			`{"type":"excalidraw","elements":[{"id":"a","type":"text","x":1,"y":2,"text":"hi"},` +
				fixedElement + `],"appState":{"zoom":1}}`,
		},
		{
			"pretty",
			pretty,
			"{\n  \"type\": \"excalidraw\",\n  \"elements\": [\n    {\n      \"id\": \"a\"\n    }," +
				fixedElement + "\n  ],\n  \"appState\": {}\n}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeScene(t, tc.in)
			if _, err := Annotate(path, AnnotateOptions{
				Text: "hi", X: 1, Y: 2, HasX: true, HasY: true, Theme: cockpitTheme(t),
			}); err != nil {
				t.Fatalf("Annotate: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read scene: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("scene = %q\nwant    %q", got, tc.want)
			}
		})
	}
}

func TestAnnotateDefaultPositionEmptyScene(t *testing.T) {
	fixSeams(t)
	path := writeScene(t, `{"type":"excalidraw","elements":[]}`)
	if _, err := Annotate(path, AnnotateOptions{Text: "hi", Theme: cockpitTheme(t)}); err != nil {
		t.Fatalf("Annotate: %v", err)
	}
	el := lastElement(t, path)
	if el["x"] != float64(0) || el["y"] != float64(0) {
		t.Errorf("default position = (%v, %v), want (0, 0)", el["x"], el["y"])
	}
}

func TestAnnotateDefaultPositionBelowBounds(t *testing.T) {
	fixSeams(t)
	path := writeScene(t, `{"type":"excalidraw","elements":[`+
		`{"id":"a","type":"text","x":10,"y":20,"width":5,"height":6},`+
		`{"id":"b","type":"rectangle","x":3,"y":0,"width":1,"height":2},`+
		`{"id":"d","type":"rectangle","x":-100,"y":-100,"width":1,"height":1,"isDeleted":true}]}`)
	if _, err := Annotate(path, AnnotateOptions{Text: "hi", Theme: cockpitTheme(t)}); err != nil {
		t.Fatalf("Annotate: %v", err)
	}
	el := lastElement(t, path)
	if el["x"] != float64(3) {
		t.Errorf("default x = %v, want 3 (the leftmost live element)", el["x"])
	}
	if el["y"] != float64(46) {
		t.Errorf("default y = %v, want 46 (lowest edge 26 + gap 20)", el["y"])
	}
}

func TestAnnotateRefusesMissingScene(t *testing.T) {
	fixSeams(t)
	path := filepath.Join(t.TempDir(), "missing.excalidraw")
	_, err := Annotate(path, AnnotateOptions{Text: "hi", Theme: cockpitTheme(t)})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Annotate(missing) = %v, want ErrNotFound", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("annotate created the missing scene: stat error = %v", statErr)
	}
}

func TestAnnotateRandomFieldsAreWellFormed(t *testing.T) {
	path := writeScene(t, `{"type":"excalidraw","elements":[]}`)
	el, err := Annotate(path, AnnotateOptions{Text: "hi", Theme: cockpitTheme(t)})
	if err != nil {
		t.Fatalf("Annotate: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(el.ID) {
		t.Errorf("id = %q, want 32 hex characters", el.ID)
	}
	obj := lastElement(t, path)
	if seed, _ := obj["seed"].(float64); seed < 0 || seed >= float64(int64(1)<<31) {
		t.Errorf("seed = %v, want [0, 2^31)", obj["seed"])
	}
	if updated, _ := obj["updated"].(float64); updated <= 0 {
		t.Errorf("updated = %v, want a positive unix ms", obj["updated"])
	}
}

func TestAnnotateStrokeIsThemeInk(t *testing.T) {
	fixSeams(t)
	for _, name := range []string{"cockpit", "blueprint"} {
		th, err := Lookup(name)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", name, err)
		}
		path := writeScene(t, `{"type":"excalidraw","elements":[]}`)
		if _, err := Annotate(path, AnnotateOptions{Text: "hi", Theme: th}); err != nil {
			t.Fatalf("Annotate: %v", err)
		}
		if got := lastElement(t, path)["strokeColor"]; got != th.Ink {
			t.Errorf("%s: strokeColor = %v, want %v", name, got, th.Ink)
		}
	}
}

func TestAnnotateRefusesBadOptions(t *testing.T) {
	fixSeams(t)
	path := writeScene(t, `{"type":"excalidraw","elements":[]}`)
	cases := []struct {
		name string
		opts AnnotateOptions
	}{
		{"empty text", AnnotateOptions{Text: "", Theme: cockpitTheme(t)}},
		{"lone x", AnnotateOptions{Text: "hi", HasX: true, X: 1, Theme: cockpitTheme(t)}},
		{"lone y", AnnotateOptions{Text: "hi", HasY: true, Y: 1, Theme: cockpitTheme(t)}},
		{"non-finite x", AnnotateOptions{Text: "hi", HasX: true, HasY: true, X: math.NaN(), Y: 1, Theme: cockpitTheme(t)}},
		{"non-finite y", AnnotateOptions{Text: "hi", HasX: true, HasY: true, X: 1, Y: math.Inf(1), Theme: cockpitTheme(t)}},
		{"nil theme", AnnotateOptions{Text: "hi"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Annotate(path, tc.opts); !errors.Is(err, ErrUsage) {
				t.Errorf("Annotate(%s) = %v, want ErrUsage", tc.name, err)
			}
		})
	}
}
