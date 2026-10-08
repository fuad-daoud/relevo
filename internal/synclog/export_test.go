package synclog

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The exporter's contract is the batch it hands the log: an owned write becomes
// one upsert carrying the row as the file holds it, a row that is gone becomes
// a delete carrying only its key, and a write another installation owns is
// neither exported nor left behind.
//
// Every case runs against a real file. The outbox entries under test are the
// ones the triggers recorded, so a test that inserted them by hand would be
// pinning a shape no user's write ever produces.

// exporterFile is one installation's own file, opened the way that machine
// opens it: as the installation it is, which is what makes a row it wrote its
// own and a row another machine wrote a stranger's. Foreign keys are on by the
// engine's default, so a cascade in a test is a cascade a user's write causes.
func exporterFile(t *testing.T, origin string) (*db.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := db.OpenWith(path, db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("open the file of %s: %v", origin, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, path
}

// seed runs the statements a caller fills a file with. The file's own triggers
// record what the statements changed, so a test writes rows and never an outbox
// entry.
func seed(t *testing.T, path string, statements ...string) {
	t.Helper()
	raw, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("open the file for writing: %v", err)
	}
	defer func() { _ = raw.Close() }()
	for _, stmt := range statements {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("write %s: %v", stmt, err)
		}
	}
}

// outboxRows is what the file still holds in its outbox, one "seq tbl pk op" per
// entry, so a test can say which changes are still waiting to travel.
func outboxRows(t *testing.T, d *db.DB) string {
	t.Helper()
	raw, err := db.OpenRawReadOnly(d.Path())
	if err != nil {
		t.Fatalf("open the file for reading: %v", err)
	}
	defer func() { _ = raw.Close() }()

	rows, err := raw.Query(`SELECT seq, tbl, pk, op FROM sync_outbox ORDER BY seq`)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var seq int
		var tbl, pk, op string
		if err := rows.Scan(&seq, &tbl, &pk, &op); err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		out = append(out, fmt.Sprintf("%d %s %s %s", seq, tbl, pk, op))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	return strings.Join(out, "|")
}

// recording is a transport that keeps what it was handed, so a test reads the
// batch the exporter built rather than the one a remote would later serve. It
// forwards to the fake underneath, so the entries it records are entries the
// log actually holds.
type recording struct {
	*MemTransport
	batches [][]Entry
	refuse  bool
}

// Append records the batch and hands it on, or refuses it where a test set the
// transport to refuse. A refusal is the ordinary failure a remote reports, and
// it is the one the exporter has to survive without losing the entries.
func (r *recording) Append(entries []Entry) ([]Entry, error) {
	r.batches = append(r.batches, entries)
	if r.refuse {
		return nil, fmt.Errorf("synclog: the log refused the append")
	}
	return r.MemTransport.Append(entries)
}

// appended is every entry the exporter handed over, in the order it handed them.
func (r *recording) appended() []Entry {
	var out []Entry
	for _, batch := range r.batches {
		out = append(out, batch...)
	}
	return out
}

// shape is each recorded entry as "table pk op": what an importer acts on. The
// body is asserted on its own where it carries the claim.
func (r *recording) shape() string {
	var out []string
	for _, e := range r.appended() {
		out = append(out, fmt.Sprintf("%s %s %s", e.Table, e.PK, e.Op))
	}
	return strings.Join(out, "|")
}

// exporter returns an exporter over d writing to r, with the clock fixed so an
// entry's timestamp is a value rather than the moment the test happened to run.
func exporter(d *db.DB, r *recording) *Exporter {
	e := NewExporter(d, r)
	e.now = syncClock
	return e
}

// insertBinding is the row every case starts from: a binding of the named
// installation. The name follows the id because one installation holds at most
// one binding of a name.
func insertBinding(id, origin string) string {
	return fmt.Sprintf(`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin)
		VALUES ('%s', '%s', '/x', 'local', 't', 'manual', '%s')`, id, id, origin)
}

// insertForkedBinding writes a binding forked from a named one, so a case has a
// shared table's self-reference, which the exporter's table sort cannot order. An
// empty parent writes a plain binding of the origin.
func insertForkedBinding(id, parent, origin string) string {
	forked := "NULL"
	if parent != "" {
		forked = "'" + parent + "'"
	}
	return fmt.Sprintf(`INSERT INTO binding (id, name, forked_from_binding_id, cwd, builder_mode, created_at, ingest_source, origin)
		VALUES ('%s', '%s', %s, '/x', 'local', 't', 'manual', '%s')`, id, id, forked, origin)
}

