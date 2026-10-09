package synclog

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// syncEntry builds an upsert whose body names every column the test gives it, so
// a test states only the columns it cares about.
func syncEntry(t *testing.T, origin, tbl, pk string, cols map[string]any) Entry {
	t.Helper()
	row := db.ExchangeRow{Table: tbl, PK: pk}
	for name, value := range cols {
		row.Columns = append(row.Columns, db.ExchangeColumn{Name: name, Value: value})
	}
	body, err := EncodeBody(row)
	if err != nil {
		t.Fatalf("encode body for %s %s: %v", tbl, pk, err)
	}
	e, err := NewUpsert(origin, tbl, pk, 25, body, syncClock())
	if err != nil {
		t.Fatalf("new upsert for %s %s: %v", tbl, pk, err)
	}
	return e
}

// syncDelete builds a delete entry for a row named by its key.
func syncDelete(t *testing.T, origin, tbl, pk string) Entry {
	t.Helper()
	e, err := NewDelete(origin, tbl, pk, 25, syncClock())
	if err != nil {
		t.Fatalf("new delete for %s %s: %v", tbl, pk, err)
	}
	return e
}

// syncClock is the moment every test's entries claim to have been written: the
// log orders entries by sequence, not by time, so a fixed clock keeps a failure
// about one property from reading as a difference in another.
func syncClock() time.Time { return time.Unix(1700000000, 0).UTC() }

// A delete names a row by its key and nothing else, so an importer can apply it
// without reading a body it would then have to trust over the key.
func TestSyncDeleteCarriesNoBody(t *testing.T) {
	t.Parallel()
	e := syncDelete(t, "m1", "round_file", `["01ABC","body"]`)

	if len(e.Body) != 0 {
		t.Errorf("delete body = %s, want empty", e.Body)
	}
	if e.Op != OpDelete {
		t.Errorf("op = %q, want %q", e.Op, OpDelete)
	}
}

// An upsert without a body would import as a row with no columns, so it is
// refused before it can reach a transport.
func TestSyncUpsertRefusesNoBody(t *testing.T) {
	t.Parallel()
	if _, err := NewUpsert("m1", "binding", `["01ABC"]`, 25, nil, syncClock()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("upsert with no body: err = %v, want ErrInvalid", err)
	}
}

// A delete carrying a body holds two answers to which state the row is in, and
// only the key is one the log defines.
func TestSyncDeleteRefusesABody(t *testing.T) {
	t.Parallel()
	built := Entry{Op: OpDelete, Table: "binding", PK: `["01ABC"]`, Body: json.RawMessage(`{"id":"01ABC"}`)}
	if err := checkShape(built); !errors.Is(err, ErrInvalid) {
		t.Fatalf("delete carrying a body: err = %v, want ErrInvalid", err)
	}
}

// An op the log does not define has no meaning to an importer, so it is refused
// at the point the entry is built.
func TestSyncEntryRefusesAnUnknownOp(t *testing.T) {
	t.Parallel()
	built := Entry{Op: "insert", Table: "binding", PK: `["01ABC"]`, Body: json.RawMessage(`{}`)}
	if err := checkShape(built); !errors.Is(err, ErrInvalid) {
		t.Fatalf("op insert: err = %v, want ErrInvalid", err)
	}
}

// The key text an outbox records is the json_array of the table's key columns in
// key order. Every shared table's rows are read and written that way, so each one
// has to parse to exactly the columns its own entry names -- otherwise a key
// column added by a later migration would silently address a different row.
func TestSyncKeyNamesEverySharedTableKey(t *testing.T) {
	t.Parallel()
	for _, shared := range db.SharedTables {
		t.Run(shared.Name, func(t *testing.T) {
			t.Parallel()
			pk := sampleKey(shared)
			cols, values, err := KeyOf(shared.Name, pk)
			if err != nil {
				t.Fatalf("KeyOf(%s, %s): %v", shared.Name, pk, err)
			}
			if len(cols) != len(shared.PrimaryKey) {
				t.Fatalf("KeyOf(%s) columns = %v, want %v", shared.Name, cols, shared.PrimaryKey)
			}
			for i := range shared.PrimaryKey {
				if cols[i] != shared.PrimaryKey[i] {
					t.Errorf("KeyOf(%s) column %d = %q, want %q", shared.Name, i, cols[i], shared.PrimaryKey[i])
				}
			}
			if len(values) != len(shared.PrimaryKey) {
				t.Fatalf("KeyOf(%s) values = %v, want %d values", shared.Name, values, len(shared.PrimaryKey))
			}
		})
	}
}

// A key naming a table no shared table lists would let a body choose the table its
// SQL reaches, so it is refused before any statement is built.
func TestSyncKeyRefusesATableThatIsNotShared(t *testing.T) {
	t.Parallel()
	for _, tbl := range []string{"sync_outbox", "sqlite_master", ""} {
		if _, _, err := KeyOf(tbl, `["1"]`); !errors.Is(err, ErrInvalid) {
			t.Errorf("KeyOf(%q): err = %v, want ErrInvalid", tbl, err)
		}
	}
}

// A key of the wrong arity would either match nothing or match a row the entry
// never named, so it is refused rather than guessed at.
func TestSyncKeyRefusesTheWrongNumberOfValues(t *testing.T) {
	t.Parallel()
	if _, _, err := KeyOf("binding_event", `["01ABC"]`); !errors.Is(err, ErrInvalid) {
		t.Fatalf("one value for a two-column key: err = %v, want ErrInvalid", err)
	}
}

// Key text is data from another machine, so it is parsed rather than read: a
// value that is not JSON at all is refused instead of being taken for a key.
func TestSyncKeyRefusesTextThatIsNotAJSONArray(t *testing.T) {
	t.Parallel()
	for _, pk := range []string{`["01ABC"]; DROP TABLE binding`, `not json`, `{"id":"01ABC"}`} {
		if _, _, err := KeyOf("binding", pk); !errors.Is(err, ErrInvalid) {
			t.Errorf("KeyOf with %q: err = %v, want ErrInvalid", pk, err)
		}
	}
}

// A key value wider than a float still has to name the row it was written for, so
// the parse keeps an integral number integral instead of rounding it through a
// real -- which is the whole reason the decoder is told to use json.Number.
func TestSyncKeyKeepsAWideIntegerExact(t *testing.T) {
	t.Parallel()
	const wide = int64(9007199254740993) // 2^53 + 1, the first integer a float64 cannot hold

	_, values, err := KeyOf("chain_event", fmt.Sprintf("[\"01ABC\",%d]", wide))
	if err != nil {
		t.Fatalf("KeyOf with a wide integer: %v", err)
	}
	got, ok := values[1].(int64)
	if !ok {
		t.Fatalf("key value type = %T, want int64", values[1])
	}
	if got != wide {
		t.Errorf("key value = %d, want %d", got, wide)
	}
}

// sampleKey builds a key text of a shared table's arity, holding the type each
// key column actually is so a parse that guessed wrong would land elsewhere.
func sampleKey(shared db.SharedTable) string {
	values := make([]any, 0, len(shared.PrimaryKey))
	for i, col := range shared.PrimaryKey {
		if isSequenceKey(col) {
			values = append(values, i+1)
			continue
		}
		values = append(values, "01ABC"+strconv.Itoa(i))
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// isSequenceKey reports whether a key column is a sequence number rather than a
// text id.
func isSequenceKey(col string) bool { return col == "seq" || col == "run" }
