package synclog

import (
	"encoding/json"
	"strings"
	"testing"
)

// How far a mark moves, and what it refuses to move. The mark is the one thing
// standing between this machine and the entries behind it, and the sequence it
// is given comes from a transport rather than from this file, so the importer
// does not take it on trust: a mark is written from the entries that were
// actually applied, and only forwards.

// scriptedLog answers a pull with entries a test wrote rather than with the
// log's own. It models a transport that hands over a sequence or an origin
// nobody assigned, which is the input these cases are about. Everything else is
// forwarded, so an importer that decides to ask for a head or an append still
// reaches the log underneath.
type scriptedLog struct {
	*MemTransport
	pulls [][]Entry
}

func (s scriptedLog) Pull(map[string]int) ([]Entry, error) {
	var out []Entry
	for _, batch := range s.pulls {
		out = append(out, batch...)
	}
	return out, nil
}

// A batch claiming a sequence far past anything the log holds applies nothing:
// it is a hole in that origin's log, so the origin holds rather than skipping to
// it. The mark is left exactly where it was and the genuine entries behind the
// fabricated sequence still import: a mark written over entries that were never
// applied would hide them for good, because a mark that has jumped forward is
// never read past again.
func TestImportLeavesTheMarkAloneAfterAFabricatedJump(t *testing.T) {
	t.Parallel()
	peer, _ := victim(t)
	know := knownVersion(t, peer)
	// The jump names the reader's own binding under an origin that does not own
	// it, so the ownership gate refuses it and nothing takes effect.
	log := scriptedLog{MemTransport: NewMemTransport("m2"), pulls: [][]Entry{
		{
			{Origin: "m1", Batch: 1000000, Seq: 1000000, Table: "binding", PK: `["01A"]`,
				Op: OpDelete, SchemaVersion: know},
		},
		{
			{Origin: "m3", Batch: 1, Seq: 1, Table: "binding", PK: `["01B"]`,
				Op: OpUpsert, SchemaVersion: know,
				Body: bodyFor(t, map[string]any{
					"id": "01B", "name": "01B", "cwd": "/real",
					"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m3",
				})},
		},
	}}

	got, err := NewImporter(peer, log).Import()
	if err != nil {
		t.Fatalf("import with a fabricated sequence: %v", err)
	}
	if got.Applied != 1 {
		t.Fatalf("import = %+v, want only the genuine entry applied", got)
	}
	if _, found, err := peer.ImportMark("m1"); err != nil || found {
		t.Fatalf("the mark for m1 = (found=%t, %v), want none: nothing was applied for it", found, err)
	}
	if seq, found, err := peer.ImportMark("m3"); err != nil || !found || seq != 1 {
		t.Fatalf("the mark for m3 = (%d, %t, %v), want the applied entry's sequence", seq, found, err)
	}
	if have := sharedRows(t, peer); have != `binding ["01A"]|binding ["01B"]` {
		t.Fatalf("the peer holds %q, want both bindings", have)
	}

	// The entries behind the fabricated sequence are still readable, which is the
	// point: the mark never moved past them.
	more := scriptedLog{MemTransport: NewMemTransport("m2"), pulls: [][]Entry{{
		{Origin: "m3", Batch: 2, Seq: 2, Table: "binding", PK: `["01B"]`,
			Op: OpUpsert, SchemaVersion: know,
			Body: bodyFor(t, map[string]any{
				"id": "01B", "name": "01B", "cwd": "/later",
				"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m3",
			})},
	}}}
	again, err := NewImporter(peer, more).Import()
	if err != nil {
		t.Fatalf("import the entries behind the fabricated jump: %v", err)
	}
	if again.Applied != 1 {
		t.Fatalf("the second import = %+v, want the genuine entry applied", again)
	}
	if cwd := columnOf(t, peer, `SELECT cwd FROM binding WHERE id = '01B'`); cwd != "/later" {
		t.Fatalf("the binding's cwd = %q, want the later genuine value", cwd)
	}
}

