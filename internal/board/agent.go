package board

import (
	"bytes"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	mrand "math/rand"
	"strings"
	"time"
)

// annotateGap is the distance the default position leaves below the scene's
// current bounds: Excalidraw's DEFAULT_GRID_SIZE.
const annotateGap = 20

// The two text metrics the element builder emits, from Cascadia's advance and
// 0.18.1's DEFAULT_FONT_SIZE and line height: a twelve-pixel cell per byte and
// twenty-four per line.
const (
	textCellWidth  = 12
	textLineHeight = 24
)

// TextElement is one text element as the agent leg reads it: the four fields a
// caller needs to place and describe a label, in the order the JSON document
// carries them.
type TextElement struct {
	ID   string  `json:"id"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Text string  `json:"text"`
}

// sceneElement is one element as the agent leg decodes it. The four numeric
// fields are pointers so a wrong type is an invalid scene while an absent one
// is zero; text defaults to the empty string.
type sceneElement struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	X         *float64 `json:"x"`
	Y         *float64 `json:"y"`
	Width     *float64 `json:"width"`
	Height    *float64 `json:"height"`
	Text      string   `json:"text"`
	IsDeleted bool     `json:"isDeleted"`
}

// num returns a pointer's value, or zero when the scene omitted the field.
func num(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// notFoundError carries ErrNotFound while printing the refusal text alone.
type notFoundError struct{ msg string }

func (e *notFoundError) Error() string { return e.msg }
func (e *notFoundError) Unwrap() error { return ErrNotFound }

// decodeElements validates body as a scene and decodes every element. A wrong
// field type is ErrInvalid; a missing scene is the caller's concern, so this
// reads bytes only.
func decodeElements(data []byte) ([]sceneElement, error) {
	if err := ValidateScene(data); err != nil {
		return nil, err
	}
	var doc struct {
		Elements []json.RawMessage `json:"elements"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: scene is not a JSON object: %w", ErrInvalid, err)
	}
	out := make([]sceneElement, 0, len(doc.Elements))
	for _, raw := range doc.Elements {
		var el sceneElement
		if err := json.Unmarshal(raw, &el); err != nil {
			return nil, fmt.Errorf("%w: element is not a JSON object: %w", ErrInvalid, err)
		}
		out = append(out, el)
	}
	return out, nil
}

// TextElements lists the scene's non-deleted text elements in array order. A
// missing scene is ErrNotFound, never a new scene.
func TextElements(path string) ([]TextElement, error) {
	data, _, isNew, err := Load(path)
	if err != nil {
		return nil, err
	}
	if isNew {
		return nil, &notFoundError{msg: "no scene at " + path}
	}
	els, err := decodeElements(data)
	if err != nil {
		return nil, err
	}
	out := make([]TextElement, 0)
	for _, el := range els {
		if el.IsDeleted || el.Type != "text" {
			continue
		}
		out = append(out, TextElement{ID: el.ID, X: num(el.X), Y: num(el.Y), Text: el.Text})
	}
	return out, nil
}

// AnnotateOptions is what one annotation asks for. HasX and HasY record
// whether the caller gave the coordinate, so a lone member and a zero are
// different requests. Theme supplies the stroke colour.
type AnnotateOptions struct {
	Text  string
	X     float64
	Y     float64
	HasX  bool
	HasY  bool
	Theme *Theme
}

