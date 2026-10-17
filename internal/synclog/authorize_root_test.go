package synclog

import (
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

func TestHoldErrorReportsItsReason(t *testing.T) {
	t.Parallel()
	if got := (&HoldError{Reason: "why"}).Error(); got != "why" {
		t.Fatalf("Error() = %q, want why", got)
	}
}

func TestColumnFromRowReadsStringsOnly(t *testing.T) {
	t.Parallel()
	row := db.ExchangeRow{Columns: []db.ExchangeColumn{
		{Name: "a", Value: "x"},
		{Name: "n", Value: int64(3)},
	}}
	for col, want := range map[string]string{"a": "x", "n": "", "missing": ""} {
		if got := columnFromRow(row, col); got != want {
			t.Errorf("columnFromRow(%q) = %q, want %q", col, got, want)
		}
	}
}

func TestPkFirstOfAnUnparsableKeyIsEmpty(t *testing.T) {
	t.Parallel()
	if got := pkFirst("binding_record", `["b1"]`); got != "b1" {
		t.Fatalf("pkFirst = %q, want b1", got)
	}
	if got := pkFirst("no_such_table", `["b1"]`); got != "" {
		t.Fatalf("pkFirst of an unknown table = %q, want empty", got)
	}
}

func TestFindRootFallsBackToTheKeyAndResolvesNothingWhenRowsAreAbsent(t *testing.T) {
	t.Parallel()
	d, _ := peerFile(t, "m1")
	cases := []struct {
		name      string
		e         Entry
		values    map[string]any
		wantTable string
		wantID    string
	}{
		{"record by key", Entry{Table: "binding_record", PK: `["r1"]`}, nil, "binding_record", "r1"},
		{"binding by key", Entry{Table: "binding", PK: `["b1"]`}, nil, "binding", "b1"},
		{"child by key", Entry{Table: "round_file", PK: `["r1","f"]`}, nil, "binding_record", "r1"},
		{"child by body", Entry{Table: "binding_event", PK: `["r1",1]`}, map[string]any{"record_id": "r9"}, "binding_record", "r9"},
		{"round with body binding", Entry{Table: "round", PK: `["x"]`}, map[string]any{"binding_id": "b2"}, "binding", "b2"},
		{"round without a row", Entry{Table: "round", PK: `["x"]`}, nil, "binding", ""},
		{"event without a row", Entry{Table: "event", PK: `["x"]`}, nil, "binding", ""},
		{"artifact without a round", Entry{Table: "artifact", PK: `["x"]`}, nil, "binding", ""},
		{"artifact with an absent round", Entry{Table: "artifact", PK: `["x"]`}, map[string]any{"round_id": "r"}, "binding", ""},
		{"transcript of a non-round", Entry{Table: "transcript", PK: `["x"]`}, map[string]any{"owner_kind": "binding", "owner_id": "o"}, "", ""},
		{"transcript without a row", Entry{Table: "transcript", PK: `["x"]`}, nil, "", ""},
		{"transcript of an absent round", Entry{Table: "transcript", PK: `["x"]`}, map[string]any{"owner_kind": "round", "owner_id": "o"}, "", ""},
		{"unknown table", Entry{Table: "other", PK: `["x"]`}, nil, "", ""},
	}
	err := d.Tx(func(tx *db.Tx) error {
		for _, c := range cases {
			table, id := findRootIDAndTable(tx, c.e, c.values)
			if table != c.wantTable || id != c.wantID {
				t.Errorf("%s: root = (%q, %q), want (%q, %q)", c.name, table, id, c.wantTable, c.wantID)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
}

func TestAuthorizeAbsentRootOutsideTheBindingTreesOwnsByOrigin(t *testing.T) {
	t.Parallel()
	d, _ := peerFile(t, "m1")
	err := d.Tx(func(tx *db.Tx) error {
		dec, err := authorizeAbsentRoot(tx, Entry{Table: "round", Origin: "m2"}, "x", nil)
		if err != nil || dec.owner != "m2" {
			t.Errorf("decision = %+v, err = %v, want owner m2", dec, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
}

func TestNewDeleteBuildsABodylessEntryAndRejectsABadAfter(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	e, err := NewDelete("m1", "binding_record", `["b1"]`, 1, at)
	if err != nil || e.Op != OpDelete || len(e.Body) != 0 {
		t.Fatalf("NewDelete = %+v, %v", e, err)
	}
	if err := checkShape(Entry{Op: OpDelete, After: &Ref{}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("checkShape with an empty after ref = %v, want ErrInvalid", err)
	}
	if err := checkShape(Entry{Op: OpDelete, Body: []byte("{}")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("checkShape of a delete with a body = %v, want ErrInvalid", err)
	}
}

func TestNewHybridClockAtReadsTheGivenWallClock(t *testing.T) {
	t.Parallel()
	wall := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	clock := NewHybridClockAt(newMemKV(), func() time.Time { return wall })
	first, err := clock.Now()
	if err != nil {
		t.Fatalf("Now: %v", err)
	}
	second, err := clock.Now()
	if err != nil || second <= first {
		t.Fatalf("second stamp %v after %v, err %v", second, first, err)
	}
}
