package board

import (
	"crypto/rand"
	"encoding/base64"
	"math"
	"strings"
)

// lineHeight is Cascadia's line metric in the vendored Excalidraw bundle: the
// ratio of a line's box height to its font size. TextSize multiplies by it.
const lineHeight = 1.2

// narrowEm is the per-rune em advance TextSize charges a narrow character.
// Cascadia's own advance is 1200/2048 ≈ 0.586 em, so 0.6 is a ceiling that
// never underestimates a line's width.
const narrowEm = 0.6

// TextElement is the vendored 0.18.1 text element, with its JSON fields in the
// order Excalidraw's own serializer writes them so a scene diff stays small.
// Every field is always present; the pointer-shaped ones marshal as null.
type TextElement struct {
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
	FrameID         any      `json:"frameId"`
	Index           any      `json:"index"`
	Roundness       any      `json:"roundness"`
	Seed            float64  `json:"seed"`
	Version         int      `json:"version"`
	VersionNonce    float64  `json:"versionNonce"`
	IsDeleted       bool     `json:"isDeleted"`
	BoundElements   any      `json:"boundElements"`
	Updated         int64    `json:"updated"`
	Link            any      `json:"link"`
	Locked          bool     `json:"locked"`
	CustomData      any      `json:"customData"`
	Text            string   `json:"text"`
	FontSize        float64  `json:"fontSize"`
	FontFamily      int      `json:"fontFamily"`
	TextAlign       string   `json:"textAlign"`
	VerticalAlign   string   `json:"verticalAlign"`
	ContainerID     any      `json:"containerId"`
	OriginalText    string   `json:"originalText"`
	AutoResize      bool     `json:"autoResize"`
	LineHeight      float64  `json:"lineHeight"`
}

// CommentMarker is the customData.relevo object a comment element carries. It
// is the one key the comment writer adds under relevo; extra keys inside
// customData or inside relevo are ignored by the reader and never rewritten.
type CommentMarker struct {
	Comment bool   `json:"comment"`
	By      string `json:"by"`
	At      string `json:"at"`
}

// TextOptions is one text element to build. A nil Marker builds a plain text
// element, so annotate reuses the builder without the comment layer.
type TextOptions struct {
	Text        string
	X, Y        float64
	FontSize    float64
	StrokeColor string
	Updated     int64
	Marker      *CommentMarker
}

// NewTextElement builds a font-safe text element: autoResize on, an explicit
// width and height from TextSize, Cascadia's font family. The id is twenty
// random URL-safe characters.
func NewTextElement(opts TextOptions) (TextElement, error) {
	fontSize := opts.FontSize
	if fontSize <= 0 {
		fontSize = 20
	}
	width, height := TextSize(opts.Text, fontSize)
	id, err := randomID(20)
	if err != nil {
		return TextElement{}, err
	}
	var customData any
	if opts.Marker != nil {
		customData = map[string]any{"relevo": *opts.Marker}
	}
	return TextElement{
		ID:              id,
		Type:            "text",
		X:               opts.X,
		Y:               opts.Y,
		Width:           width,
		Height:          height,
		StrokeColor:     opts.StrokeColor,
		BackgroundColor: "transparent",
		FillStyle:       "solid",
		StrokeWidth:     2,
		StrokeStyle:     "solid",
		Roughness:       Roughness,
		Opacity:         100,
		GroupIDs:        []string{},
		Seed:            0,
		Version:         1,
		VersionNonce:    0,
		Updated:         opts.Updated,
		CustomData:      customData,
		Text:            opts.Text,
		FontSize:        fontSize,
		FontFamily:      FontFamily,
		TextAlign:       "left",
		VerticalAlign:   "top",
		OriginalText:    opts.Text,
		AutoResize:      true,
		LineHeight:      lineHeight,
	}, nil
}

// TextSize estimates a text's rendered size in Cascadia: 0.6 em per narrow
// rune and 1.0 em per East Asian wide or emoji rune on the widest line, and
// one line height per line. Both dimensions are ceiled, so the estimate never
// underestimates and the text is never clipped.
func TextSize(text string, fontSize float64) (width, height float64) {
	lines := strings.Split(normalizeNewlines(text), "\n")
	var widest float64
	for _, line := range lines {
		var w float64
		for _, r := range line {
			if wideRune(r) {
				w += fontSize
			} else {
				w += narrowEm * fontSize
			}
		}
		if w > widest {
			widest = w
		}
	}
	return math.Ceil(widest), math.Ceil(float64(len(lines)) * fontSize * lineHeight)
}

// normalizeNewlines folds CRLF and a lone CR onto LF, so a Windows-typed
// comment measures and renders the same as a Unix one.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// wideRune reports whether r occupies a full em: the East Asian wide and
// fullwidth ranges and the emoji blocks. The stdlib carries no East Asian
// width property, so the ranges are spelled here.
func wideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E,   // CJK radicals, Kangxi, CJK symbols
		r >= 0x3041 && r <= 0x33FF,   // Hiragana, Katakana, CJK compatibility
		r >= 0x3400 && r <= 0x4DBF,   // CJK unified extension A
		r >= 0x4E00 && r <= 0x9FFF,   // CJK unified ideographs
		r >= 0xA000 && r <= 0xA4CF,   // Yi
		r >= 0xAC00 && r <= 0xD7A3,   // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF,   // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE4F,   // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60,   // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,   // fullwidth signs
		r >= 0x1F300 && r <= 0x1FAFF, // emoji
		r >= 0x20000 && r <= 0x3FFFD: // CJK unified extensions B and beyond
		return true
	}
	return false
}

// randomID mints n random URL-safe characters.
func randomID(n int) (string, error) {
	b := make([]byte, (n*3+3)/4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b)[:n], nil
}