// A batch handed over again at a sequence this machine has already applied
// changes nothing and moves no mark. Replaying it would rewrite rows with content
// the log has since moved past, because an import takes the last write it is
// offered rather than the last one that was written.
func TestImportIgnoresAReplayedBatch(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, theirPath, insertBinding("01A", "m1"), insertBinding("01B", "m1"))

	log := NewMemTransport("m1")
	writer := &recording{MemTransport: log}
	importFrom(t, peer, log, theirs, writer)
	before := sharedRows(t, peer)
	marks, err := peer.ImportMarks()
	if err != nil {
		t.Fatalf("read the marks after the first import: %v", err)
	}
	if marks["m1"] != 2 {
		t.Fatalf("marks = %v, want m1 applied through sequence 2", marks)
	}

	// The same entries, at the sequence numbers they had, offered again.
	replayed := scriptedLog{MemTransport: log, pulls: [][]Entry{writer.appended()}}
	got, err := NewImporter(peer, replayed).Import()
	if err != nil {
		t.Fatalf("import the replayed batch: %v", err)
	}
	if got.Applied != 0 || got.Batches != 0 || len(got.Dropped) != 0 {
		t.Fatalf("import = %+v, want the replayed batch ignored", got)
	}
	if have := sharedRows(t, peer); have != before {
		t.Fatalf("the peer holds %q, want the first import's %q", have, before)
	}
	after, err := peer.ImportMarks()
	if err != nil {
		t.Fatalf("read the marks after the replay: %v", err)
	}
	if after["m1"] != marks["m1"] {
		t.Fatalf("the mark moved to %d, want it resting at %d", after["m1"], marks["m1"])
	}
}

// A batch a newer binary wrote writes no mark, so an upgraded machine resumes
// from the entry before it. A batch this machine refuses outright is a different
// thing: it moves the mark past itself, or the refusal would sit at the origin's
// mark and hold every batch behind it forever.
func TestImportWritesNoMarkForAHeldBatch(t *testing.T) {
	t.Parallel()
	peer, _ := victim(t)
	know := knownVersion(t, peer)
	log := scriptedLog{MemTransport: NewMemTransport("m2"), pulls: [][]Entry{
		[]Entry{
			// Held: written by a binary this build does not know.
			{Origin: "m1", Batch: 1, Seq: 1, Table: "binding", PK: `["01A"]`,
				Op: OpUpsert, SchemaVersion: know + 1,
				Body: bodyFor(t, map[string]any{"id": "01A", "name": "01A", "cwd": "/ahead"})},
		},
		// Refused: a claim on the reader's own row.
		[]Entry{
			{Origin: "m3", Batch: 1, Seq: 1, Table: "binding", PK: `["01A"]`,
				Op: OpDelete, SchemaVersion: know},
		},
	}}

	got, err := NewImporter(peer, log).Import()
	if err != nil {
		t.Fatalf("import with a held and a refused batch: %v", err)
	}
	if len(got.Held) != 1 || got.Held[0].Origin != "m1" {
		t.Fatalf("Held = %+v, want m1 held", got.Held)
	}
	if len(got.Dropped) != 1 || got.Dropped[0].Origin != "m3" {
		t.Fatalf("Dropped = %+v, want m3's batch dropped", got.Dropped)
	}
	marks, err := peer.ImportMarks()
	if err != nil {
		t.Fatalf("read the marks: %v", err)
	}
	if len(marks) != 1 || marks["m3"] != 1 {
		t.Fatalf("marks = %v, want only m3 moved past the batch it refused", marks)
	}
	if cwd := columnOf(t, peer, `SELECT cwd FROM binding WHERE id = '01A'`); cwd != "/x" {
		t.Fatalf("the binding's cwd = %q, want the file left as it was", cwd)
	}
}

// Ordinary progress moves the mark to the last sequence each batch carried, one
// origin at a time. This is the whole of what the mark is for, so it is pinned
// here as well as in the import cases that happen to exercise it.
func TestImportMovesTheMarkWithEachBatch(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, theirPath,
		insertBinding("01A", "m1"),
		insertRound("02A", "01A"),
		insertRecord("03A", "m1"),
	)
	other, otherPath := exporterFile(t, "m3")
	seed(t, otherPath, insertBinding("04A", "m3"))

	log := NewMemTransport("m1")
	if _, err := exporter(theirs, &recording{MemTransport: log}).Export(); err != nil {
		t.Fatalf("export m1's rows: %v", err)
	}
	if _, err := exporter(other, &recording{MemTransport: log.OnLog("m3")}).Export(); err != nil {
		t.Fatalf("export m3's rows: %v", err)
	}

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import both origins: %v", err)
	}
	if got.Applied != 4 || got.Batches != 2 {
		t.Fatalf("import = %+v, want both origins' batches applied", got)
	}
	marks, err := peer.ImportMarks()
	if err != nil {
		t.Fatalf("read the marks: %v", err)
	}
	if marks["m1"] != 3 || marks["m3"] != 1 {
		t.Fatalf("marks = %v, want m1 at the last sequence of its batch and m3 at its own", marks)
	}

	// A second run has nothing left to read, so neither mark moves.
	again, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import again: %v", err)
	}
	if again.Applied != 0 || again.Batches != 0 {
		t.Fatalf("the second import = %+v, want nothing left to apply", again)
	}
	rest, err := peer.ImportMarks()
	if err != nil {
		t.Fatalf("read the marks after the second run: %v", err)
	}
	if rest["m1"] != 3 || rest["m3"] != 1 {
		t.Fatalf("marks = %v, want both left where the first run put them", rest)
	}
}

