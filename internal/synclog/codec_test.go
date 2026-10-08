package synclog

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/fuad-daoud/relevo/internal/db"
)

// A compressed round-file body travels as the bytes the file holds. The importer
// binds those bytes into a BLOB column beside the codec column that says what
// they are, so a body that arrived decompressed -- or recompressed differently
// -- would install a row the writer never had.
func TestSyncCodecBlobRoundTrip(t *testing.T) {
	t.Parallel()

	// The frames below are what a zstd encoder writes over the payload; they are
	// opaque bytes to the exchange, which is the point: it must not open them.
	frames := map[string][]byte{
		"long":   zstdBytes(t, bytes.Repeat([]byte("transcript body "), 400)),
		"short":  zstdBytes(t, []byte("tiny")),
		"binary": {0x00, 0x01, 0xff, 0xfe, 0x7f},
		"empty":  {},
	}
	for name, frame := range frames {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			row := db.ExchangeRow{
				Table: "round_file",
				PK:    `["01ABC","body"]`,
				Columns: []db.ExchangeColumn{
					{Name: "record_id", Value: "01ABC"},
					{Name: "name", Value: "body"},
					// The compressed bytes and the codec that describes them
					// both travel, verbatim.
					{Name: "body", Value: frame},
					{Name: "body_codec", Value: int64(1)},
					{Name: "size", Value: int64(len(frame))},
				},
			}
			body, err := EncodeBody(row)
			if err != nil {
				t.Fatalf("EncodeBody: %v", err)
			}
			got, err := DecodeBody(body)
			if err != nil {
				t.Fatalf("DecodeBody: %v", err)
			}
			blob, ok := got["body"].([]byte)
			if !ok {
				t.Fatalf("body column type = %T, want []byte", got["body"])
			}
			if !bytes.Equal(blob, frame) {
				t.Errorf("blob = %x, want %x", blob, frame)
			}
			// The codec column beside it has to arrive as the integer it was, or
			// the importer would store the frame with the wrong codec.
			if codec, ok := got["body_codec"].(int64); !ok || codec != 1 {
				t.Errorf("body_codec = %#v, want int64(1)", got["body_codec"])
			}
		})
	}
}

// A rendered transcript column is text on both sides, and a text column holding
// bytes that look like a blob tag must not come back as bytes either.
func TestSyncCodecTextRoundTripsAsText(t *testing.T) {
	t.Parallel()
	const rendered = `{"$blob":"bm90IGEgYmxvYg=="}`
	row := db.ExchangeRow{
		Table: "transcript",
		PK:    `["01ABC"]`,
		Columns: []db.ExchangeColumn{
			{Name: "id", Value: "01ABC"},
			{Name: "rendered", Value: rendered},
			{Name: "rendered_codec", Value: int64(0)},
		},
	}
	body, err := EncodeBody(row)
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	got, err := DecodeBody(body)
	if err != nil {
		t.Fatalf("DecodeBody: %v", err)
	}
	text, ok := got["rendered"].(string)
	if !ok {
		t.Fatalf("rendered column type = %T, want string", got["rendered"])
	}
	if text != rendered {
		t.Errorf("rendered = %q, want %q", text, rendered)
	}
}

// A value's type decides what the importer binds, and a float that arrives as an
// integer -- or an integer that arrives as text -- installs a row of the wrong
// storage class. Each stored type has to come back as itself.
func TestSyncCodecPreservesTypes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value any
	}{
		{"integer", int64(42)},
		{"negative integer", int64(-7)},
		{"zero", int64(0)},
		// The first integer a float64 cannot hold: rounding it would name a
		// different value than the one exported.
		{"wide integer", int64(9007199254740993)},
		{"real", 2.5},
		{"text", "hello"},
		{"empty text", ""},
		{"blob", []byte{0x00, 0xff, 0x10}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			row := db.ExchangeRow{
				Table:   "binding",
				PK:      `["01ABC"]`,
				Columns: []db.ExchangeColumn{{Name: "value", Value: c.value}},
			}
			body, err := EncodeBody(row)
			if err != nil {
				t.Fatalf("EncodeBody: %v", err)
			}
			got, err := DecodeBody(body)
			if err != nil {
				t.Fatalf("DecodeBody: %v", err)
			}
			if !reflect.DeepEqual(got["value"], c.value) {
				t.Errorf("value = %#v (%T), want %#v (%T)", got["value"], got["value"], c.value, c.value)
			}
		})
	}
}