// One owned insert travels as one upsert carrying the row, and the outbox entry
// that recorded it is gone once the transport has taken it. An entry left
// behind would be read again on every pass; one deleted before the append would
// be lost if the append failed.
func TestExporterAppendsOwnedInsertAndClearsTheOutbox(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"))
	if got := outboxRows(t, d); got != `1 binding ["01A"] insert` {
		t.Fatalf("outbox before export = %q, want the one insert its trigger recorded", got)
	}

	r := &recording{MemTransport: NewMemTransport("m1")}
	got, err := exporter(d, r).Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if got.Drained != 1 || got.Appended != 1 {
		t.Fatalf("export = %+v, want one entry drained and one appended", got)
	}
	if shape := r.shape(); shape != `binding ["01A"] upsert` {
		t.Fatalf("appended = %q, want one upsert of the binding", shape)
	}

	// The body is the row as the file holds it, so an importer can write it
	// without asking this machine what the row meant.
	body, err := DecodeBody(r.appended()[0].Body)
	if err != nil {
		t.Fatalf("read the appended body: %v", err)
	}
	if body["id"] != "01A" || body["name"] != "01A" || body["origin"] != "m1" {
		t.Fatalf("appended body = %v, want the binding's own columns", body)
	}
	if out := outboxRows(t, d); out != "" {
		t.Fatalf("outbox after export = %q, want it empty", out)
	}
}

// Repeated writes to one row are one entry, and that entry says what the row
// holds now: an insert and the updates behind it are one row, and a row that was
// created and removed again is not there to be written. Without the fold the log
// would carry the row three times over and an importer would write it three
// times, the last of which is the only one that matters.
func TestExporterCoalescesRepeatedEntries(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path,
		insertBinding("01A", "m1"),
		`UPDATE binding SET tier = 'second' WHERE id = '01A'`,
		`UPDATE binding SET tier = 'third' WHERE id = '01A'`,
		insertBinding("01B", "m1"),
		`DELETE FROM binding WHERE id = '01B'`,
	)
	if got := outboxRows(t, d); got == "" {
		t.Fatal("outbox is empty, want the five writes its triggers recorded")
	}

	r := &recording{MemTransport: NewMemTransport("m1")}
	got, err := exporter(d, r).Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if got.Drained != 5 || got.Appended != 2 {
		t.Fatalf("export = %+v, want five entries drained and one entry per row appended", got)
	}
	if shape := r.shape(); shape != `binding ["01A"] upsert|binding ["01B"] delete` {
		t.Fatalf("appended = %q, want one entry per row, the removed one a delete", shape)
	}

	// The state is the one the drain read, not the first write's: a batch
	// carrying the row as it was three writes ago would undo the other two.
	body, err := DecodeBody(r.appended()[0].Body)
	if err != nil {
		t.Fatalf("read the appended body: %v", err)
	}
	if body["tier"] != "third" {
		t.Fatalf("appended tier = %v, want the value the row holds now", body["tier"])
	}
	if out := outboxRows(t, d); out != "" {
		t.Fatalf("outbox after export = %q, want it empty", out)
	}
}

// A row the file does not hold is a delete, and it carries no body: the key
// already names the row to remove, and a body beside it could disagree with the
// key about whether the row is there at all.
func TestExporterEmitsDeleteForAbsentRow(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path,
		insertBinding("01A", "m1"),
		`DELETE FROM binding WHERE id = '01A'`,
	)

	r := &recording{MemTransport: NewMemTransport("m1")}
	if _, err := exporter(d, r).Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	if shape := r.shape(); shape != `binding ["01A"] delete` {
		t.Fatalf("appended = %q, want one delete of the binding", shape)
	}
	if body := r.appended()[0].Body; len(body) != 0 {
		t.Fatalf("appended body = %s, want a delete carrying none", body)
	}
}