// The mark is the highest sequence a batch carries, not the last entry read: a
// transport handing entries back out of order must not be able to lower what this
// machine records, because a lower mark offers the entries behind it again.
func TestTailIsTheHighestSequenceInABatch(t *testing.T) {
	t.Parallel()
	batch := []Entry{{Seq: 4}, {Seq: 9}, {Seq: 6}}
	if got := tail(batch); got != 9 {
		t.Fatalf("tail = %d, want the highest sequence in the batch", got)
	}
	if got := tail(nil); got != 0 {
		t.Fatalf("tail of no batch = %d, want nothing", got)
	}
}

// A batch that begins past the origin's mark is a hole, not progress. Sequence
// numbers per origin are contiguous by construction, so a batch far ahead means
// the entries in between have not arrived; the origin holds where it is and the
// hole is reported, and the forged batch writes nothing. The genuine entries the
// origin did send still apply, because the mark never moved over them.
func TestImportHoldsOnAGapInsteadOfSkipping(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	know := knownVersion(t, peer)
	log := scriptedLog{MemTransport: NewMemTransport("m2"), pulls: [][]Entry{
		{{Origin: "m1", Batch: 1, Seq: 1, Table: "binding", PK: `["01A"]`,
			Op: OpUpsert, SchemaVersion: know,
			Body: bodyFor(t, map[string]any{
				"id": "01A", "name": "01A", "cwd": "/real",
				"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m1",
			})}},
		{{Origin: "m1", Batch: 1000000, Seq: 1000000, Table: "binding", PK: `["01B"]`,
			Op: OpUpsert, SchemaVersion: know,
			Body: bodyFor(t, map[string]any{
				"id": "01B", "name": "01B", "cwd": "/forged",
				"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m1",
			})}},
		{{Origin: "m3", Batch: 1, Seq: 1, Table: "binding", PK: `["01C"]`,
			Op: OpUpsert, SchemaVersion: know,
			Body: bodyFor(t, map[string]any{
				"id": "01C", "name": "01C", "cwd": "/other",
				"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m3",
			})}},
	}}

	got, err := NewImporter(peer, log).Import()
	if err != nil {
		t.Fatalf("import with a gap in one origin: %v", err)
	}
	if got.Applied != 2 {
		t.Fatalf("import = %+v, want the genuine batches applied and the forged one held", got)
	}
	if len(got.Gaps) != 1 || got.Gaps[0].Origin != "m1" || got.Gaps[0].Got != 1000000 {
		t.Fatalf("Gaps = %+v, want m1 held at the forged sequence", got.Gaps)
	}
	if !strings.Contains(got.Gaps[0].String(), "m1") {
		t.Fatalf("the gap reads %q, want it to name the held origin", got.Gaps[0])
	}
	if seq, found, err := peer.ImportMark("m1"); err != nil || !found || seq != 1 {
		t.Fatalf("ImportMark for m1 = (%d, %t, %v), want the genuine entry's sequence", seq, found, err)
	}
	if have := sharedRows(t, peer); have != `binding ["01A"]|binding ["01C"]` {
		t.Fatalf("the peer holds %q, want the genuine rows and not the forged one", have)
	}
}