// A column the writer set to nothing has to stay nothing: an empty string and a
// null are different rows, and the importer's upsert writes whichever it is given.
func TestSyncCodecPreservesNull(t *testing.T) {
	t.Parallel()
	row := db.ExchangeRow{
		Table:   "binding",
		PK:      `["01ABC"]`,
		Columns: []db.ExchangeColumn{{Name: "note", Value: nil}},
	}
	body, err := EncodeBody(row)
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	got, err := DecodeBody(body)
	if err != nil {
		t.Fatalf("DecodeBody: %v", err)
	}
	value, present := got["note"]
	if !present {
		t.Fatal("the null column is missing from the body")
	}
	if value != nil {
		t.Errorf("null column = %#v (%T), want nil", value, value)
	}
}

// Every column of a row travels, in the file's schema order, so an import can
// write the row as the file holds it rather than as a query happened to return
// it.
func TestSyncCodecCarriesEveryColumnInSchemaOrder(t *testing.T) {
	t.Parallel()
	row := db.ExchangeRow{
		Table: "round_file",
		PK:    `["01ABC","body"]`,
		Columns: []db.ExchangeColumn{
			{Name: "record_id", Value: "01ABC"},
			{Name: "name", Value: "body"},
			{Name: "body", Value: []byte("zstd frame")},
			{Name: "body_codec", Value: int64(1)},
		},
	}
	body, err := EncodeBody(row)
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	got, err := DecodeBody(body)
	if err != nil {
		t.Fatalf("DecodeBody: %v", err)
	}
	if len(got) != len(row.Columns) {
		t.Fatalf("body names %d columns, want %d", len(got), len(row.Columns))
	}
	for _, col := range row.Columns {
		if _, ok := got[col.Name]; !ok {
			t.Errorf("column %s is missing from the decoded body", col.Name)
		}
	}
}

// A body was written by another installation, so a key naming a column this
// machine does not have is kept rather than refused here: which of this
// machine's columns to name in SQL is the importer's decision, and dropping the
// key at the codec would lose it before that decision could be made.
func TestSyncCodecKeepsAColumnThisMachineDoesNotHave(t *testing.T) {
	t.Parallel()
	body := json.RawMessage(`{"id":"01ABC","column_from_the_future":"kept"}`)
	got, err := DecodeBody(body)
	if err != nil {
		t.Fatalf("DecodeBody: %v", err)
	}
	if got["column_from_the_future"] != "kept" {
		t.Errorf("unknown column = %#v, want it kept", got["column_from_the_future"])
	}
	if got["id"] != "01ABC" {
		t.Errorf("id = %#v, want 01ABC", got["id"])
	}
}

// A body is untrusted input, so each shape it may not hold is refused rather
// than handed on as a type a caller would have to reason about.
func TestSyncCodecRefusesShapesItDoesNotDefine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{"not json", `not json`},
		{"not an object", `["01ABC"]`},
		{"an array value", `{"tags":["a"]}`},
		// The blob cases are values inside a body: at the top level the tag is
		// a column name and its value is just that column's text.
		{"an object that is not a blob", `{"meta":{"a":1}}`},
		{"a blob with an extra field", `{"body":{"$blob":"YWJj","other":"x"}}`},
		{"a blob that is not base64", `{"body":{"$blob":"!!!"}}`},
		{"a blob that is not text", `{"body":{"$blob":1}}`},
		{"an empty value", `{"id":}`},
		{"a second value", `{"id":"01ABC"} {"id":"02DEF"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeBody(json.RawMessage(c.body)); !errors.Is(err, ErrInvalid) {
				t.Errorf("DecodeBody(%s): err = %v, want ErrInvalid", c.body, err)
			}
		})
	}
}

// A value the file cannot hold is refused at the encoder rather than arriving at
// an importer as a type nothing can bind.
func TestSyncCodecRefusesAValueItCannotStore(t *testing.T) {
	t.Parallel()
	row := db.ExchangeRow{
		Table:   "binding",
		PK:      `["01ABC"]`,
		Columns: []db.ExchangeColumn{{Name: "when", Value: int32(7)}},
	}
	if _, err := EncodeBody(row); !errors.Is(err, ErrInvalid) {
		t.Fatalf("EncodeBody with an int32: err = %v, want ErrInvalid", err)
	}
}

// zstdBytes is what a zstd encoder writes over value, which is what the file
// stores for a body above the size where compression pays.
func zstdBytes(t *testing.T, value []byte) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	return enc.EncodeAll(value, nil)
}
