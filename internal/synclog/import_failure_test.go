package synclog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The failures an import has to survive: a log that will not answer, and entries
// it hands over that this machine cannot act on. A log that will not answer
// ends the run and leaves the file as it was, because a mark that moved over
// entries that were not applied is a change no later run would make. An entry
// this machine cannot act on is the opposite: the run carries on, the origins
// beside it keep moving, and the mark moves past the batch so it is not offered
// again.

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

// An entry this machine cannot act on costs its own batch and nothing else. The
// batch is dropped, the mark moves past it, and every other origin's batches
// still apply: a body nobody can read is a body nobody will ever read, so
// re-reading it would refuse it again for as long as the log holds it. Each case
// is an entry a remote could carry and a local build must refuse.
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
		{"a body that names no key to place the row", Entry{
			Table: "binding", PK: `["b1"]`, Op: OpUpsert,
			Body: json.RawMessage(`{"name":"b1","cwd":"/x"}`),
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
			got, err := NewImporter(peer, log.OnLog("m2")).Import()
			if err != nil {
				t.Fatalf("import with %s: %v, want the batch dropped rather than the run failed", c.name, err)
			}
			if got.Applied != 0 || len(got.Dropped) != 1 {
				t.Fatalf("import = %+v, want nothing applied and the batch dropped", got)
			}
			if have := sharedRows(t, peer); have != "" {
				t.Fatalf("the peer holds %q, want nothing written by a refused entry", have)
			}
			// The mark is past the dropped batch, so the next run is not offered
			// it again: a refusal that re-read itself would halt every origin on
			// this machine for as long as the log holds the entry.
			if seq, found, err := peer.ImportMark("m1"); err != nil || !found || seq != 1 {
				t.Fatalf("ImportMark = (%d, %t, %v), want the mark past the dropped batch", seq, found, err)
			}
			again, err := NewImporter(peer, log.OnLog("m2")).Import()
			if err != nil {
				t.Fatalf("import again after the drop: %v", err)
			}
			if again.Applied != 0 || len(again.Dropped) != 0 {
				t.Fatalf("the second import = %+v, want the dropped batch not re-read", again)
			}
		})
	}
}

// One origin's poison entry does not stop the origins beside it. The two writers
// are m1 and m3 and the reader is m2, so a run that aborted on m1's entry would
// never reach m3's batches at all -- the permanent stall one un-appliable entry
// must not leave every peer in.
func TestImportDropsOneOriginAndKeepsTheOther(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")

	appendBatch(t, log, Entry{
		Table: "binding", PK: `["b1"]`, Op: OpUpsert, SchemaVersion: know,
		Body: bodyFor(t, map[string]any{"name": "no key in here"}),
	})
	appendBatch(t, log.OnLog("m3"), upsert(t, "03A", know))

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import with one origin's poison batch: %v", err)
	}
	if got.Applied != 1 || got.Batches != 1 {
		t.Fatalf("import = %+v, want the healthy origin's one batch applied", got)
	}
	if len(got.Dropped) != 1 || got.Dropped[0].Origin != "m1" {
		t.Fatalf("Dropped = %+v, want only m1's batch dropped", got.Dropped)
	}
	if have := sharedRows(t, peer); have != `binding ["03A"]` {
		t.Fatalf("the peer holds %q, want the healthy origin's row", have)
	}
	if seq, _, err := peer.ImportMark("m3"); err != nil || seq != 1 {
		t.Fatalf("ImportMark for m3 = %d (%v), want the healthy origin advanced", seq, err)
	}
	if seq, found, err := peer.ImportMark("m1"); err != nil || !found || seq != 1 {
		t.Fatalf("ImportMark for m1 = (%d, %t, %v), want it past the dropped batch", seq, found, err)
	}

	// A second run after the drop is clean: nothing is applied, nothing is
	// dropped, and nothing is offered again.
	again, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import again after the drop: %v", err)
	}
	if again.Applied != 0 || again.Batches != 0 || len(again.Dropped) != 0 {
		t.Fatalf("the second import = %+v, want the run with nothing left to do", again)
	}
}

// A body that breaks a foreign key is refused the same way an unreadable one is:
// the parent the body names is not in the file, and that is a fact about an
// entry from elsewhere rather than a failure of this machine's write.
func TestImportDropsABatchThatBreaksAForeignKey(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	know := knownVersion(t, peer)
	log := NewMemTransport("m1")

	// A round naming a binding this file has never been told about.
	appendBatch(t, log, Entry{
		Table: "round", PK: `["02A"]`, Op: OpUpsert, SchemaVersion: know,
		Body: bodyFor(t, map[string]any{"id": "02A", "binding_id": "nowhere", "round": 1}),
	})

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import a batch that breaks a foreign key: %v", err)
	}
	if got.Applied != 0 || len(got.Dropped) != 1 {
		t.Fatalf("import = %+v, want the batch dropped for the constraint", got)
	}
	if have := sharedRows(t, peer); have != "" {
		t.Fatalf("the peer holds %q, want nothing written by a refused batch", have)
	}
}

// A failure of this machine's own ends the run rather than being dropped. A busy
// file, a read that failed, a file that is not there to write into: none of those
// is a body to pass over, and a mark written past one would hide the entries
// behind it from every later run.
func TestApplyBatchFailsOnAHardFailure(t *testing.T) {
	t.Parallel()
	peer, path := peerFile(t, "m2")
	i := NewImporter(peer, NewMemTransport("m1"))
	batch := []Entry{{
		Origin: "m1", Batch: 1, Seq: 1, Table: "binding", PK: `["01A"]`,
		Op: OpUpsert, SchemaVersion: i.known,
		Body: bodyFor(t, map[string]any{
			"id": "01A", "name": "01A", "cwd": "/x",
			"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m1",
		}),
	}}
	// The file this machine writes into is closed under it, which is what a
	// daemon that has shut down looks like from here.
	if err := peer.Close(); err != nil {
		t.Fatalf("close the peer file: %v", err)
	}
	applied, hold, drop, err := i.applyBatch(batch, map[string]int{})
	if err == nil {
		t.Fatal("applying a batch to a file that is not open succeeded, want the failure reported")
	}
	if applied != 0 || hold != nil || drop != nil {
		t.Fatalf("applyBatch = (%d, %+v, %+v, %v), want the failure reported rather than dropped",
			applied, hold, drop, err)
	}
	if refused(err) {
		t.Fatalf("the failure %v reads as a refusal, want this machine's own trouble", err)
	}

	// Nothing was applied, so nothing may be recorded as applied.
	raw, err := db.OpenRawReadOnly(path)
	if err != nil {
		t.Fatalf("open the file for reading its mark: %v", err)
	}
	defer func() { _ = raw.Close() }()
	var marks int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sync_import_mark`).Scan(&marks); err != nil {
		t.Fatalf("count the marks: %v", err)
	}
	if marks != 0 {
		t.Fatalf("the file holds %d marks, want none after a failure that applied nothing", marks)
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
	applied, hold, drop, err := i.applyBatch(nil, map[string]int{})
	if err != nil || applied != 0 || hold != nil || drop != nil {
		t.Fatalf("applyBatch(nil) = (%d, %+v, %+v, %v), want nothing applied, no hold, no drop and no error",
			applied, hold, drop, err)
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
			var handled bool
			err := peer.Tx(func(tx *db.Tx) error {
				ok, err := applyEntry(tx, entry)
				handled = ok
				return err
			})
			if handled || err == nil {
				t.Fatalf("applying %s = (%t, %v), want it refused", c.name, handled, err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("the refusal is %q, want it to mention %q", err, c.want)
			}
		})
	}
}
