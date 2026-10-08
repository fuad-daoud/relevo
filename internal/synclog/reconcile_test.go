package synclog

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The reconciler's contract is the batch it hands the log when the log and the
// file disagree: every row this machine owns whose body the log does not already
// carry, parents first, and every row the log carries for this machine that the
// file does not hold, children first. A run that finds nothing appends
// nothing.
//
// The cases run against a real file and the same fake the exporter and importer
// use, so a head a reconcile reads is a head the transport really maintains.

func TestReconcileEmitsOnlyDifferences(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path,
		insertBinding("01A", "m1"),
		insertRound("02A", "01A"),
		insertBinding("01B", "m1"),
	)

	// An empty log against a populated file proposes every owned row. The
	// parents-first order is what an importer needs: the round names a binding
	// the same batch carries, and the foreign keys are on.
	first := &recording{MemTransport: NewMemTransport("m1")}
	got, err := reconciler(d, first).Reconcile()
	if err != nil {
		t.Fatalf("reconcile against an empty log: %v", err)
	}
	if got.Batches != 1 || got.Upserts != 3 || got.Deletes != 0 {
		t.Fatalf("reconcile = %+v, want one batch of three upserts", got)
	}
	if shape := first.shape(); shape != `binding ["01A"] upsert|binding ["01B"] upsert|round ["02A"] upsert` {
		t.Fatalf("appended = %q, want both bindings ahead of the round", shape)
	}

	// A second run finds nothing: every row's body hash is what the first run
	// wrote into head. A reconcile that re-proposed unchanged rows would append
	// the same batch forever, and every importer would pay for it.
	second := &recording{MemTransport: first.MemTransport}
	again, err := reconciler(d, second).Reconcile()
	if err != nil {
		t.Fatalf("reconcile the unchanged file: %v", err)
	}
	if again.Batches != 0 || again.Upserts != 0 || again.Deletes != 0 {
		t.Fatalf("reconcile = %+v, want nothing appended for a file head already carries", again)
	}
	if shape := second.shape(); shape != "" {
		t.Fatalf("appended %q on the second run, want nothing", shape)
	}
}

// A row that changed and a row that appeared are the two differences a run
// after a converged one can find, and each is proposed once: the changed row as
// an upsert and the vanished one as a delete, children before parents so the
// importer's foreign keys hold.
func TestReconcileEmitsOneDeleteAndOneUpsertAfterTheChange(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path,
		insertBinding("01A", "m1"),
		insertBinding("01B", "m1"),
		insertBinding("01C", "m1"),
		insertRound("02A", "01C"),
	)

	log := NewMemTransport("m1")
	settled := &recording{MemTransport: log}
	if _, err := reconciler(d, settled).Reconcile(); err != nil {
		t.Fatalf("reconcile the settled file: %v", err)
	}

	seed(t, path,
		`UPDATE binding SET cwd = '/moved' WHERE id = '01B'`,
		`DELETE FROM binding WHERE id = '01A'`,
	)

	changed := &recording{MemTransport: log}
	got, err := reconciler(d, changed).Reconcile()
	if err != nil {
		t.Fatalf("reconcile after the change: %v", err)
	}
	if got.Batches != 1 || got.Upserts != 1 || got.Deletes != 1 {
		t.Fatalf("reconcile = %+v, want one upsert and one delete", got)
	}
	// The round and the binding it names are both untouched, so neither is a
	// difference: only the changed row and the removed one are, and the upsert
	// leads the batch because a parent is written before a removal.
	if shape := changed.shape(); shape != `binding ["01B"] upsert|binding ["01A"] delete` {
		t.Fatalf("appended = %q, want the changed binding and the removed one", shape)
	}
	body, err := DecodeBody(changed.appended()[0].Body)
	if err != nil {
		t.Fatalf("read the appended body: %v", err)
	}
	if body["cwd"] != "/moved" {
		t.Fatalf("appended cwd = %v, want the value the row holds now", body["cwd"])
	}
}

