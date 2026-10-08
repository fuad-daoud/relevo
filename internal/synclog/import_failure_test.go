package synclog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The failures an import has to survive: a log that will not answer, and entries
// it hands over that this machine cannot act on. In both cases the file is left
// as it was, because a mark that moved over entries that were not applied is a
// change no later run would make.

// refusingLog is a transport whose pull fails, which is how a remote reports that
// it is not ready. The import surfaces it rather than reporting nothing applied
// as though there were nothing to apply.
type refusingLog struct {
	*MemTransport
	err error
}

func (r refusingLog) Pull(map[string]int) ([]Entry, error) { return nil, r.err }

// A log that cannot be read is reported, and nothing is counted as applied.
func TestImportReportsAFailedPull(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	_, err := NewImporter(peer, refusingLog{MemTransport: NewMemTransport("m1"), err: fmt.Errorf("the log is away")}).Import()
	if err == nil {
		t.Fatal("import through a failing log succeeded, want the failure reported")
	}
	if seq, found, markErr := peer.ImportMark("m1"); markErr != nil || found {
		t.Fatalf("ImportMark = (%d, %t, %v), want no mark after a failed pull", seq, found, markErr)
	}
}

// An entry this machine cannot act on fails its batch, and the mark does not
// move: the batch is still unapplied, so it is read again rather than passed
// over. Each case is an entry a remote could carry and a local build must refuse.
func TestImportRefusesAnUnapplicableEntry(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		entry Entry
	}{
		{"a body that is not a row", Entry{
			Table: "binding", PK: `["b1"]`, Op: OpUpsert, Body: json.RawMessage(`["not","a","row"]`),
		}},
		{"a body that is not json", Entry{
			Table: "binding", PK: `["b1"]`, Op: OpUpsert, Body: json.RawMessage(`{`),
		}},
		{"an op the log does not carry", Entry{
			Table: "binding", PK: `["b1"]`, Op: Op("truncate"),
		}},
		{"a key that is not json", Entry{
			Table: "binding", PK: `b1`, Op: OpDelete,
		}},
		{"a key of the wrong length", Entry{
			Table: "binding", PK: `["b1","extra"]`, Op: OpDelete,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			peer, _ := peerFile(t, "m2")
			log := NewMemTransport("m1")
			entry := c.entry
			entry.Origin = "m1"
			entry.SchemaVersion = 23
			if _, err := log.Append([]Entry{entry}); err != nil {
				// A shape the transport itself refuses never reaches an importer,
				// which is the better place for it to be caught.
				return
			}
			if _, err := NewImporter(peer, log.OnLog("m2")).Import(); err == nil {
				t.Fatal("the import succeeded, want the entry refused")
			}
			if seq, found, err := peer.ImportMark("m1"); err != nil || found {
				t.Fatalf("ImportMark = (%d, %t, %v), want no mark for a batch that was refused", seq, found, err)
			}
		})
	}
}

// An empty batch is applied without a transaction, because there is nothing to
// write and nothing to mark. The transport never hands one over -- an append of
// no entries writes nothing -- so this is the shape a caller holding an empty
// batch reaches, and the mark it would otherwise write is one past a sequence
// that does not exist.
func TestApplyBatchSkipsAnEmptyBatch(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	i := NewImporter(peer, NewMemTransport("m1"))
	applied, err := i.applyBatch(nil)
	if err != nil || applied != 0 {
		t.Fatalf("applyBatch(nil) = (%d, %v), want nothing applied and no error", applied, err)
	}
	if marks, err := peer.ImportMarks(); err != nil || len(marks) != 0 {
		t.Fatalf("marks = %v (%v), want none written for an empty batch", marks, err)
	}
}

// An entry that reaches the apply path with a shape the log would never carry is
// still refused, because the check that refuses it is the importer's own and does
// not depend on what got past the transport. Each case names what it got past.
func TestApplyEntryRefusesAMalformedEntry(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		entry Entry
		want  string
	}{
		{"a body that is not a row", Entry{
			Table: "binding", PK: `["b1"]`, Op: OpUpsert, Body: json.RawMessage(`["not","a","row"]`),
		}, "decode body"},
		{"a body that is not json", Entry{
			Table: "binding", PK: `["b1"]`, Op: OpUpsert, Body: json.RawMessage(`{`),
		}, "decode body"},
		{"an op the log does not carry", Entry{
			Table: "binding", PK: `["b1"]`, Op: Op("truncate"),
		}, "op"},
		{"a key that is not json", Entry{
			Table: "binding", PK: `b1`, Op: OpDelete,
		}, "not a JSON array"},
		{"a table that is not shared", Entry{
			Table: "sqlite_master", PK: `["x"]`, Op: OpDelete,
		}, "shared table"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			peer, _ := peerFile(t, "m2")
			entry := c.entry
			entry.Origin = "m1"
			err := peer.Tx(func(tx *db.Tx) error { return applyEntry(tx, entry) })
			if err == nil {
				t.Fatalf("applying %s succeeded, want it refused", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("the refusal is %q, want it to mention %q", err, c.want)
			}
		})
	}
}