// annotateElement is the appended element, field for field in 0.18.1's order.
// Every value is ours; customData is absent because 0.18.1 leaves it
// undefined.
type annotateElement struct {
	ID              string   `json:"id"`
	Type            string   `json:"type"`
	X               float64  `json:"x"`
	Y               float64  `json:"y"`
	Width           float64  `json:"width"`
	Height          float64  `json:"height"`
	Angle           float64  `json:"angle"`
	StrokeColor     string   `json:"strokeColor"`
	BackgroundColor string   `json:"backgroundColor"`
	FillStyle       string   `json:"fillStyle"`
	StrokeWidth     float64  `json:"strokeWidth"`
	StrokeStyle     string   `json:"strokeStyle"`
	Roughness       float64  `json:"roughness"`
	Opacity         float64  `json:"opacity"`
	GroupIDs        []string `json:"groupIds"`
	FrameID         *string  `json:"frameId"`
	Index           *string  `json:"index"`
	Roundness       any      `json:"roundness"`
	Seed            int64    `json:"seed"`
	Version         int      `json:"version"`
	VersionNonce    int      `json:"versionNonce"`
	IsDeleted       bool     `json:"isDeleted"`
	BoundElements   any      `json:"boundElements"`
	Updated         int64    `json:"updated"`
	Link            any      `json:"link"`
	Locked          bool     `json:"locked"`
	Text            string   `json:"text"`
	FontSize        float64  `json:"fontSize"`
	FontFamily      int      `json:"fontFamily"`
	TextAlign       string   `json:"textAlign"`
	VerticalAlign   string   `json:"verticalAlign"`
	ContainerID     *string  `json:"containerId"`
	OriginalText    string   `json:"originalText"`
	AutoResize      bool     `json:"autoResize"`
	LineHeight      float64  `json:"lineHeight"`
}

// The three non-deterministic values, each behind a seam so a board test pins
// the element byte-for-byte and a cmd test only shape-checks the result.
var (
	newElementID = func() (string, error) {
		b := make([]byte, 16)
		if _, err := crand.Read(b); err != nil {
			return "", err
		}
		return hex.EncodeToString(b), nil
	}
	newElementSeed = func() int64 { return mrand.Int63n(1 << 31) }
	elementNow     = func() int64 { return time.Now().UnixMilli() }
)

// normalizeText applies the two rewrites 0.18.1 makes before it measures: a tab
// becomes eight spaces and every line ending becomes a newline.
func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\t", "        ")
	return s
}

// estimateTextWidth is twelve pixels per byte of the widest line. Byte
// counting over-counts every wider script, so the estimate errs upward; an
// empty line counts as one space, as 0.18.1's measureText does.
func estimateTextWidth(s string) float64 {
	max := 1
	for _, line := range strings.Split(normalizeText(s), "\n") {
		if n := len(line); n > max {
			max = n
		}
	}
	return float64(textCellWidth * max)
}

// estimateTextHeight is twenty-four pixels per line.
func estimateTextHeight(s string) float64 {
	return float64(textLineHeight * len(strings.Split(normalizeText(s), "\n")))
}

// buildElementBytes marshals the one element an annotation appends: a text
// element at (x, y) in theme's ink, sized by the estimate. The three
// non-deterministic values come from their seams. It returns the minted id
// beside the bytes, so a caller can report it.
func buildElementBytes(text string, x, y float64, theme *Theme) (string, []byte, error) {
	id, err := newElementID()
	if err != nil {
		return "", nil, err
	}
	raw, err := json.Marshal(annotateElement{
		ID:              id,
		Type:            "text",
		X:               x,
		Y:               y,
		Width:           estimateTextWidth(text),
		Height:          estimateTextHeight(text),
		Angle:           0,
		StrokeColor:     theme.Ink,
		BackgroundColor: "transparent",
		FillStyle:       "solid",
		StrokeWidth:     2,
		StrokeStyle:     "solid",
		Roughness:       Roughness,
		Opacity:         100,
		GroupIDs:        []string{},
		Seed:            newElementSeed(),
		Version:         1,
		VersionNonce:    0,
		IsDeleted:       false,
		Updated:         elementNow(),
		Locked:          false,
		Text:            text,
		FontSize:        20,
		FontFamily:      FontFamily,
		TextAlign:       "left",
		VerticalAlign:   "top",
		OriginalText:    text,
		AutoResize:      true,
		LineHeight:      1.2,
	})
	if err != nil {
		return "", nil, err
	}
	return id, raw, nil
}

