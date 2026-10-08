package synclog

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

// blobTag is the one JSON shape that is not a column value: a BLOB travels as a
// single-key object carrying its base64 text. JSON has no byte type, so without
// the tag a blob would arrive as the same string a TEXT column holding base64
// would, and the importer could not tell a compressed transcript body from text
// that merely looks like one.
const blobTag = "$blob"

// EncodeBody turns one row read from the file into the body an entry carries:
// every column under its own name, in the type the file stored it as. A NULL
// stays a JSON null so a column set to nothing is distinguishable from one that
// was never set.
//
// The row is encoded exactly as stored. The compressed bodies of migration 013
// travel as their bytes with their codec column beside them, so a body that
// arrives is byte-identical to the one that left and no re-compression can
// decide differently than the writer did.
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

// encodeValue renders one stored value as JSON. The stored types are named
// rather than reflected over so a value the file cannot hold is refused here
// instead of arriving at an importer as a type nothing can bind.
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
	default:
		return nil, fmt.Errorf("%T is not a storable column value: %w", value, ErrInvalid)
	}
}

// DecodeBody turns an entry's body into the row's columns. Every key in the body
// is kept, including one this machine's schema does not declare: the body was
// written by another installation and the importer is what knows which of this
// machine's columns to name in SQL, so a key it does not recognise is dropped
// there rather than here.
func DecodeBody(body json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	// Numbers are read as written. Going through float64 would round an integer
	// wider than 2^53 before the importer ever sees it.
	dec.UseNumber()
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("synclog: decode body: %w: %w", err, ErrInvalid)
	}
	// The decoder stops at the end of the object, so a body with a second value
	// or trailing bytes behind it would otherwise decode as its own prefix. An
	// importer that bound such a body would be acting on half of it.
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

// decodeValue renders one JSON value back as the type the file stored it as: a
// blob as bytes, an integral number as an integer, null as nil. A body is
// untrusted input, so anything else is refused rather than passed on as a type
// the caller would have to reason about.
func decodeValue(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("body value is empty: %w", ErrInvalid)
	}
	switch trimmed[0] {
	case '{':
		return decodeBlob(trimmed)
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

// decodeBlob returns the bytes a tagged BLOB carries. An object is only a blob
// when it is exactly the tag and a string: any other object is refused, so a
// body cannot smuggle a structure past a reader that would bind its contents.
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

// decodeNumber binds an integral number as the integer its column holds and any
// other number as a real, so a column's storage class survives the exchange.
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