// An entry the transport refused is not deleted, and the next pass hands the
// same entries over. Deleting before the append would turn a refused batch into a
// lost change; the repeat is safe because import is an idempotent upsert, so the
// log holding a batch twice costs an importer one redundant write.
func TestExporterKeepsOutboxUntilAccept(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"))
	before := outboxRows(t, d)

	refused := &recording{MemTransport: NewMemTransport("m1"), refuse: true}
	got, err := exporter(d, refused).Export()
	if err == nil {
		t.Fatal("export through a refusing transport succeeded, want the refusal reported")
	}
	if got.Drained != 0 || got.Appended != 0 {
		t.Fatalf("export = %+v, want nothing counted as moved after a refusal", got)
	}
	if after := outboxRows(t, d); after != before {
		t.Fatalf("outbox after the refusal = %q, want it unchanged at %q", after, before)
	}

	accepted := &recording{MemTransport: NewMemTransport("m1")}
	if _, err := exporter(d, accepted).Export(); err != nil {
		t.Fatalf("export the retry: %v", err)
	}
	if shape := accepted.shape(); shape != `binding ["01A"] upsert` {
		t.Fatalf("the retry appended %q, want the entry the refusal held back", shape)
	}
	if out := outboxRows(t, d); out != "" {
		t.Fatalf("outbox after the retry = %q, want it empty", out)
	}
}

// A row this machine did not write is neither appended nor left behind: it would
// be appended back to the installation that wrote it and applied here a second
// time, so the echo and the endless re-read are stopped in the same place.
//
// Two shapes a drain meets are seeded here. A row another installation owns is
// what an import leaves -- the other machine's own file shows the entry that
// travelled, and this file carries the row it wrote, with that owner, because
// the columns travel as the body carried them. A child whose parent is already
// gone is what a cascade leaves, and its own entry names no owner at all.
func TestImportedRowsNeverEcho(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	other, otherPath := exporterFile(t, "m2")
	seed(t, otherPath, insertBinding("01A", "m2"))

	// What the other machine exports of that row is the entry that arrives here.
	theirLog := &recording{MemTransport: NewMemTransport("m2")}
	if _, err := exporter(other, theirLog).Export(); err != nil {
		t.Fatalf("export the other installation's own row: %v", err)
	}
	if shape := theirLog.shape(); shape != `binding ["01A"] upsert` {
		t.Fatalf("the other installation appended %q, want its own binding", shape)
	}

	// This machine holds the row that entry wrote, and the parent and child a
	// removal left behind it. Both name the other installation, and the child's
	// own entry names no owner at all, because the parent it would read is gone.
	seed(t, path,
		insertBinding("01A", "m2"),
		`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json, created_at, updated_at, origin)
		 VALUES ('02A', 'o', 'n', 'open', 1, '/z', '{}', 't', 't', 'm2')`,
		`INSERT INTO round_file (record_id, name, round, body, bytes, sha256, mtime, sealed_at)
		 VALUES ('02A', 'f', 1, X'00', 1, 's', 't', 't')`,
		`DELETE FROM binding_record WHERE id = '02A'`,
	)

	r := &recording{MemTransport: NewMemTransport("m1")}
	got, err := exporter(d, r).Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if got.Appended != 0 {
		t.Fatalf("appended %d entries (%s), want none of another machine's rows",
			got.Appended, r.shape())
	}
	if out := outboxRows(t, d); out != "" {
		t.Fatalf("outbox after export = %q, want the foreign and orphaned entries cleared", out)
	}
}

// One batch is one transport append, so a reader can tell where it ended, and
// the entries of it share its number. The exporter names no sequence of its own,
// so an entry it hands over carries none until the transport writes it.
func TestExporterAppendsEachDrainAsOneBatch(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path,
		insertBinding("01A", "m1"),
		`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json, created_at, updated_at, origin)
		 VALUES ('02A', 'o', 'n', 'open', 1, '/x', '{}', 't', 't', 'm1')`,
	)

	r := &recording{MemTransport: NewMemTransport("m1")}
	if _, err := exporter(d, r).Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(r.batches) != 1 {
		t.Fatalf("handed the transport %d batches, want one", len(r.batches))
	}
	for _, e := range r.batches[0] {
		if e.Seq != 0 || e.Batch != 0 {
			t.Fatalf("handed over an entry already numbered %d/%d, want the transport to number it", e.Seq, e.Batch)
		}
	}
	stats, err := r.Stats()
	if err != nil {
		t.Fatalf("read the log's stats: %v", err)
	}
	if stats.Entries != 2 {
		t.Fatalf("the log holds %d entries, want the two the batch carried", stats.Entries)
	}
	// Read back as a machine elsewhere would: both entries come back carrying
	// the one batch number, which is how a reader knows to apply them together.
	pulled, err := r.OnLog("m3").Pull(map[string]int{"m1": 0})
	if err != nil {
		t.Fatalf("pull what was appended: %v", err)
	}
	if len(pulled) != 2 || pulled[0].Batch != pulled[1].Batch {
		t.Fatalf("pulled = %+v, want both entries numbered from the one batch", pulled)
	}
}