// elementsClose locates the last top-level "elements" array and the offset
// just after its last non-whitespace byte before the closing bracket, where an
// appended element's bytes belong. empty reports an array with nothing in it,
// which takes no comma.
func elementsClose(data []byte) (at int, empty bool, err error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return 0, false, fmt.Errorf("%w: scene is not a JSON object: %w", ErrInvalid, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return 0, false, fmt.Errorf("%w: scene is not a JSON object", ErrInvalid)
	}
	found := false
	start, end := 0, 0
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return 0, false, err
		}
		key, _ := keyTok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return 0, false, err
		}
		if key == "elements" {
			end = int(dec.InputOffset())
			start = end - len(raw)
			found = true
		}
	}
	if !found {
		return 0, false, fmt.Errorf("%w: scene has no elements", ErrInvalid)
	}
	val := data[start:end]
	closeIdx := bytes.LastIndexByte(val, ']')
	if closeIdx < 0 {
		return 0, false, fmt.Errorf("%w: scene elements is not an array", ErrInvalid)
	}
	j := closeIdx - 1
	for j >= 0 && (val[j] == ' ' || val[j] == '\t' || val[j] == '\r' || val[j] == '\n') {
		j--
	}
	if j < 0 {
		return 0, false, fmt.Errorf("%w: scene elements is not an array", ErrInvalid)
	}
	return start + j + 1, val[j] == '[', nil
}

// spliceElement inserts element into the scene's last top-level elements array
// without touching any other byte.
func spliceElement(data, element []byte) ([]byte, error) {
	at, empty, err := elementsClose(data)
	if err != nil {
		return nil, err
	}
	ins := element
	if !empty {
		ins = append([]byte(","), element...)
	}
	out := make([]byte, 0, len(data)+len(ins))
	out = append(out, data[:at]...)
	out = append(out, ins...)
	out = append(out, data[at:]...)
	return out, nil
}

// defaultPosition is where an element lands with no coordinates: left-aligned
// with the scene's leftmost element and one gap below its lowest edge. An
// empty scene is the origin; deleted elements never move it.
func defaultPosition(els []sceneElement) (float64, float64) {
	minX := math.Inf(1)
	maxY := math.Inf(-1)
	for _, el := range els {
		if el.IsDeleted {
			continue
		}
		if x := num(el.X); x < minX {
			minX = x
		}
		if bottom := num(el.Y) + num(el.Height); bottom > maxY {
			maxY = bottom
		}
	}
	if math.IsInf(minX, 1) {
		return 0, 0
	}
	return minX, maxY + annotateGap
}

// Annotate appends one text element to the scene at path and returns it. A
// missing scene is ErrNotFound, a non-finite coordinate or a malformed request
// is ErrUsage, and the rest of the scene keeps every byte.
func Annotate(path string, opts AnnotateOptions) (TextElement, error) {
	if opts.Text == "" {
		return TextElement{}, usagef("--text must not be empty")
	}
	if opts.HasX != opts.HasY {
		return TextElement{}, usagef("--x and --y come together")
	}
	if opts.HasX && (math.IsNaN(opts.X) || math.IsInf(opts.X, 0)) {
		return TextElement{}, usagef("--x must be a finite number")
	}
	if opts.HasY && (math.IsNaN(opts.Y) || math.IsInf(opts.Y, 0)) {
		return TextElement{}, usagef("--y must be a finite number")
	}
	if opts.Theme == nil {
		return TextElement{}, usagef("annotate needs a theme")
	}

	data, _, isNew, err := Load(path)
	if err != nil {
		return TextElement{}, err
	}
	if isNew {
		return TextElement{}, &notFoundError{msg: "no scene at " + path}
	}
	els, err := decodeElements(data)
	if err != nil {
		return TextElement{}, err
	}

	x, y := opts.X, opts.Y
	if !opts.HasX {
		x, y = defaultPosition(els)
	}

	id, raw, err := buildElementBytes(opts.Text, x, y, opts.Theme)
	if err != nil {
		return TextElement{}, err
	}
	spliced, err := spliceElement(data, raw)
	if err != nil {
		return TextElement{}, err
	}
	if err := writeAtomic(path, spliced); err != nil {
		return TextElement{}, err
	}
	return TextElement{ID: id, X: x, Y: y, Text: opts.Text}, nil
}
