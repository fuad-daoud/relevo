package synclog

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// The transport numbers an append from the log's own high-water mark rather than
// from a counter it kept. That is what stops a machine whose local file was
// restored from a backup -- arriving here believing nothing was ever written --
// from handing out a number the log already holds.
func TestSyncFakeAppendNumbersFromTheLogsHighest(t *testing.T) {
	t.Parallel()
	m := NewMemTransport("m1")

	first, err := m.Append([]Entry{
		syncEntry(t, "m1", "binding", `["01ABC"]`, map[string]any{"id": "01ABC"}),
		syncEntry(t, "m1", "binding", `["02DEF"]`, map[string]any{"id": "02DEF"}),
	})
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if got := seqsOf(first); got != "1,2" {
		t.Fatalf("first append seqs = %s, want 1,2", got)
	}

	// A second machine writes its own entries in the same log. They are numbered
	// in its own sequence, which starts at one because it owns no number yet --
	// one sequence per origin, not one per log.
	other := m.OnLog("m2")
	if _, err := other.Append([]Entry{syncEntry(t, "m2", "binding", `["03GHI"]`, map[string]any{"id": "03GHI"})}); err != nil {
		t.Fatalf("other origin append: %v", err)
	}

	// This machine comes back with its local file restored from a backup: it
	// believes nothing was ever written and its own numbering would start at one
	// again. The log's highest number is what it has to start from.
	restored := m.OnLog("m1")
	second, err := restored.Append([]Entry{syncEntry(t, "m1", "binding", `["04JKL"]`, map[string]any{"id": "04JKL"})})
	if err != nil {
		t.Fatalf("append after a restore: %v", err)
	}
	if got := seqsOf(second); got != "3" {
		t.Fatalf("append after a restore seqs = %s, want 3", got)
	}
}

// One append is one write: the entries of a batch share the sequence number of
// the first of them, so a reader can see where an atomic batch ends.
func TestSyncFakeAppendGivesEachBatchItsOwnNumber(t *testing.T) {
	t.Parallel()
	m := NewMemTransport("m1")

	first, err := m.Append([]Entry{
		syncEntry(t, "m1", "binding", `["01ABC"]`, map[string]any{"id": "01ABC"}),
		syncEntry(t, "m1", "binding", `["02DEF"]`, map[string]any{"id": "02DEF"}),
	})
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	second, err := m.Append([]Entry{
		syncEntry(t, "m1", "binding", `["03GHI"]`, map[string]any{"id": "03GHI"}),
	})
	if err != nil {
		t.Fatalf("second append: %v", err)
	}

	// Two appends are two batches: the second does not continue the first.
	if first[0].Batch == second[0].Batch {
		t.Errorf("two appends share batch %d, want one batch each", first[0].Batch)
	}
	// Every entry of one append carries that append's batch, so a reader can
	// find the whole of it and apply it whole.
	for _, e := range first {
		if e.Batch != first[0].Batch {
			t.Errorf("entry seq %d has batch %d, want %d", e.Seq, e.Batch, first[0].Batch)
		}
	}
	if second[0].Batch != second[0].Seq {
		t.Errorf("a one-entry batch %d is not its own first seq %d", second[0].Batch, second[0].Seq)
	}
}

// A pull returns whole batches, so a reader applying what it got applies exactly
// the batches the writer wrote -- a half-batch would apply an order that was
// never written.
func TestSyncFakePullReturnsWholeBatches(t *testing.T) {
	t.Parallel()
	writer := NewMemTransport("m1")
	if _, err := writer.Append([]Entry{

		syncEntry(t, "m1", "binding", `["01ABC"]`, map[string]any{"id": "01ABC"}),
		syncEntry(t, "m1", "binding", `["02DEF"]`, map[string]any{"id": "02DEF"}),
	}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if _, err := writer.Append([]Entry{
		syncEntry(t, "m1", "binding", `["03GHI"]`, map[string]any{"id": "03GHI"}),
	}); err != nil {
		t.Fatalf("second append: %v", err)
	}

	reader := writer.OnLog("m2")
	got, err := reader.Pull(nil)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("pull returned %d entries, want all 3", len(got))
	}
	for i, e := range got {
		if e.Seq != i+1 {
			t.Errorf("entry %d has seq %d, want %d", i, e.Seq, i+1)
		}
	}

	// A mark landing inside the first batch does not split it. That batch was
	// written before the mark and is not half-applied, so it is skipped whole
	// and the reader resumes at the next batch.
	mid := got[0].Batch
	rest, err := reader.Pull(map[string]int{"m1": mid})
	if err != nil {
		t.Fatalf("pull after a mid-batch mark: %v", err)
	}
	if len(rest) != 1 {
		t.Fatalf("pull after a mid-batch mark returned %d entries (%s), want only the single-entry second batch",
			len(rest), seqsOf(rest))
	}
	if rest[0].Seq != 3 || rest[0].Batch != 3 {
		t.Errorf("pull after a mid-batch mark returned seq %d batch %d, want the batch at seq 3", rest[0].Seq, rest[0].Batch)
	}
}