// An outbox larger than one drain is drained across several appends, each of
// which is one write, and the whole outbox is empty afterwards.
func TestExporterDrainsBeyondOneBatch(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	for _, id := range []string{"01A", "01B", "01C", "01D", "01E", "01F"} {
		seed(t, path, insertBinding(id, "m1"))
	}

	r := &recording{MemTransport: NewMemTransport("m1")}
	e := exporter(d, r)
	e.batch = 4
	got, err := e.Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if got.Drained != 6 || got.Appended != 6 {
		t.Fatalf("export = %+v, want the six writes drained and appended", got)
	}
	if len(r.batches) != 2 {
		t.Fatalf("handed the transport %d batches, want two", len(r.batches))
	}
	if len(r.batches[0]) != 4 || len(r.batches[1]) != 2 {
		t.Fatalf("batch sizes = %d,%d, want 4,2", len(r.batches[0]), len(r.batches[1]))
	}
	if out := outboxRows(t, d); out != "" {
		t.Fatalf("outbox after export = %q, want it empty", out)
	}
}

// One batch is one consistent snapshot ordered so an importer can apply it with
// foreign keys on: a row meets its parent before anything that references it,
// and a row is removed before its parent.
//
// The parent here is written after the child that first referenced it, and the
// child is re-pointed at it. That is the case a first-position rule gets wrong:
// the child's earliest position is ahead of the parent that has to exist before
// it, so keeping positions would hand the importer a round whose binding it has
// not written. The deletes are the mirror -- the children go first, because the
// foreign keys of the original tables carry no cascade and an importer that
// removed the parent first would have the child's delete refused.
func TestExporterOrdersBatchParentsFirst(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path,
		insertBinding("01A", "m1"),
		insertRound("02A", "01A"),
		insertBinding("01B", "m1"),
		`UPDATE round SET binding_id = '01B' WHERE id = '02A'`,
	)

	r := &recording{MemTransport: NewMemTransport("m1")}
	if _, err := exporter(d, r).Export(); err != nil {
		t.Fatalf("export: %v", err)
	}
	if shape := r.shape(); shape != `binding ["01A"] upsert|binding ["01B"] upsert|round ["02A"] upsert` {
		t.Fatalf("appended = %q, want both bindings before the round that references one", shape)
	}

	// The child's body carries the parent it now points at, so the entry an
	// importer writes names a parent this same batch carries ahead of it.
	body, err := DecodeBody(r.appended()[2].Body)
	if err != nil {
		t.Fatalf("read the round's body: %v", err)
	}
	if body["binding_id"] != "01B" {
		t.Fatalf("the round's body names binding %v, want the parent this batch carries", body["binding_id"])
	}

	// The mirror, on a pair about to be removed.
	seed(t, path,
		insertBinding("03A", "m1"),
		insertRound("04A", "03A"),
		`DELETE FROM round WHERE id = '04A'`,
		`DELETE FROM binding WHERE id = '03A'`,
	)
	removals := &recording{MemTransport: NewMemTransport("m1")}
	if _, err := exporter(d, removals).Export(); err != nil {
		t.Fatalf("export the removals: %v", err)
	}
	if shape := removals.shape(); shape != `round ["04A"] delete|binding ["03A"] delete` {
		t.Fatalf("appended = %q, want the round removed before the binding", shape)
	}
}

// insertRound is a child of the binding named, so the case above has a row whose
// parent the importer must already hold. A child carries no owner of its own: the
// trigger resolves one through the parent it names.
func insertRound(id, bindingID string) string {
	return fmt.Sprintf(`INSERT INTO round (id, binding_id, number, started_at, outcome, switches)
		VALUES ('%s', '%s', 1, 't', 'done', 0)`, id, bindingID)
}

