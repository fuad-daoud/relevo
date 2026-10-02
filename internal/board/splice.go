package board

import (
	"bytes"
	"encoding/json"
)

// AppendElement inserts element bytes before the top-level elements array's
// closing bracket and leaves every other byte of scene unchanged: no comma
// into an empty array, and nothing re-encoded. The span comes from a JSON
// token walk plus InputOffset, never a whole-document unmarshal, so a scene
// with unknown or non-canonical fields round-trips untouched. A document that
// is not an object whose elements is an array is ErrInvalid.
func AppendElement(scene, element []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(scene))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, ErrInvalid
	}

	close := -1
	empty := false
	for dec.More() {
		ktok, err := dec.Token()
		if err != nil {
			return nil, ErrInvalid
		}
		key, ok := ktok.(string)
		if !ok {
			return nil, ErrInvalid
		}
		if key != "elements" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, ErrInvalid
			}
			continue
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, ErrInvalid
		}
		end := int(dec.InputOffset())
		start := end - len(raw)
		if start < 0 || end > len(scene) || len(raw) == 0 || raw[0] != '[' || raw[len(raw)-1] != ']' {
			return nil, ErrInvalid
		}
		close = end - 1
		empty = len(bytes.TrimSpace(raw[1:len(raw)-1])) == 0
	}
	if close < 0 {
		return nil, ErrInvalid
	}

	out := make([]byte, 0, len(scene)+len(element)+1)
	out = append(out, scene[:close]...)
	if !empty {
		out = append(out, ',')
	}
	out = append(out, element...)
	out = append(out, scene[close:]...)
	return out, nil
}
