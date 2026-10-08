package synclog

import (
	"encoding/json"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Reconcile compares a row read locally against the hash its origin's `head`
// carries. The hash therefore has to be a value of the row rather than of the
// bytes that happened to describe it: the same row written by two machines
// compares equal, and a row changed in one place compares different.
func TestSyncBodyHashIsStable(t *testing.T) {
	t.Parallel()

	row := db.ExchangeRow{
		Table: "round_file",
		PK:    `["01ABC","body"]`,
		Columns: []db.ExchangeColumn{
			{Name: "record_id", Value: "01ABC"},
			{Name: "name", Value: "body"},
			{Name: "body", Value: []byte("zstd frame")},
			{Name: "body_codec", Value: int64(1)},
			{Name: "size", Value: int64(9)},
		},
	}
	body, err := EncodeBody(row)
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	first, err := BodyHash(body)
	if err != nil {
		t.Fatalf("BodyHash: %v", err)
	}
	second, err := BodyHash(body)
	if err != nil {
		t.Fatalf("BodyHash again: %v", err)
	}
	if first != second {
		t.Fatalf("the same body hashed to %s and %s", first, second)
	}

	// The same row read again comes back with its columns in whatever order the
	// schema lists them, and that must not move the digest: a hash that read
	// the column order would call one row two rows and re-export it forever.
	reordered := db.ExchangeRow{
		Table: "round_file",
		PK:    `["01ABC","body"]`,
		Columns: []db.ExchangeColumn{
			{Name: "size", Value: int64(9)},
			{Name: "body_codec", Value: int64(1)},
			{Name: "body", Value: []byte("zstd frame")},
			{Name: "name", Value: "body"},
			{Name: "record_id", Value: "01ABC"},
		},
	}
	sameRow, err := EncodeBody(reordered)
	if err != nil {
		t.Fatalf("EncodeBody reordered: %v", err)
	}
	reorderedHash, err := BodyHash(sameRow)
	if err != nil {
		t.Fatalf("BodyHash reordered: %v", err)
	}
	if reorderedHash != first {
		t.Errorf("the same row in another column order hashed to %s, want %s", reorderedHash, first)
	}
}

// A body that arrives from another installation with its columns named in another
// order describes the same row, so it has to hash the same: reconcile compares
// this against `head` and would otherwise call one row two and re-export it on
// every run.
func TestSyncBodyHashIgnoresTheOrderColumnsArrivedIn(t *testing.T) {
	t.Parallel()

	ordered := json.RawMessage(`{"id":"01ABC","note":"hello","size":9}`)
	reordered := json.RawMessage(`{"size":9,"note":"hello","id":"01ABC"}`)
	spaced := json.RawMessage("{\n  \"note\": \"hello\",\n  \"size\": 9,\n  \"id\": \"01ABC\"\n}")

	first, err := BodyHash(ordered)
	if err != nil {
		t.Fatalf("BodyHash: %v", err)
	}
	for name, body := range map[string]json.RawMessage{"reordered": reordered, "reformatted": spaced} {
		got, err := BodyHash(body)
		if err != nil {
			t.Fatalf("BodyHash %s: %v", name, err)
		}
		if got != first {
			t.Errorf("the %s body hashed to %s, want %s", name, got, first)
		}
	}
}

// One byte changed in one value is a different row, and a digest that missed it
// would leave reconcile convinced the two machines agree.
func TestSyncBodyHashChangesWithTheRow(t *testing.T) {
	t.Parallel()

	changed := []struct {
		name   string
		mutate func(cols []db.ExchangeColumn) []db.ExchangeColumn
	}{
		{"a byte of text", func(cols []db.ExchangeColumn) []db.ExchangeColumn {
			cols[0].Value = "01ABD"
			return cols
		}},
		{"an integer", func(cols []db.ExchangeColumn) []db.ExchangeColumn {
			cols[3].Value = int64(10)
			return cols
		}},
		{"a blob byte", func(cols []db.ExchangeColumn) []db.ExchangeColumn {
			cols[2].Value = []byte("zstd framf")
			return cols
		}},
		{"a null into text", func(cols []db.ExchangeColumn) []db.ExchangeColumn {
			cols[3].Value = nil
			return cols
		}},
	}
	for _, c := range changed {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			base := bodyOf(t, "binding", `["01ABC"]`, []db.ExchangeColumn{
				{Name: "id", Value: "01ABC"},
				{Name: "note", Value: "hello"},
				{Name: "body", Value: []byte("zstd frame")},
				{Name: "size", Value: int64(9)},
			})
			before, err := BodyHash(base)
			if err != nil {
				t.Fatalf("BodyHash: %v", err)
			}
			mutated := bodyOf(t, "binding", `["01ABC"]`, c.mutate([]db.ExchangeColumn{
				{Name: "id", Value: "01ABC"},
				{Name: "note", Value: "hello"},
				{Name: "body", Value: []byte("zstd frame")},
				{Name: "size", Value: int64(9)},
			}))
			after, err := BodyHash(mutated)
			if err != nil {
				t.Fatalf("BodyHash after the change: %v", err)
			}
			if after == before {
				t.Errorf("changing %s left the hash at %s", c.name, before)
			}
		})
	}
}

// A body that cannot be read has no digest, and reporting that is what keeps a
// reconcile from writing an empty hash next to a row.
func TestSyncBodyHashRefusesABodyItCannotRead(t *testing.T) {
	t.Parallel()
	if _, err := BodyHash(json.RawMessage(`not json`)); err == nil {
		t.Fatal("BodyHash of an unreadable body returned no error")
	}
}

// bodyOf encodes one row's columns as a body.
func bodyOf(t *testing.T, tbl, pk string, cols []db.ExchangeColumn) json.RawMessage {
	t.Helper()
	body, err := EncodeBody(db.ExchangeRow{Table: tbl, PK: pk, Columns: cols})
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	return body
}