// insertEvent is a child of a round, which is itself a child of a binding, so it
// names two parents by key. It is the deepest row the import cases hold, which is
// what makes an order that writes parents late visible as a foreign-key refusal.
func insertEvent(id, bindingID, roundID string, seq int) string {
	return fmt.Sprintf(`INSERT INTO event (id, binding_id, round_id, seq, ts, kind, direction,
		confirmed, late, entry_json)
		VALUES ('%s', '%s', '%s', %d, 't', 'note', 'in', 1, 0, '{}')`, id, bindingID, roundID, seq)
}

// insertRecord is the root the CASCADE cases hang rows from: binding_event and
// round_file both reference it and both cascade from it, so a removal of the
// record takes them on the peer too.
func insertRecord(id, origin string) string {
	return fmt.Sprintf(`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json,
		created_at, updated_at, origin)
		VALUES ('%s', 'o', 'n', 'open', 1, '/z', '{}', 't', 't', '%s')`, id, origin)
}

// insertEvent writes one binding_event of a record, keyed by the record and its
// own sequence.
func insertRecordEvent(recordID string, seq int) string {
	return fmt.Sprintf(`INSERT INTO binding_event (record_id, seq, ts, round, direction, kind,
		confirmed, entry_json)
		VALUES ('%s', %d, 't', 1, 'in', 'note', 1, '{}')`, recordID, seq)
}

// insertRoundFile writes one round_file of a record. The body is a BLOB, so a
// case that carries one across also carries the codec column beside it and
// proves the compressed bytes travel as stored rather than decompressed.
func insertRoundFile(recordID, name string, round int) string {
	return fmt.Sprintf(`INSERT INTO round_file (record_id, name, round, body, bytes, sha256, mtime, sealed_at)
		VALUES ('%s', '%s', %d, X'0001', 1, 's', 't', 't')`, recordID, name, round)
}

// A drain closes over a parent its own window cannot carry. A child whose
// snapshot points at a parent written after the window's entries would otherwise
// travel alone, and the importer applying it with the foreign keys on would
// refuse the batch for a parent it was never sent -- a refusal that repeats for
// as long as the log holds the batch, so that origin would never import again.
//
// The window is one entry: it takes the child's update, whose snapshot already
// points at the later parent, while the parent's own entry lies past the window.
// The drain pulls the parent in with its snapshot state, so the batch applies
// whole and the parent is present on the peer afterwards.
func TestDrainCarriesAPendingParentPastTheWindow(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, theirPath, insertBinding("P1", "m1"), insertRound("C", "P1"))

	log := NewMemTransport("m1")
	importFrom(t, peer, log, theirs, &recording{MemTransport: log})
	if have := sharedRows(t, peer); have != `binding ["P1"]|round ["C"]` {
		t.Fatalf("the peer holds %q, want the parent and child the first import carried", have)
	}

	// A write to the child that names the later parent only in its resulting
	// state, then the parent itself, then the re-point. The window of one takes
	// the child's update first.
	seed(t, theirPath,
		`UPDATE round SET outcome = 'changed' WHERE id = 'C'`,
		insertBinding("P2", "m1"),
		`UPDATE round SET binding_id = 'P2' WHERE id = 'C'`,
	)

	r := &recording{MemTransport: log}
	e := exporter(theirs, r)
	e.batch = 1
	first, err := e.ExportBatch()
	if err != nil {
		t.Fatalf("export one window: %v", err)
	}
	if first.Appended != 2 {
		t.Fatalf("the batch carried %d entries (%s), want the child and the parent it points at",
			first.Appended, r.shape())
	}

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import the batch: %v", err)
	}
	if len(got.Dropped) != 0 {
		t.Fatalf("Dropped = %+v, want the batch applied: a missing parent must be pulled in, not refused", got.Dropped)
	}
	if binding := columnOf(t, peer, `SELECT binding_id FROM round WHERE id = 'C'`); binding != "P2" {
		t.Fatalf("the peer's round points at %q, want the parent the child was re-pointed to", binding)
	}
	if have := sharedRows(t, peer); !strings.Contains(have, `binding ["P2"]`) {
		t.Fatalf("the peer holds %q, want the pulled parent present", have)
	}
}