// A mark is how far one origin has been applied, and an origin with no mark is
// read from the start of its log -- which is what a machine that has never
// imported from that installation sees.
func TestSyncFakePullReturnsOnlyWhatIsPastTheMark(t *testing.T) {
	t.Parallel()
	writer := NewMemTransport("m1")
	for _, id := range []string{"01ABC", "02DEF", "03GHI"} {
		if _, err := writer.Append([]Entry{syncEntry(t, "m1", "binding", `["`+id+`"]`, map[string]any{"id": id})}); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
	}

	reader := writer.OnLog("m2")
	got, err := reader.Pull(map[string]int{"m1": 2})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("pull after mark 2 returned %s, want only seq 3", seqsOf(got))
	}

	// A mark at the log's end returns nothing, and a mark past it does not read
	// backwards.
	for _, mark := range []int{3, 4, 99} {
		got, err := reader.Pull(map[string]int{"m1": mark})
		if err != nil {
			t.Fatalf("pull after mark %d: %v", mark, err)
		}
		if len(got) != 0 {
			t.Errorf("pull after mark %d returned %s, want nothing", mark, seqsOf(got))
		}
	}

	// Another origin's mark says nothing about this one.
	got, err = reader.Pull(map[string]int{"m9": 99})
	if err != nil {
		t.Fatalf("pull with another origin's mark: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("pull with another origin's mark returned %d entries, want all 3", len(got))
	}
}

// An installation appends only for the rows it owns and reads only the entries of
// other origins, so an append naming another origin is refused and a pull never
// returns the reader's own.
func TestSyncFakeRefusesEntriesOfAnotherOrigin(t *testing.T) {
	t.Parallel()
	m := NewMemTransport("m1")

	if _, err := m.Append([]Entry{syncEntry(t, "m2", "binding", `["01ABC"]`, map[string]any{"id": "01ABC"})}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("append as another origin: err = %v, want ErrInvalid", err)
	}

	if _, err := m.Append([]Entry{syncEntry(t, "m1", "binding", `["01ABC"]`, map[string]any{"id": "01ABC"})}); err != nil {
		t.Fatalf("append as this origin: %v", err)
	}
	got, err := m.Pull(map[string]int{"m1": 0})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("pull returned %d of this origin's own entries, want none", len(got))
	}
}

// An entry of the wrong shape is refused at the transport as well as at
// construction: a caller that built one by hand must not get it serialized.
func TestSyncFakeRefusesAnEntryOfTheWrongShape(t *testing.T) {
	t.Parallel()
	m := NewMemTransport("m1")
	bad := []Entry{
		{Op: OpDelete, Table: "binding", PK: `["01ABC"]`, Body: []byte(`{"id":"01ABC"}`)},
		{Op: OpUpsert, Table: "binding", PK: `["01ABC"]`},
		{Op: "insert", Table: "binding", PK: `["01ABC"]`, Body: []byte(`{}`)},
	}
	for i, e := range bad {
		if _, err := m.Append([]Entry{e}); !errors.Is(err, ErrInvalid) {
			t.Errorf("append %d: err = %v, want ErrInvalid", i, err)
		}
	}
	if stats, _ := m.Stats(); stats.Entries != 0 {
		t.Errorf("a refused append left %d entries in the log", stats.Entries)
	}
}