// The outbox and head answer different questions, so reconcile is not redundant
// with an export: a file whose writes were exported before the log knew about
// them proposes them anyway. Here the outbox is emptied first, so anything
// reconcile emits came from head's side of the comparison rather than from a
// pending write.
func TestReconcileProposesRowsTheOutboxNoLongerHolds(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m1"))
	if _, err := exporter(d, &recording{MemTransport: NewMemTransport("m1")}).Export(); err != nil {
		t.Fatalf("export through a log of its own: %v", err)
	}
	if out := outboxRows(t, d); out != "" {
		t.Fatalf("outbox after the export = %q, want it empty", out)
	}

	// The log this machine reaches holds nothing of its own, so the row is a row
	// it owns that the log has never heard of. The outbox cannot say so: it is
	// empty, and an export would propose nothing.
	fresh := &recording{MemTransport: NewMemTransport("m1")}
	got, err := reconciler(d, fresh).Reconcile()
	if err != nil {
		t.Fatalf("reconcile with an empty outbox: %v", err)
	}
	if got.Upserts != 1 {
		t.Fatalf("reconcile = %+v, want the one owned row the outbox does not hold", got)
	}
	if shape := fresh.shape(); shape != `binding ["01A"] upsert` {
		t.Fatalf("appended = %q, want the binding the outbox had cleared", shape)
	}
}

// A run stops at the chunk size and is finished by the next one, so a file with
// more rows than one batch carries emits them all without any batch being split
// mid-way. The order across the batches is the same as a single batch's, which is
// what lets an importer apply them in sequence.
func TestReconcileChunksBeyondOneBatch(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	for _, id := range []string{"01A", "01B", "01C", "01D", "01E"} {
		seed(t, path, insertBinding(id, "m1"))
	}

	r := &recording{MemTransport: NewMemTransport("m1")}
	e := reconciler(d, r)
	e.chunk = 2
	got, err := e.Reconcile()
	if err != nil {
		t.Fatalf("reconcile in chunks: %v", err)
	}
	if got.Batches != 3 || got.Upserts != 5 {
		t.Fatalf("reconcile = %+v, want five upserts across three appends", got)
	}
	if sizes := batchSizes(r); sizes != "2,2,1" {
		t.Fatalf("batch sizes = %q, want 2,2,1", sizes)
	}
	// Every entry of one append carries that append's number, so a reader can
	// apply a batch whole.
	for _, batch := range r.batches {
		for _, e := range batch {
			if e.Batch != batch[0].Batch {
				t.Fatalf("a batch mixes batch numbers %d and %d, want one per append",
					batch[0].Batch, e.Batch)
			}
		}
	}
}

// batchSizes is each recorded batch's length, joined, so a case can say how the
// differences were divided rather than only how many there were.
func batchSizes(r *recording) string {
	var out []string
	for _, batch := range r.batches {
		out = append(out, itoa(len(batch)))
	}
	return strings.Join(out, ",")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// A row another installation owns is not this machine's to propose, so a
// reconcile of a file holding one proposes nothing for it. The same shape the
// exporter skips is skipped here, which is what stops an imported row from
// echoing back across the log through the other path.
func TestReconcileSkipsRowsAnotherInstallationOwns(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path,
		insertBinding("01A", "m2"),
		insertBinding("01B", "m1"),
	)

	r := &recording{MemTransport: NewMemTransport("m1")}
	got, err := reconciler(d, r).Reconcile()
	if err != nil {
		t.Fatalf("reconcile a file holding a foreign row: %v", err)
	}
	if got.Upserts != 1 {
		t.Fatalf("reconcile = %+v, want only this machine's own binding", got)
	}
	if shape := r.shape(); shape != `binding ["01B"] upsert` {
		t.Fatalf("appended = %q, want the other machine's binding left alone", shape)
	}
}