// A pulled chain of one table reaches the batch parents-first. A binding forked
// from another binding is a real self-reference, and the closure can pull both in
// one drain; the exporter's table sort orders tables but cannot order two rows of
// one table, so the closure itself has to hand the source before the fork or the
// peer refuses the batch for a parent it was never sent.
//
// P1 and its fork P2 are created after the cut, and the window of one takes the
// round's re-point, whose snapshot names P2. Both bindings lie past the window,
// so the closure pulls them; the peer holds neither, so the order is the whole
// difference between a batch it takes and one it drops.
func TestDrainOrdersPulledParentsParentsFirst(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, theirPath,
		insertBinding("P0", "m1"),
		insertRound("C", "P0"),
	)

	log := NewMemTransport("m1")
	importFrom(t, peer, log, theirs, &recording{MemTransport: log})

	// A write to the child, then the fork chain it is re-pointed into: P1, then
	// P2 forked from P1, then the re-point. The window of one takes the child's
	// update; P1 and P2 lie past it.
	seed(t, theirPath,
		`UPDATE round SET outcome = 'changed' WHERE id = 'C'`,
		insertForkedBinding("P1", "", "m1"),
		insertForkedBinding("P2", "P1", "m1"),
		`UPDATE round SET binding_id = 'P2' WHERE id = 'C'`,
	)

	r := &recording{MemTransport: log}
	e := exporter(theirs, r)
	e.batch = 1
	first, err := e.ExportBatch()
	if err != nil {
		t.Fatalf("export one window: %v", err)
	}
	if first.Appended != 3 {
		t.Fatalf("the batch carried %d entries (%s), want the child and both pulled parents",
			first.Appended, r.shape())
	}
	if shape := r.shape(); shape != `binding ["P1"] upsert|binding ["P2"] upsert|round ["C"] upsert` {
		t.Fatalf("appended = %q, want the fork's source before the fork", shape)
	}

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import the batch: %v", err)
	}
	if len(got.Dropped) != 0 {
		t.Fatalf("Dropped = %+v, want the batch applied: a same-table parent must precede its child", got.Dropped)
	}
	if have := sharedRows(t, peer); !strings.Contains(have, `binding ["P1"]`) || !strings.Contains(have, `binding ["P2"]`) {
		t.Fatalf("the peer holds %q, want both pulled parents present", have)
	}
}

// The pending-parent lookup compares the parent's key with the text the outbox
// stored. The outbox spells a key with SQLite's json_array, so the drain has to
// spell it the same way: Go's json.Marshal escapes <, >, & and the line
// separators, and a key holding one of them would name the row with different
// text, miss the outbox entry, and let the child travel alone.
func TestDrainPullsAParentWhoseKeyNeedsNoEscaping(t *testing.T) {
	t.Parallel()
	theirs, theirPath := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, theirPath,
		insertBinding("P<1&2>", "m1"),
		insertRound("C", "P<1&2>"),
	)

	log := NewMemTransport("m1")
	importFrom(t, peer, log, theirs, &recording{MemTransport: log})
	if have := sharedRows(t, peer); !strings.Contains(have, `binding ["P<1&2>"]`) {
		t.Fatalf("the peer holds %q, want the parent and child the first import carried", have)
	}

	// The child's update comes first, its resulting state names the later
	// parent, and the parent itself follows. The window of one takes the child's
	// update and leaves the parent to the closure.
	seed(t, theirPath,
		`UPDATE round SET outcome = 'changed' WHERE id = 'C'`,
		insertBinding("P<3&4>", "m1"),
		`UPDATE round SET binding_id = 'P<3&4>' WHERE id = 'C'`,
	)

	r := &recording{MemTransport: log}
	e := exporter(theirs, r)
	e.batch = 1
	first, err := e.ExportBatch()
	if err != nil {
		t.Fatalf("export one window: %v", err)
	}
	if first.Appended != 2 {
		t.Fatalf("the batch carried %d entries (%s), want the child and the parent it points at",
			first.Appended, r.shape())
	}

	got, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import the batch: %v", err)
	}
	if len(got.Dropped) != 0 {
		t.Fatalf("Dropped = %+v, want the batch applied: the parent must be pulled, not missed", got.Dropped)
	}
	if binding := columnOf(t, peer, `SELECT binding_id FROM round WHERE id = 'C'`); binding != "P<3&4>" {
		t.Fatalf("the peer's round points at %q, want the parent the child was re-pointed to", binding)
	}
	if have := sharedRows(t, peer); !strings.Contains(have, `binding ["P<3&4>"]`) {
		t.Fatalf("the peer holds %q, want the pulled parent present", have)
	}
}