// `head` is the state reconcile compares against, so it holds one row per table
// and key: the latest entry's sequence number and that entry's body hash. A
// second write to the same row replaces them rather than adding a second row.
func TestSyncFakeHeadTracksTheLatestStatePerRow(t *testing.T) {
	t.Parallel()
	m := NewMemTransport("m1")

	const pk = `["01ABC"]`
	body := syncEntry(t, "m1", "binding", pk, map[string]any{"id": "01ABC", "note": "first"})
	hash, err := BodyHash(body.Body)
	if err != nil {
		t.Fatalf("BodyHash: %v", err)
	}
	if _, err := m.Append([]Entry{body}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	rows, err := m.Head("m1")
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("head holds %d rows, want 1", len(rows))
	}
	if rows[0].Table != "binding" || rows[0].PK != pk || rows[0].Seq != 1 {
		t.Errorf("head row = %+v, want binding %s at seq 1", rows[0], pk)
	}
	if rows[0].Hash != hash {
		t.Errorf("head hash = %s, want the body's hash %s", rows[0].Hash, hash)
	}

	// Writing the row again moves its sequence and its hash, and leaves one row.
	updated := syncEntry(t, "m1", "binding", pk, map[string]any{"id": "01ABC", "note": "second"})
	updatedHash, err := BodyHash(updated.Body)
	if err != nil {
		t.Fatalf("BodyHash updated: %v", err)
	}
	written, err := m.Append([]Entry{updated})
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	rows, err = m.Head("m1")
	if err != nil {
		t.Fatalf("head after the update: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("head holds %d rows after an update, want 1", len(rows))
	}
	if rows[0].Seq != written[0].Seq || rows[0].Hash != updatedHash {
		t.Errorf("head row = %+v, want seq %d hash %s", rows[0], written[0].Seq, updatedHash)
	}
	if updatedHash == hash {
		t.Fatal("the changed body hashed the same as the first, so the test proves nothing")
	}
}

// A row the origin has deleted is not a row reconcile should keep proposing, so
// a delete drops the row from that origin's head.
func TestSyncFakeHeadDropsADeletedRow(t *testing.T) {
	t.Parallel()
	m := NewMemTransport("m1")

	const pk = `["01ABC"]`
	if _, err := m.Append([]Entry{syncEntry(t, "m1", "binding", pk, map[string]any{"id": "01ABC"})}); err != nil {
		t.Fatalf("append the row: %v", err)
	}
	if _, err := m.Append([]Entry{syncDelete(t, "m1", "binding", pk)}); err != nil {
		t.Fatalf("append the delete: %v", err)
	}

	rows, err := m.Head("m1")
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("head holds %+v after the row was deleted, want nothing", rows)
	}

	// The delete itself is still in the log: another installation has to be told
	// to remove the row, and head is not how it learns.
	reader := m.OnLog("m2")
	got, err := reader.Pull(nil)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(got) != 2 || got[1].Op != OpDelete {
		t.Errorf("pull returned %+v, want the write and the delete", got)
	}
}

