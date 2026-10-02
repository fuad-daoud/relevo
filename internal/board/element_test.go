package board

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// jsonKeys returns the top-level key names of a JSON object, in document order.
func jsonKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		t.Fatalf("jsonKeys: %q is not a JSON object (token %v, err %v)", raw, tok, err)
	}
	var keys []string
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			t.Fatalf("jsonKeys: %v", err)
		}
		keys = append(keys, kt.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("jsonKeys: %v", err)
		}
	}
	return keys
}

// TestTextElementFieldSet pins the whole emitted element: every field the
// vendored 0.18.1 text element carries, in the serializer's order, with the
// font-safety fields on (autoResize, an explicit width == TextSize), the
// comment marker shape, and Cascadia's metrics.
func TestTextElementFieldSet(t *testing.T) {
	el, err := NewTextElement(TextOptions{
		Text:        "hello",
		X:           10,
		Y:           20,
		StrokeColor: "#b48cf2",
		Updated:     1700000000000,
		Marker:      &CommentMarker{Comment: true, By: "human", At: "2026-10-01T12:00:00Z"},
	})
	if err != nil {
		t.Fatalf("NewTextElement: %v", err)
	}

	raw, err := json.Marshal(el)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wantOrder := []string{
		"id", "type", "x", "y", "width", "height", "angle", "strokeColor",
		"backgroundColor", "fillStyle", "strokeWidth", "strokeStyle", "roughness",
		"opacity", "groupIds", "frameId", "index", "roundness", "seed", "version",
		"versionNonce", "isDeleted", "boundElements", "updated", "link", "locked",
		"customData", "text", "fontSize", "fontFamily", "textAlign", "verticalAlign",
		"containerId", "originalText", "autoResize", "lineHeight",
	}
	if got := jsonKeys(t, raw); !reflect.DeepEqual(got, wantOrder) {
		t.Errorf("element keys = %v, want %v", got, wantOrder)
	}

	width, height := TextSize("hello", 20)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]any{
		"id": el.ID, "type": "text", "x": 10.0, "y": 20.0,
		"width": width, "height": height, "angle": 0.0,
		"strokeColor": "#b48cf2", "backgroundColor": "transparent", "fillStyle": "solid",
		"strokeWidth": 2.0, "strokeStyle": "solid", "roughness": 0.0, "opacity": 100.0,
		"groupIds": []any{}, "frameId": nil, "index": nil, "roundness": nil,
		"seed": 0.0, "version": 1.0, "versionNonce": 0.0, "isDeleted": false,
		"boundElements": nil, "updated": 1700000000000.0, "link": nil, "locked": false,
		"customData": map[string]any{
			"relevo": map[string]any{"comment": true, "by": "human", "at": "2026-10-01T12:00:00Z"},
		},
		"text": "hello", "fontSize": 20.0, "fontFamily": 3.0, "textAlign": "left",
		"verticalAlign": "top", "containerId": nil, "originalText": "hello",
		"autoResize": true, "lineHeight": 1.2,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("element = %#v\nwant %#v", got, want)
	}

	if !el.AutoResize {
		t.Error("autoResize = false, want true (font safety)")
	}
	if wantW, _ := TextSize(el.Text, el.FontSize); el.Width != wantW {
		t.Errorf("width = %v, want TextSize width %v", el.Width, wantW)
	}
	if el.FontFamily != FontFamily {
		t.Errorf("fontFamily = %d, want %d", el.FontFamily, FontFamily)
	}
}

// TestTextElementWithoutMarker: a nil Marker leaves customData null, so S2's
// annotate reuses the builder without the comment layer.
func TestTextElementWithoutMarker(t *testing.T) {
	el, err := NewTextElement(TextOptions{Text: "plain", StrokeColor: "#e6e8ec", Updated: 1})
	if err != nil {
		t.Fatalf("NewTextElement: %v", err)
	}
	raw, err := json.Marshal(el)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		CustomData *CommentMarker `json:"customData"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.CustomData != nil {
		t.Errorf("customData = %+v, want null", got.CustomData)
	}
}

// TestTextSizeEstimate pins the estimate table: 0.6 em per narrow rune, 1.0
// em for wide and emoji runes, one line height per line, both ceiled, and CRLF
// and CR folded onto LF first.
func TestTextSizeEstimate(t *testing.T) {
	cases := []struct {
		text string
		font float64
		w, h float64
	}{
		{"", 20, 0, 24},
		{"abc", 20, 36, 24},
		{"a\nbb", 20, 24, 48},
		{"a\r\nb", 20, 12, 48},
		{"a\rb", 20, 12, 48},
		{"中", 20, 20, 24},
		{"😀", 20, 20, 24},
		{"aaaa", 20, 48, 24},
		{"abc", 10, 18, 12},
	}
	for _, tc := range cases {
		w, h := TextSize(tc.text, tc.font)
		if w != tc.w || h != tc.h {
			t.Errorf("TextSize(%q, %v) = %v, %v; want %v, %v", tc.text, tc.font, w, h, tc.w, tc.h)
		}
	}
}
