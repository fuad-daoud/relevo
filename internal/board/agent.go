package board

import (
	"encoding/json"
	"fmt"
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