// Head rows come back in the parents-first order a local walk uses, so a reader
// meeting them in pages never sees a child before the parent it needs.
func TestSyncFakeHeadComesBackParentsFirst(t *testing.T) {
	t.Parallel()
	m := NewMemTransport("m1")

	// Appended children-first, which is the order a drain can hold them in.
	if _, err := m.Append([]Entry{
		syncEntry(t, "m1", "binding_event", `["01ABC",1]`, map[string]any{"record_id": "01ABC", "seq": int64(1)}),
		syncEntry(t, "m1", "binding_record", `["01ABC"]`, map[string]any{"id": "01ABC"}),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	rows, err := m.Head("m1")
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("head holds %d rows, want 2", len(rows))
	}
	// SharedTables lists binding_record before binding_event; a reader that saw
	// the child first would have no parent to apply it against.
	if rows[0].Table != "binding_record" {
		t.Errorf("head starts at %s, want binding_record first", rows[0].Table)
	}
}

// `head` is read off the log's entries, so it holds one row per table and key:
// the newest entry's sequence and that entry's doc-aware hash. A delete drops
// the row until a later write brings it back, and each origin reads only the
// entries it wrote itself.
func TestMemHeadDerivesNewestPerRow(t *testing.T) {
	t.Parallel()
	m1 := NewMemTransport("m1")
	m2 := m1.OnLog("m2")

	readHead := func(t *testing.T, m *MemTransport, origin string) []HeadRow {
		t.Helper()
		rows, err := m.Head(origin)
		if err != nil {
			t.Fatalf("head %s: %v", origin, err)
		}
		return rows
	}
	findRow := func(rows []HeadRow, table, pk string) (HeadRow, bool) {
		for _, row := range rows {
			if row.Table == table && row.PK == pk {
				return row, true
			}
		}
		return HeadRow{}, false
	}

	// Children-first appends still read back parents-first, and keys in order:
	// the walk order is the table's place in SharedTables, then the key text.
	if _, err := m1.Append([]Entry{
		syncEntry(t, "m1", "binding_event", `["02DEF",1]`, map[string]any{"record_id": "02DEF", "seq": int64(1)}),
		syncEntry(t, "m1", "binding_record", `["02DEF"]`, map[string]any{"id": "02DEF"}),
		syncEntry(t, "m1", "binding", `["02DEF"]`, map[string]any{"id": "02DEF"}),
		syncEntry(t, "m1", "binding", `["01ABC"]`, map[string]any{"id": "01ABC"}),
	}); err != nil {
		t.Fatalf("append out of order: %v", err)
	}
	rows := readHead(t, m1, "m1")
	wantOrder := [][2]string{
		{"binding_record", `["02DEF"]`},
		{"binding", `["01ABC"]`},
		{"binding", `["02DEF"]`},
		{"binding_event", `["02DEF",1]`},
	}
	if len(rows) != len(wantOrder) {
		t.Fatalf("head holds %d rows, want %d", len(rows), len(wantOrder))
	}
	for i, want := range wantOrder {
		if rows[i].Table != want[0] || rows[i].PK != want[1] {
			t.Errorf("head row %d = %s %s, want %s %s", i, rows[i].Table, rows[i].PK, want[0], want[1])
		}
	}

	// A second write to one row replaces its sequence and hash, leaving one row.
	const pk = `["09UPD"]`
	if _, err := m1.Append([]Entry{syncEntry(t, "m1", "binding", pk, map[string]any{"id": "09UPD", "note": "first"})}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	rewrite := syncEntry(t, "m1", "binding", pk, map[string]any{"id": "09UPD", "note": "second"})
	written, err := m1.Append([]Entry{rewrite})
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	wantHash, err := entryHash(written[0])
	if err != nil {
		t.Fatalf("entryHash: %v", err)
	}
	row, ok := findRow(readHead(t, m1, "m1"), "binding", pk)
	if !ok {
		t.Fatalf("head has no row for binding %s", pk)
	}
	if row.Seq != written[0].Seq || row.Hash != wantHash {
		t.Errorf("head row = %+v, want seq %d hash %s", row, written[0].Seq, wantHash)
	}

	// A delete drops the row while a live one stays.
	if _, err := m1.Append([]Entry{syncDelete(t, "m1", "binding", pk)}); err != nil {
		t.Fatalf("append the delete: %v", err)
	}
	rows = readHead(t, m1, "m1")
	if _, ok := findRow(rows, "binding", pk); ok {
		t.Errorf("head still holds binding %s after its delete", pk)
	}
	if _, ok := findRow(rows, "binding", `["01ABC"]`); !ok {
		t.Errorf("head lost a live row when another was deleted")
	}

	// A write after the delete brings the row back at the new sequence.
	recreate := syncEntry(t, "m1", "binding", pk, map[string]any{"id": "09UPD", "note": "third"})
	rewritten, err := m1.Append([]Entry{recreate})
	if err != nil {
		t.Fatalf("recreate append: %v", err)
	}
	wantRecreate, err := entryHash(rewritten[0])
	if err != nil {
		t.Fatalf("entryHash: %v", err)
	}
	row, ok = findRow(readHead(t, m1, "m1"), "binding", pk)
	if !ok {
		t.Fatalf("head has no row for recreated binding %s", pk)
	}
	if row.Seq != rewritten[0].Seq || row.Hash != wantRecreate {
		t.Errorf("head row = %+v, want seq %d hash %s", row, rewritten[0].Seq, wantRecreate)
	}

	// Another origin's write to the same row is its own state, not this one's.
	other := syncEntry(t, "m2", "binding", pk, map[string]any{"id": "09UPD", "note": "elsewhere"})
	otherWritten, err := m2.Append([]Entry{other})
	if err != nil {
		t.Fatalf("other origin append: %v", err)
	}
	wantOther, err := entryHash(otherWritten[0])
	if err != nil {
		t.Fatalf("entryHash: %v", err)
	}
	otherRows := readHead(t, m2, "m2")
	if len(otherRows) != 1 {
		t.Fatalf("other origin head holds %d rows, want only its own", len(otherRows))
	}
	if otherRows[0].Table != "binding" || otherRows[0].PK != pk || otherRows[0].Hash != wantOther {
		t.Errorf("other origin head row = %+v, want binding %s at its own hash", otherRows[0], pk)
	}
	row, ok = findRow(readHead(t, m1, "m1"), "binding", pk)
	if !ok || row.Seq != rewritten[0].Seq || row.Hash != wantRecreate {
		t.Errorf("m1 head row = %+v, want it unchanged at seq %d", row, rewritten[0].Seq)
	}

	// A doc entry's hash is over its stamp and value, not the row encoding.
	doc := Entry{
		Op:    OpUpsert,
		Table: "doc:config",
		PK:    `["policy"]`,
		Body:  json.RawMessage(`{"stamp":"5","value":{"x":1}}`),
	}
	docWritten, err := m1.Append([]Entry{doc})
	if err != nil {
		t.Fatalf("doc append: %v", err)
	}
	wantDoc, err := entryHash(docWritten[0])
	if err != nil {
		t.Fatalf("entryHash: %v", err)
	}
	if bodyHash, err := BodyHash(docWritten[0].Body); err == nil && bodyHash == wantDoc {
		t.Fatal("the doc body hashes the same under BodyHash, so the case proves nothing")
	}
	docRow, ok := findRow(readHead(t, m1, "m1"), "doc:config", `["policy"]`)
	if !ok {
		t.Fatalf("head has no row for doc:config policy")
	}
	if docRow.Hash != wantDoc {
		t.Errorf("doc head hash = %s, want the entry hash %s", docRow.Hash, wantDoc)
	}
}

// A stored entry no append could have written still refuses at read time rather
// than handing reconcile a row with no hash: entries reach the log only
// through the validating append, so this state is built by hand.
func TestMemHeadReportsAnUnhashableStoredEntry(t *testing.T) {
	t.Parallel()
	m := NewMemTransport("m1")
	if _, err := m.Append([]Entry{syncEntry(t, "m1", "binding", `["01ABC"]`, map[string]any{"id": "01ABC"})}); err != nil {
		t.Fatalf("append: %v", err)
	}
	m.log.mu.Lock()
	m.log.entries = append(m.log.entries, Entry{
		Op:     OpUpsert,
		Table:  "binding",
		PK:     `["09BAD"]`,
		Origin: "m1",
		Seq:    2,
		Body:   json.RawMessage(`not json`),
	})
	m.log.mu.Unlock()

	rows, err := m.Head("m1")
	if err == nil {
		t.Fatalf("head with an unhashable entry returned %+v, want an error", rows)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("head err = %v, want ErrInvalid", err)
	}
	if len(rows) != 0 {
		t.Errorf("head returned %d rows with an unhashable entry, want none", len(rows))
	}
}

// Compaction drops only superseded entries, so one origin's derived head reads
// the same on either side of it: the newest entry per row survives the floor,
// and another origin's rows are never in reach.
func TestMemHeadAfterCompactMatchesBefore(t *testing.T) {
	t.Parallel()
	m1 := NewMemTransport("m1")
	m2 := m1.OnLog("m2")

	// Row A superseded, row B deleted, row C written once: the compactable,
	// the absent and the untouched shapes in one log.
	if _, err := m1.Append([]Entry{syncEntry(t, "m1", "binding", `["0AAAA"]`, map[string]any{"id": "0AAAA", "note": "first"})}); err != nil {
		t.Fatalf("append A first: %v", err)
	}
	if _, err := m1.Append([]Entry{syncEntry(t, "m1", "binding", `["0AAAA"]`, map[string]any{"id": "0AAAA", "note": "second"})}); err != nil {
		t.Fatalf("append A second: %v", err)
	}
	if _, err := m1.Append([]Entry{syncEntry(t, "m1", "binding", `["0BBBB"]`, map[string]any{"id": "0BBBB"})}); err != nil {
		t.Fatalf("append B: %v", err)
	}
	if _, err := m1.Append([]Entry{syncDelete(t, "m1", "binding", `["0BBBB"]`)}); err != nil {
		t.Fatalf("append B delete: %v", err)
	}
	last, err := m1.Append([]Entry{syncEntry(t, "m1", "binding", `["0CCCC"]`, map[string]any{"id": "0CCCC"})})
	if err != nil {
		t.Fatalf("append C: %v", err)
	}
	if _, err := m2.Append([]Entry{syncEntry(t, "m2", "binding", `["0DDDD"]`, map[string]any{"id": "0DDDD"})}); err != nil {
		t.Fatalf("append m2 row: %v", err)
	}

	before, err := m1.Head("m1")
	if err != nil {
		t.Fatalf("head before: %v", err)
	}
	beforeOther, err := m2.Head("m2")
	if err != nil {
		t.Fatalf("other head before: %v", err)
	}

	// The floor is the highest sequence m1 holds, so both superseded entries
	// sit at or below it and are in reach of the drop.
	dropped, err := m1.Compact("m1", last[0].Seq)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if dropped == 0 {
		t.Fatal("compaction dropped nothing, so the test pins nothing")
	}

	after, err := m1.Head("m1")
	if err != nil {
		t.Fatalf("head after: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("head after compaction = %+v, want the head from before %+v", after, before)
	}
	for _, row := range after {
		if row.PK == `["0BBBB"]` {
			t.Errorf("head after compaction holds deleted row %+v", row)
		}
	}
	afterOther, err := m2.Head("m2")
	if err != nil {
		t.Fatalf("other head after: %v", err)
	}
	if !reflect.DeepEqual(afterOther, beforeOther) {
		t.Errorf("other head after compacting m1 = %+v, want %+v", afterOther, beforeOther)
	}
}

// An entry no hash can digest never enters the log: the append refuses the
// whole batch, so a later head has nothing to refuse and the log holds nothing.
func TestMemAppendRefusesAnUnhashableBodyAtAppend(t *testing.T) {
	t.Parallel()

	m := NewMemTransport("m1")
	bad := Entry{Op: OpUpsert, Table: "binding", PK: `["01ABC"]`, Body: json.RawMessage(`not json`)}
	if _, err := m.Append([]Entry{
		syncEntry(t, "m1", "binding", `["02DEF"]`, map[string]any{"id": "02DEF"}),
		bad,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("append with an unhashable body: err = %v, want ErrInvalid", err)
	}
	if stats, _ := m.Stats(); stats.Entries != 0 {
		t.Errorf("a refused append left %d entries in the log", stats.Entries)
	}
	rows, err := m.Head("m1")
	if err != nil {
		t.Fatalf("head after a refused append: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("head after a refused append holds %+v, want nothing", rows)
	}

	// The doc half of the hash refuses the same way: a stamp with no value is
	// not a document, so the append carrying it is refused whole.
	docs := NewMemTransport("m1")
	docBad := Entry{Op: OpUpsert, Table: "doc:config", PK: `["policy"]`, Body: json.RawMessage(`{"stamp":"5"}`)}
	if _, err := docs.Append([]Entry{docBad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("append with an unhashable doc body: err = %v, want ErrInvalid", err)
	}
	if stats, _ := docs.Stats(); stats.Entries != 0 {
		t.Errorf("a refused doc append left %d entries in the log", stats.Entries)
	}
	docRows, err := docs.Head("m1")
	if err != nil {
		t.Fatalf("doc head after a refused append: %v", err)
	}
	if len(docRows) != 0 {
		t.Errorf("doc head after a refused append holds %+v, want nothing", docRows)
	}
}

// Two installations sharing one log is the whole arrangement: one appends and
// numbers, the other reads what is past its mark, and neither reads the other's
// entries as its own or writes as the other.
func TestSyncFakeTwoInstallationsShareOneLog(t *testing.T) {
	t.Parallel()
	one := NewMemTransport("m1")
	two := one.OnLog("m2")

	written, err := one.Append([]Entry{syncEntry(t, "m1", "binding", `["01ABC"]`, map[string]any{"id": "01ABC"})})
	if err != nil {
		t.Fatalf("m1 append: %v", err)
	}
	if _, err := two.Append([]Entry{syncEntry(t, "m2", "binding", `["02DEF"]`, map[string]any{"id": "02DEF"})}); err != nil {
		t.Fatalf("m2 append: %v", err)
	}

	// Each reads what the other wrote, and each numbers its own sequence from
	// one: the two sequences are independent, which is what lets two machines
	// write the same log without coordinating a single number.
	got, err := two.Pull(map[string]int{"m1": 0})
	if err != nil {
		t.Fatalf("m2 pull: %v", err)
	}
	if len(got) != 1 || got[0].Origin != "m1" || got[0].Seq != written[0].Seq {
		t.Errorf("m2 pulled %+v, want m1's entry at seq %d", got, written[0].Seq)
	}
	if other, err := one.Pull(map[string]int{"m2": 0}); err != nil {
		t.Fatalf("m1 pull: %v", err)
	} else if len(other) != 1 || other[0].Origin != "m2" {
		t.Errorf("m1 pulled %+v, want only m2's entry", other)
	}

	// A pull never returns the caller's own entries, whatever mark it is given:
	// m1's own writes are already in m1's file, so reading them back would apply
	// them twice.
	if got, err := one.Pull(map[string]int{"m1": 0}); err != nil {
		t.Fatalf("m1 pull: %v", err)
	} else if len(got) != 1 || got[0].Origin != "m2" {
		t.Errorf("m1 pulled %+v, want only m2's entry", got)
	}
	if got, err := one.Pull(map[string]int{"m1": 0, "m2": 1}); err != nil {
		t.Fatalf("m1 pull with both marks: %v", err)
	} else if len(got) != 0 {
		t.Errorf("m1 pulled %+v with every mark applied, want nothing", got)
	}
	// And neither side can append as the other, which is what stops two machines
	// writing the same row.
	if _, err := one.Append([]Entry{syncEntry(t, "m2", "binding", `["03GHI"]`, map[string]any{"id": "03GHI"})}); !errors.Is(err, ErrInvalid) {
		t.Errorf("m1 appending as m2: err = %v, want ErrInvalid", err)
	}
}

// The stats a caller reads to tell an empty log from an unanswered one.
func TestSyncFakeStatsReportTheLog(t *testing.T) {
	t.Parallel()
	m := NewMemTransport("m1")

	stats, err := m.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Entries != 0 || stats.Origins != 0 || stats.Seq != 0 {
		t.Errorf("empty log reported %+v, want all zero", stats)
	}

	if _, err := m.Append([]Entry{
		syncEntry(t, "m1", "binding", `["01ABC"]`, map[string]any{"id": "01ABC"}),
		syncEntry(t, "m1", "binding", `["02DEF"]`, map[string]any{"id": "02DEF"}),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	stats, err = m.Stats()
	if err != nil {
		t.Fatalf("stats after the append: %v", err)
	}
	if stats.Entries != 2 || stats.Origins != 1 || stats.Seq != 2 {
		t.Errorf("stats = %+v, want 2 entries, 1 origin, seq 2", stats)
	}
}

// Appending nothing writes nothing: the exporter may have drained an outbox that
// held only rows it skipped.
func TestSyncFakeAppendOfNothingWritesNothing(t *testing.T) {
	t.Parallel()
	m := NewMemTransport("m1")

	written, err := m.Append(nil)
	if err != nil {
		t.Fatalf("append nothing: %v", err)
	}
	if len(written) != 0 {
		t.Errorf("appending nothing returned %d entries", len(written))
	}
	stats, _ := m.Stats()
	if stats.Entries != 0 {
		t.Errorf("appending nothing left %d entries in the log", stats.Entries)
	}
}

// seqsOf renders a run of entries' sequence numbers for a failure message.
func seqsOf(entries []Entry) string {
	out := ""
	for i, e := range entries {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprint(e.Seq)
	}
	return out
}
