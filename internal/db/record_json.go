package db

import (
	"bytes"
	"encoding/json"
)

func encodeJSONString(s string) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSpace(buf.Bytes())
}

// mapObject applies fn to each member of one JSON object, in order, and
// re-emits the object with the members' raw text otherwise untouched. ok is
// false when raw is not one object.
func mapObject(raw []byte, fn func(key string, val json.RawMessage) (json.RawMessage, bool)) (out []byte, changed, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false, false
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for first := true; dec.More(); first = false {
		keyTok, err := dec.Token()
		key, isStr := keyTok.(string)
		if err != nil || !isStr {
			return nil, false, false
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, false, false
		}
		next, did := fn(key, val)
		changed = changed || did
		if !first {
			buf.WriteByte(',')
		}
		buf.Write(encodeJSONString(key))
		buf.WriteByte(':')
		buf.Write(next)
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false, false
	}
	buf.WriteByte('}')
	return buf.Bytes(), changed, true
}
