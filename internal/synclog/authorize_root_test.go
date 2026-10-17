package synclog

import (
	"errors"
	"fmt"
	"strings"
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

func stripBodies(tbl, writer string, withKey bool) map[string]any {
	var body map[string]any
	if tbl == "binding" {
		body = map[string]any{
			"id": "b1", "name": "b1", "cwd": "/x", "builder_mode": "local",
			"created_at": "t", "ingest_source": "manual", "origin": "m1",
		}
	} else {
		body = map[string]any{
			"id": "b1", "owner": "o1", "name": "b1", "state": "open", "round": 1,
			"cwd": "/x", "record_json": "{}", "created_at": "t", "updated_at": "t",
			"origin": "m1",
		}
	}
	if withKey {
		body["writer"] = writer
	}
	return body
}

// importStripAttempt has m1 create the root, optionally hand it to m2, then
// upsert the root again with the given writer body, and returns the peer, the
// import result and error of that last entry alone.
func importStripAttempt(t *testing.T, tbl string, handOver, withKey bool, writer string) (*db.DB, ImportResult, error) {
	t.Helper()
	peer, _ := peerFile(t, "m3")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")
	pk := `["b1"]`
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", tbl, pk, OpUpsert, know, stripBodies(tbl, "m1", true), nil))
	if handOver {
		appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", tbl, pk, OpUpsert, know, stripBodies(tbl, "m2", true), nil))
	}
	first := NewImporter(peer, log.OnLog("m3"))
	first.SetKey(handoverKey)
	if _, err := first.Import(); err != nil {
		t.Fatalf("import before the strip: %v", err)
	}
	appendSignedBatch(t, log, handoverKey, makeSignedEntry(t, handoverKey, "m1", tbl, pk, OpUpsert, know, stripBodies(tbl, writer, withKey), nil))
	imp := NewImporter(peer, log.OnLog("m3"))
	imp.SetKey(handoverKey)
	res, err := imp.Import()
	return peer, res, err
}

func TestAnEmptyWriterNeverStripsAPresentRoot(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		tbl      string
		handOver bool
		want     string
	}{
		"binding_record, current writer": {"binding_record", false, "m1"},
		"binding, current writer":        {"binding", false, "m1"},
		"binding_record, term path":      {"binding_record", true, "m2"},
		"binding, term path":             {"binding", true, "m2"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			peer, res, err := importStripAttempt(t, tc.tbl, tc.handOver, true, "")
			if err == nil && len(res.Dropped) == 0 && len(res.Held) == 0 {
				t.Fatalf("the strip applied cleanly: %+v", res)
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Fatalf("import error = %v, want ErrInvalid", err)
			}
			if reason := fmt.Sprint(err, res.Dropped); !strings.Contains(reason, "every machine") {
				t.Fatalf("refusal = %s, want the authorize refusal rather than the storage one", reason)
			}
			if got := columnOf(t, peer, `SELECT writer FROM `+tc.tbl+` WHERE id = 'b1'`); got != tc.want {
				t.Fatalf("writer = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAWriterlessBodyStillAppliesToAPresentRoot(t *testing.T) {
	t.Parallel()
	peer, res, err := importStripAttempt(t, "binding_record", false, false, "")
	if err != nil || res.Applied != 1 {
		t.Fatalf("import = %+v, %v; want one applied entry", res, err)
	}
	if got := columnOf(t, peer, `SELECT writer FROM binding_record WHERE id = 'b1'`); got != "m1" {
		t.Fatalf("writer = %q, want m1", got)
	}
}