// A batch is applied only when every entry is contiguous from the mark. A batch
// whose head lands on the expected sequence but whose second entry jumps far past
// it is the same hole as one that begins late: applying it would advance the mark
// over the sequences it skipped and hide them for good. The origin holds, and the
// genuine batch that later runs from the mark still applies.
func TestImportHoldsWhenABatchJumpsInsideItself(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	know := knownVersion(t, peer)
	jump := scriptedLog{MemTransport: NewMemTransport("m2"), pulls: [][]Entry{
		{
			{Origin: "m1", Batch: 1, Seq: 1, Table: "binding", PK: `["01A"]`,
				Op: OpUpsert, SchemaVersion: know,
				Body: bodyFor(t, map[string]any{
					"id": "01A", "name": "01A", "cwd": "/a",
					"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m1",
				})},
			{Origin: "m1", Batch: 1, Seq: 1000000, Table: "binding", PK: `["01B"]`,
				Op: OpUpsert, SchemaVersion: know,
				Body: bodyFor(t, map[string]any{
					"id": "01B", "name": "01B", "cwd": "/forged",
					"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m1",
				})},
		},
	}}

	got, err := NewImporter(peer, jump).Import()
	if err != nil {
		t.Fatalf("import a batch that jumps inside itself: %v", err)
	}
	if got.Applied != 0 {
		t.Fatalf("import = %+v, want nothing applied for a hole inside the batch", got)
	}
	if len(got.Gaps) != 1 || got.Gaps[0].Origin != "m1" || got.Gaps[0].Got != 1000000 {
		t.Fatalf("Gaps = %+v, want m1 held at the sequence it jumped to", got.Gaps)
	}
	if _, found, err := peer.ImportMark("m1"); err != nil || found {
		t.Fatalf("the mark for m1 (found=%t, %v), want none: nothing was applied", found, err)
	}

	// The log is honest from here: the genuine entries run from the mark, so the
	// whole batch is contiguous and applies.
	honest := scriptedLog{MemTransport: NewMemTransport("m2"), pulls: [][]Entry{
		{
			{Origin: "m1", Batch: 1, Seq: 1, Table: "binding", PK: `["01A"]`,
				Op: OpUpsert, SchemaVersion: know,
				Body: bodyFor(t, map[string]any{
					"id": "01A", "name": "01A", "cwd": "/a",
					"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m1",
				})},
			{Origin: "m1", Batch: 2, Seq: 2, Table: "binding", PK: `["01C"]`,
				Op: OpUpsert, SchemaVersion: know,
				Body: bodyFor(t, map[string]any{
					"id": "01C", "name": "01C", "cwd": "/c",
					"builder_mode": "local", "created_at": "t", "ingest_source": "manual", "origin": "m1",
				})},
		},
	}}
	again, err := NewImporter(peer, honest).Import()
	if err != nil {
		t.Fatalf("import the genuine batch: %v", err)
	}
	if again.Applied != 2 || len(again.Gaps) != 0 {
		t.Fatalf("import = %+v, want the genuine entries applied with no gap", again)
	}
	if seq, found, err := peer.ImportMark("m1"); err != nil || !found || seq != 2 {
		t.Fatalf("ImportMark = (%d, %t, %v), want the mark at the genuine tail", seq, found, err)
	}
	if have := sharedRows(t, peer); have != `binding ["01A"]|binding ["01C"]` {
		t.Fatalf("the peer holds %q, want the genuine rows and not the forged one", have)
	}
}

// A dropped batch moves the mark only forwards. A batch whose tail is behind the
// mark writes nothing: the sequence behind it is not progress, and rewinding
// would have the entries above it offered again as though they were new.
func TestImportNeverRewindsAMarkWhenDropping(t *testing.T) {
	t.Parallel()
	peer, _ := peerFile(t, "m2")
	if err := peer.SetImportMark("m1", 5); err != nil {
		t.Fatalf("set the mark: %v", err)
	}
	i := NewImporter(peer, NewMemTransport("m1"))
	marks := map[string]int{"m1": 5}
	// A body this machine cannot read, in a batch whose tail is behind the mark.
	batch := []Entry{{
		Origin: "m1", Batch: 3, Seq: 3, Table: "binding", PK: `["b1"]`,
		Op: OpUpsert, SchemaVersion: i.known, Body: json.RawMessage(`{`),
	}}

	applied, hold, drop, err := i.applyBatch(batch, marks)
	if err != nil {
		t.Fatalf("apply a batch behind the mark: %v", err)
	}
	if drop == nil || applied != 0 || hold != nil {
		t.Fatalf("applyBatch = (%d, %+v, %+v, %v), want the unreadable batch dropped", applied, hold, drop, err)
	}
	if marks["m1"] != 5 {
		t.Fatalf("the in-memory mark = %d, want it left at 5", marks["m1"])
	}
	if seq, found, err := peer.ImportMark("m1"); err != nil || !found || seq != 5 {
		t.Fatalf("ImportMark = (%d, %t, %v), want the mark left at 5", seq, found, err)
	}
}

// An entry at or below the mark its origin has reached is not offered to the
// importer again, whatever its batch number says. Both decide it, because the
// transport hands back whole batches and a batch that began before the mark is a
// batch whose order this machine has already followed.
func TestReplayedReadsBothTheSequenceAndTheBatch(t *testing.T) {
	t.Parallel()
	marks := map[string]int{"m1": 5}
	for _, c := range []struct {
		name  string
		entry Entry
		want  bool
	}{
		{"past the mark", Entry{Origin: "m1", Seq: 6, Batch: 6}, false},
		{"at the mark", Entry{Origin: "m1", Seq: 5, Batch: 5}, true},
		{"behind the mark in a batch past it", Entry{Origin: "m1", Seq: 4, Batch: 6}, true},
		{"past the mark in a batch behind it", Entry{Origin: "m1", Seq: 6, Batch: 5}, true},
		{"an origin with no mark", Entry{Origin: "m3", Seq: 1, Batch: 1}, false},
	} {
		if got := replayed(marks, c.entry); got != c.want {
			t.Fatalf("replayed(%s) = %t, want %t", c.name, got, c.want)
		}
	}
}
