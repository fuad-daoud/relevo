package synclog

import (
	"testing"
	"time"
)

// The per-table breakdown tallies what each direction moved under each name,
// and merges breakdowns without losing a table. A nil breakdown tallies and
// merges as empty, so a run that moved nothing carries none.
func TestTableTalliesNoteAndAdd(t *testing.T) {
	t.Parallel()
	var tallies TableTallies
	tallies.noteExported([]Entry{
		{Table: "binding", Body: []byte(`{"a":1}`)},
		{Table: "binding", Body: []byte(`{"b":2}`)},
		{Table: "round"},
	})
	tallies.noteImported([]Entry{
		{Table: "binding", Body: []byte(`{"c":3}`)},
	})
	var more TableTallies
	more.noteExported([]Entry{{Table: "round", Body: []byte(`{"d":4}`)}})
	tallies.Add(more)
	tallies.Add(nil)

	binding := tallies["binding"]
	if binding.ExportedEntries != 2 || binding.ExportedBytes != 14 || binding.ImportedEntries != 1 || binding.ImportedBytes != 7 {
		t.Errorf("binding tally = %+v, want 2 exported/14 bytes and 1 imported/7 bytes", binding)
	}
	round := tallies["round"]
	if round.ExportedEntries != 2 || round.ExportedBytes != 7 || round.ImportedEntries != 0 {
		t.Errorf("round tally = %+v, want 2 exported/7 bytes and nothing imported", round)
	}
}

// Entries a run dropped are tallied by nobody: the mark moved past them
// rather than over them, so counting them would bill the breakdown for rows
// the file never took.
func TestTableTalliesSkipDroppedRows(t *testing.T) {
	t.Parallel()
	var tallies TableTallies
	rows := []Entry{{Table: "binding", Seq: 1}, {Table: "round", Seq: 2}}
	tallies.noteAppliedRows(rows, []Drop{{Origin: "m1", Seq: 2, Reason: "refused"}})
	if got := tallies["binding"].ImportedEntries; got != 1 {
		t.Errorf("binding imported = %d, want 1", got)
	}
	if _, ok := tallies["round"]; ok {
		t.Errorf("the dropped round was tallied: %+v", tallies["round"])
	}
}

// One export's breakdown names every table it appended, with the body bytes
// each carried. The byte totals are checked against the batches the transport
// recorded, so the breakdown cannot drift from what the wire carried.
func TestExportTalliesTablesPerBatch(t *testing.T) {
	t.Parallel()
	mine, myPath := exporterFile(t, "m1")
	seed(t, myPath,
		insertBinding("01A", "m1"),
		insertRound("02A", "01A"),
	)
	log := NewMemTransport("m1")
	writer := &recording{MemTransport: log}
	res, err := exporter(mine, writer).Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if res.Appended != 2 {
		t.Fatalf("appended = %d, want 2", res.Appended)
	}
	var wireBytes int64
	wireEntries := map[string]int{}
	for _, batch := range writer.batches {
		for _, e := range batch {
			wireBytes += int64(len(e.Body))
			wireEntries[e.Table]++
		}
	}
	var talliedBytes int64
	for table, tally := range res.Tables {
		if tally.ExportedEntries != wireEntries[table] {
			t.Errorf("%s exported = %d, wire carried %d", table, tally.ExportedEntries, wireEntries[table])
		}
		talliedBytes += tally.ExportedBytes
		if tally.ImportedEntries != 0 {
			t.Errorf("%s imported = %d on an export, want 0", table, tally.ImportedEntries)
		}
	}
	if talliedBytes != wireBytes {
		t.Errorf("tallied bytes = %d, wire carried %d", talliedBytes, wireBytes)
	}
	if len(res.Tables) != 2 {
		t.Errorf("tables = %v, want the binding and its round", res.Tables)
	}
}

// The peer's import tallies the same rows under the same names, so the two
// halves of one exchange agree on which table dominated it.
func TestImportTalliesAppliedRows(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, theirPath,
		insertBinding("01A", "m1"),
		insertRound("02A", "01A"),
	)
	log := NewMemTransport("m1")
	got := importFrom(t, peer, log, theirs, &recording{MemTransport: log})
	if got.Applied != 2 {
		t.Fatalf("applied = %d, want 2", got.Applied)
	}
	if len(got.Tables) != 2 {
		t.Fatalf("tables = %v, want the binding and its round", got.Tables)
	}
	var entries int
	var bytes int64
	for table, tally := range got.Tables {
		entries += tally.ImportedEntries
		bytes += tally.ImportedBytes
		if tally.ExportedEntries != 0 {
			t.Errorf("%s exported = %d on an import, want 0", table, tally.ExportedEntries)
		}
	}
	if entries != got.Applied {
		t.Errorf("tallied entries = %d, applied = %d", entries, got.Applied)
	}
	if bytes <= 0 {
		t.Error("tallied bytes are not positive for two upserts carrying rows")
	}
}

// The handle counts the transfers it completed: one push per append, one pull
// per pull, one push per marks publish and compact. An empty append and an
// empty publish move nothing, as on the worker, so they count nothing.
func TestMemTransportCountsTransfers(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("m1")
	if _, err := log.Append([]Entry{}); err != nil {
		t.Fatalf("empty append: %v", err)
	}
	if err := log.PublishMarks(map[string]int{}, time.Now()); err != nil {
		t.Fatalf("empty publish: %v", err)
	}
	stats, err := log.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TursoPushes != 0 || stats.TursoPulls != 0 {
		t.Fatalf("transfers = %d pushes/%d pulls after no-ops, want 0/0", stats.TursoPushes, stats.TursoPulls)
	}

	written, err := log.Append([]Entry{{Table: "binding", PK: `["01A"]`, Op: OpUpsert, Body: []byte(`{}`)}})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("appended %d entries, want 1", len(written))
	}
	if _, err := log.Pull(nil); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if err := log.PublishMarks(map[string]int{"m2": 0}, time.Now()); err != nil {
		t.Fatalf("PublishMarks: %v", err)
	}
	if _, err := log.CompactOwn(1); err != nil {
		t.Fatalf("CompactOwn: %v", err)
	}

	stats, err = log.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TursoPushes != 3 {
		t.Errorf("pushes = %d, want 1 append + 1 publish + 1 compact", stats.TursoPushes)
	}
	if stats.TursoPulls != 1 {
		t.Errorf("pulls = %d, want 1", stats.TursoPulls)
	}
}
