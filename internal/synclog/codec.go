package synclog

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

// blobTag marks a BLOB's base64 text as bytes. JSON has no byte type, so without
// the tag the importer could not tell a compressed body from text merely looking
// like one.
const blobTag = "$blob"

// RefValue is a column value not in the entry: the importer fetches the bytes
// before applying. It is its own type because a bare ref object would be
// indistinguishable from a structured column value.
type RefValue struct{ BlobRef }

// EncodeBody turns one file row into the body an entry carries. A NULL stays
// JSON null, so nothing is distinguishable from never set. The row is encoded
// exactly as stored, so an arriving body is byte-identical to the one that left.
func EncodeBody(row db.ExchangeRow) (json.RawMessage, error) {
	values := make(map[string]json.RawMessage, len(row.Columns))
	for _, col := range row.Columns {
		encoded, err := encodeValue(col.Value)
		if err != nil {
			return nil, fmt.Errorf("synclog: encode %s %s column %s: %w", row.Table, row.PK, col.Name, err)
		}
		values[col.Name] = encoded
	}
	body, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("synclog: encode %s %s: %w", row.Table, row.PK, err)
	}
	return body, nil
}

// encodeValue renders one stored value as JSON. Types are named, not reflected,
// so an unstorable value is refused here rather than arriving as an unbindable one.
func encodeValue(value any) (json.RawMessage, error) {
	switch v := value.(type) {
	case nil:
		return json.RawMessage("null"), nil
	case string:
		return json.Marshal(v)
	case int64:
		return json.Marshal(v)
	case float64:
		return json.Marshal(v)
	case []byte:
		// The tag is what tells this apart from text on the way back, so it is
		// written here rather than left to the reader to guess.
		return json.Marshal(map[string]string{blobTag: base64.StdEncoding.EncodeToString(v)})
	case RefValue:
		return EncodeRef(v.BlobRef), nil
	default:
		return nil, fmt.Errorf("%T is not a storable column value: %w", value, ErrInvalid)
	}
}

// DecodeBody turns an entry's body into the row's columns. Every key is kept,
// even undeclared ones: the importer knows which columns to name in SQL, so
// unknown keys drop there, not here.
func DecodeBody(body json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	// Numbers are read as written: float64 would round an integer wider than 2^53.
	dec.UseNumber()
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("synclog: decode body: %w: %w", err, ErrInvalid)
	}
	// The decoder stops at the object's end, so trailing bytes would otherwise
	// decode as the prefix, and the importer would act on half a body.
	if expectEOF(dec) != nil {
		return nil, fmt.Errorf("synclog: decode body: text after the object: %w", ErrInvalid)
	}
	out := make(map[string]any, len(raw))
	for name, value := range raw {
		decoded, err := decodeValue(value)
		if err != nil {
			return nil, fmt.Errorf("synclog: decode body column %s: %w", name, err)
		}
		out[name] = decoded
	}
	return out, nil
}

// decodeValue renders one JSON value as the stored type. A body is untrusted
// input, so anything else is refused rather than passed on.
func decodeValue(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("body value is empty: %w", ErrInvalid)
	}
	switch trimmed[0] {
	case '{':
		return decodeObject(trimmed)
	case 'n':
		if string(trimmed) != "null" {
			return nil, fmt.Errorf("body value %s is not null: %w", trimmed, ErrInvalid)
		}
		return nil, nil
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil, fmt.Errorf("body value %s is not text: %w: %w", trimmed, err, ErrInvalid)
		}
		return s, nil
	default:
		return decodeNumber(trimmed)
	}
}

// decodeObject reads one tagged value: $blob says the bytes are here, $ref says
// they are in the store. Only one leaves the importer with a bindable value.
// Both require exactly the tag and nothing beside it, so no body smuggles a
// structure past the reader.
func decodeObject(raw json.RawMessage) (any, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("body value %s is not an object: %w: %w", raw, err, ErrInvalid)
	}
	if len(fields) != 1 {
		return nil, fmt.Errorf("body value %s is neither a blob nor a ref: %w", raw, ErrInvalid)
	}
	if _, ok := fields[refTag]; ok {
		ref, _, err := DecodeRef(raw)
		if err != nil {
			return nil, fmt.Errorf("body value %s is not a ref: %w", raw, err)
		}
		return RefValue{BlobRef: ref}, nil
	}
	return decodeBlob(raw)
}

// decodeBlob returns the bytes a tagged BLOB carries, refusing any other object
// so no body smuggles a structure past the reader.
func decodeBlob(raw json.RawMessage) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("body value %s is not an object: %w: %w", raw, err, ErrInvalid)
	}
	if len(fields) != 1 {
		return nil, fmt.Errorf("body value %s is not a blob: %w", raw, ErrInvalid)
	}
	encoded, ok := fields[blobTag]
	if !ok {
		return nil, fmt.Errorf("body value %s is not a blob: %w", raw, ErrInvalid)
	}
	var text string
	if err := json.Unmarshal(encoded, &text); err != nil {
		return nil, fmt.Errorf("blob value %s is not base64 text: %w: %w", raw, err, ErrInvalid)
	}
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("blob value %s is not base64: %w: %w", raw, err, ErrInvalid)
	}
	return decoded, nil
}

// decodeNumber binds an integral number as an integer and any other as a real,
// so a column's storage class survives.
func decodeNumber(raw json.RawMessage) (any, error) {
	num := json.Number(string(raw))
	if i, err := num.Int64(); err == nil {
		return i, nil
	}
	f, err := num.Float64()
	if err != nil {
		return nil, fmt.Errorf("body value %s is not a number: %w: %w", raw, err, ErrInvalid)
	}
	return f, nil
}
