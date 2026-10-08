package syncworker

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// wireCase is one value on the wire with the exact line it must encode to.
//
// Both halves are here because neither alone pins anything. A round trip
// compares a value with itself: rename a field's tag and both sides still
// agree with each other, so a round trip alone would pass over the one change
// that breaks a pipe between two processes. The golden line is what catches it,
// and the round trip is what says the line a value encodes to is a line this
// pipe can read back.
type wireCase struct {
	name  string
	value any
	wire  string
}

// pinnedAt is the moment every value below was written, so the golden lines are
// about the wire's field names and not about a clock.
var pinnedAt = time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)

// The wire lines for the two entries the cases carry. A delete's line has no
// body, which is what pins a delete carrying none.
const (
	upsertWire = `{"origin":"origin-a","seq":7,"batch":5,"tbl":"task",` +
		`"pk":"[\"t-1\"]","op":"upsert","schema_version":12,` +
		`"body":{"title":"one","body":"two"},"at":"2026-10-08T09:30:00Z"}`
	deleteWire = `{"origin":"origin-a","seq":8,"batch":7,"tbl":"task",` +
		`"pk":"[\"t-2\"]","op":"delete","schema_version":12,` +
		`"at":"2026-10-08T09:30:00Z"}`
)

func upsertAt() Entry {
	return Entry{
		Origin: "origin-a", Seq: 7, Batch: 5, Tbl: "task", PK: `["t-1"]`,
		Op: "upsert", SchemaVersion: 12,
		Body: json.RawMessage(`{"title":"one","body":"two"}`), At: pinnedAt,
	}
}

func deleteAt() Entry {
	return Entry{
		Origin: "origin-a", Seq: 8, Batch: 7, Tbl: "task", PK: `["t-2"]`,
		Op: "delete", SchemaVersion: 12, At: pinnedAt,
	}
}

// TestProtocolRoundTripPinsEveryRequest pins the wire's own field names by
// reading every request and every reply through JSON and back, and against the
// line each must encode to.
//
// Every verb appears here with every field it carries, because a name nothing
// carries yet is still a name one of the two sides is about to start using.
func TestProtocolRoundTripPinsEveryRequest(t *testing.T) {
	cases := append(requestWireCases(), responseWireCases()...)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertWire(t, c)
			assertRoundTrip(t, c.value)
		})
	}
}

// requestWireCases is one case per verb the daemon sends, each carrying every
// field that verb reads.
func requestWireCases() []wireCase {
	return []wireCase{
		{
			name: "request hello",
			value: Request{
				ID: "1", Verb: VerbHello, Version: ProtocolVersion, Origin: "origin-a",
				Token: "t", URL: "libsql://remote.example",
			},
			wire: `{"id":"1","verb":"hello","version":1,"origin":"origin-a",` +
				`"token":"t","url":"libsql://remote.example"}`,
		},
		{
			name: "request export",
			value: Request{
				ID: "2", Verb: VerbExport, Entries: []Entry{upsertAt(), deleteAt()},
			},
			wire: `{"id":"2","verb":"export","entries":[` + upsertWire + `,` + deleteWire + `]}`,
		},
		{
			name: "request pull",
			value: Request{
				ID: "3", Verb: VerbPull, Marks: map[string]int{"origin-b": 4, "origin-c": 0},
			},
			wire: `{"id":"3","verb":"pull","marks":{"origin-b":4,"origin-c":0}}`,
		},
		{
			name: "request head",
			value: Request{
				ID: "4", Verb: VerbHead, Origin: "origin-a", After: `["t-1"]`, Limit: 25,
			},
			wire: `{"id":"4","verb":"head","origin":"origin-a",` +
				`"after":"[\"t-1\"]","limit":25}`,
		},
		{
			name:  "request stats",
			value: Request{ID: "5", Verb: VerbStats},
			wire:  `{"id":"5","verb":"stats"}`,
		},
		{
			name:  "request shutdown",
			value: Request{ID: "6", Verb: VerbShutdown},
			wire:  `{"id":"6","verb":"shutdown"}`,
		},
	}
}

// responseWireCases is one case per shape a reply takes, so a field a reply
// gained and one a reply lost are both caught.
func responseWireCases() []wireCase {
	stats := Stats{Entries: 11, Origins: 2, Seq: 9}
	return []wireCase{
		{
			name:  "response carrying entries",
			value: Response{ID: "2", OK: true, Entries: []Entry{upsertAt()}},
			wire:  `{"id":"2","ok":true,"entries":[` + upsertWire + `]}`,
		},
		{
			name:  "response carrying a refusal",
			value: Response{ID: "3", OK: false, Error: "no hello yet"},
			wire:  `{"id":"3","ok":false,"error":"no hello yet"}`,
		},
		{
			name: "response carrying a head page",
			value: Response{ID: "4", OK: true, Head: []HeadRow{
				{Tbl: "task", PK: `["t-1"]`, Seq: 7, Hash: "abc123"},
			}},
			wire: `{"id":"4","ok":true,"head":[{"tbl":"task","pk":"[\"t-1\"]",` +
				`"seq":7,"hash":"abc123"}]}`,
		},
		{
			name:  "response carrying stats",
			value: Response{ID: "5", OK: true, Stats: &stats},
			wire:  `{"id":"5","ok":true,"stats":{"entries":11,"origins":2,"seq":9}}`,
		},
		{
			name:  "response to a shutdown",
			value: Response{ID: "6", OK: true},
			wire:  `{"id":"6","ok":true}`,
		},
	}
}

// assertWire compares what a value encodes to with the line the pipe must carry.
func assertWire(t *testing.T, c wireCase) {
	t.Helper()
	line, err := json.Marshal(c.value)
	if err != nil {
		t.Fatalf("marshal %s: %v", c.name, err)
	}
	if got := string(line); got != c.wire {
		t.Errorf("wire form:\n got %s\nwant %s", got, c.wire)
	}
}

// assertRoundTrip decodes what v encoded back into the same type and compares,
// so a field that would not survive the wire fails here even where the golden
// line was written to match it.
//
// The type is recovered from the value rather than a type parameter, because the
// cases are held in one table: a parameter inferred from an `any` would decode
// into a map and compare a row against a map.
func assertRoundTrip(t *testing.T, v any) {
	t.Helper()
	line, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	back := reflect.New(reflect.TypeOf(v))
	if err := json.Unmarshal(line, back.Interface()); err != nil {
		t.Fatalf("unmarshal %T: %v", v, err)
	}
	if !reflect.DeepEqual(v, back.Elem().Interface()) {
		t.Errorf("%T did not survive the wire:\n sent %#v\n read %#v", v, v, back.Elem().Interface())
	}
}