// A head row this file does not hold is a delete, and the deletes of one batch
// run children first: a child removed ahead of the parent it hangs off is the
// order the foreign keys of the original tables require, since a parent removed
// first would have the child's delete refused.
func TestReconcileOrdersDeletesChildrenFirst(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path,
		insertBinding("01A", "m1"),
		insertRound("02A", "01A"),
	)

	log := NewMemTransport("m1")
	if _, err := reconciler(d, &recording{MemTransport: log}).Reconcile(); err != nil {
		t.Fatalf("reconcile the settled file: %v", err)
	}

	// Both rows go, and the round's removal has to reach the log before the
	// binding's: an importer removing the binding first is refused by the very
	// foreign key the round still holds it under.
	seed(t, path,
		`DELETE FROM round WHERE id = '02A'`,
		`DELETE FROM binding WHERE id = '01A'`,
	)

	r := &recording{MemTransport: log}
	got, err := reconciler(d, r).Reconcile()
	if err != nil {
		t.Fatalf("reconcile after both removals: %v", err)
	}
	if got.Deletes != 2 || got.Upserts != 0 {
		t.Fatalf("reconcile = %+v, want two deletes and nothing else", got)
	}
	if shape := r.shape(); shape != `round ["02A"] delete|binding ["01A"] delete` {
		t.Fatalf("appended = %q, want the round removed before the binding", shape)
	}
}

// A fork pair is two rows of one table, and the delete phase has to remove the
// fork before its source even there. The fork is created after the source and
// carries the higher key, so descending within the table proposes it first; an
// importer removing the source first is refused by the fork that still hangs off
// it, drops the whole batch and leaves both rows behind.
func TestReconcileOrdersSameTableDeletesChildFirst(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	peer, _ := peerFile(t, "m2")
	seed(t, path,
		insertForkedBinding("01A", "", "m1"),
		insertForkedBinding("01B", "01A", "m1"),
	)

	log := NewMemTransport("m1")
	if _, err := reconciler(d, &recording{MemTransport: log}).Reconcile(); err != nil {
		t.Fatalf("reconcile the settled fork: %v", err)
	}
	settled, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import the settled fork: %v", err)
	}
	if len(settled.Dropped) != 0 {
		t.Fatalf("Dropped = %+v, want the peer to take both bindings", settled.Dropped)
	}

	seed(t, path,
		`DELETE FROM binding WHERE id = '01B'`,
		`DELETE FROM binding WHERE id = '01A'`,
	)

	r := &recording{MemTransport: log}
	got, err := reconciler(d, r).Reconcile()
	if err != nil {
		t.Fatalf("reconcile after both removals: %v", err)
	}
	if got.Deletes != 2 || got.Upserts != 0 {
		t.Fatalf("reconcile = %+v, want two deletes and nothing else", got)
	}
	if shape := r.shape(); shape != `binding ["01B"] delete|binding ["01A"] delete` {
		t.Fatalf("appended = %q, want the fork removed before the binding it forked from", shape)
	}

	back, err := NewImporter(peer, log.OnLog("m2")).Import()
	if err != nil {
		t.Fatalf("import the deletes: %v", err)
	}
	if len(back.Dropped) != 0 {
		t.Fatalf("Dropped = %+v, want the deletes applied rather than refused", back.Dropped)
	}
	if have := sharedRows(t, peer); have != "" {
		t.Fatalf("the peer holds %q, want both bindings removed", have)
	}
}

// A run reads the file in bounded pages rather than in one query per table: no
// read hands back more rows than the page size, however many rows the table
// holds. This is what keeps a file whose history dwarfs a chunk from being held
// in memory whole.
func TestReconcileReadsBoundedPages(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	for _, id := range []string{"01A", "01B", "01C", "01D", "01E"} {
		seed(t, path, insertBinding(id, "m1"))
	}

	r := &recording{MemTransport: NewMemTransport("m1")}
	e := reconciler(d, r)
	e.chunk = 2
	inner := e.readPage
	reads := 0
	e.readPage = func(tx *db.Tx, tbl, after string, limit int) ([]db.ExchangeRow, error) {
		if limit > e.chunk {
			t.Errorf("a read asked for %d rows, want at most the page size %d", limit, e.chunk)
		}
		rows, err := inner(tx, tbl, after, limit)
		if err != nil {
			return nil, err
		}
		reads++
		if len(rows) > e.chunk {
			t.Errorf("a read returned %d rows, want no more than the page size %d", len(rows), e.chunk)
		}
		return rows, nil
	}
	got, err := e.Reconcile()
	if err != nil {
		t.Fatalf("reconcile in bounded pages: %v", err)
	}
	if got.Upserts != 5 {
		t.Fatalf("reconcile = %+v, want the five bindings proposed", got)
	}
	if reads < 3 {
		t.Fatalf("the run read %d pages, want the five rows spread over more than two", reads)
	}
}

// A multi-chunk run reads each row once: the cursor carries from chunk to chunk,
// so the walk continues where it stopped instead of restarting the table. A run
// that restarted would read the rows of every earlier chunk again, which is
// exactly the whole-history re-read this walk is shaped to avoid.
//
// The run starts from a settled log and changes a few rows, so the pages it walks
// carry rows that need no proposal beside rows that do. A page is bounded by what
// the chunk still needs, so a chunk that fills mid-page fills on the page's last
// row and no fetched row is left to be read again.
func TestReconcileInspectsEachRowOnce(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	ids := []string{"01A", "01B", "01C", "01D", "01E"}
	for _, id := range ids {
		seed(t, path, insertBinding(id, "m1"))
	}

	log := NewMemTransport("m1")
	if _, err := reconciler(d, &recording{MemTransport: log}).Reconcile(); err != nil {
		t.Fatalf("reconcile the settled file: %v", err)
	}
	seed(t, path,
		`UPDATE binding SET cwd = '/a' WHERE id = '01A'`,
		`UPDATE binding SET cwd = '/c' WHERE id = '01C'`,
		`UPDATE binding SET cwd = '/e' WHERE id = '01E'`,
	)

	r := &recording{MemTransport: log}
	e := reconciler(d, r)
	e.chunk = 2
	inner := e.readPage
	read := map[string]int{}
	e.readPage = func(tx *db.Tx, tbl, after string, limit int) ([]db.ExchangeRow, error) {
		rows, err := inner(tx, tbl, after, limit)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			read[row.Table+" "+row.PK]++
		}
		return rows, nil
	}
	got, err := e.Reconcile()
	if err != nil {
		t.Fatalf("reconcile across chunks: %v", err)
	}
	if got.Upserts != 3 || got.Batches < 2 {
		t.Fatalf("reconcile = %+v, want the three changed rows over more than one chunk", got)
	}
	for _, id := range ids {
		if n := read[`binding ["`+id+`"]`]; n != 1 {
			t.Errorf("the run read binding [%q] %d times, want once", id, n)
		}
	}
}

// A head row the file still holds under another installation is not this walk's
// to remove: the row is present, so no delete for it travels back to the machine
// that owns it. The walk did not find it, because it resolves to the other
// installation, so the presence check is what keeps it from being dropped.
func TestReconcileLeavesAHeadRowTheFileStillHolds(t *testing.T) {
	t.Parallel()
	d, path := exporterFile(t, "m1")
	seed(t, path, insertBinding("01A", "m2"))

	log := NewMemTransport("m1")
	appendBatch(t, log, upsert(t, "01A", knownVersion(t, d)))

	r := &recording{MemTransport: log}
	got, err := reconciler(d, r).Reconcile()
	if err != nil {
		t.Fatalf("reconcile a head row the file holds under another installation: %v", err)
	}
	if got.Upserts != 0 || got.Deletes != 0 {
		t.Fatalf("reconcile = %+v, want nothing proposed for a row the other installation owns", got)
	}
	if shape := r.shape(); shape != "" {
		t.Fatalf("appended = %q, want the foreign installation's row left alone", shape)
	}
}

// reconciler returns a reconciler over d writing to the given log, with the clock
// fixed so an entry's timestamp is a value rather than the moment the test
// happened to run.
func reconciler(d *db.DB, t LogTransport) *Reconciler {
	e := NewReconciler(d, t)
	e.now = syncClock
	return e
}
