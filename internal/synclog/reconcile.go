package synclog

import (
	"fmt"
	"slices"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// defaultReconcileChunk is how many entries one reconcile batch carries. A batch
// is one transport append, so the size is the granularity a remote sees and the
// work a refused append repeats; it matches the exporter's drain. It also bounds
// the local read: a page asks for no more than the chunk still needs, so a
// filling chunk ends on its page's last row and no row is read twice.
const defaultReconcileChunk = 256

// The two phases of a chunked run. Upserts run parents-first so a parent is in
// place before the row; deletes run children-first so a child is gone before its parent.
const (
	phaseUpserts = iota
	phaseDeletes
)

// Reconciler compares what this installation's file holds against what the log
// already holds for it, and appends the difference.
// The outbox answers only what this machine wrote since it last exported; reconcile
// asks the log directly, which is what a backup-restored, refused or migrated file
// needs: is the log missing anything owned, and does it still hold anything
// dropped? It emits rows whose body hash differs from head plus deletes for head
// rows this file does not hold.
// A run walks the file once in key-ordered pages, keeping its place between chunks,
// and reads head once for the delete phase to name unfound rows without asking again.
type Reconciler struct {
	keyed
	db        *db.DB
	transport LogTransport
	origin    string
	schema    int
	chunk     int
	now       func() time.Time

	// mover and staging are where the values too large to travel inline go.
	// Both are nil or empty on a transport that moves no bodies, which keeps
	// every value inline for a log with no bucket behind it.
	mover   BlobMover
	staging string

	// pending is what the current chunk's rows need uploaded before its entries
	// may be appended. It is emptied by the batch that consumed it, so a never-
	// appended chunk leaves nothing behind.
	pending []pendingBlob
	// left is the bindings rows this installation handed over or lost to a
	// takeover: the log still names them, but the delete phase never proposes
	// removing rows another machine now writes.
	left map[db.RowRef]bool
	// blobBudget is the body bytes past which a chunk ends early: below what an
	// importer takes in one batch by the most one more row can add.
	blobBudget int64

	// present is the bodies the bucket holds under this origin, or nil when the
	// run does not check the bucket. With it, a headed row still gets its bodies
	// uploaded when the bucket lacks them: a reader joining from zero needs them there.
	present map[string]bool

	// readPage reads one rowid-ordered page of a table's owned rows inside the
	// chunk's read transaction. It is a field so a test can count the pages and
	// rows a run reads.
	readPage func(tx *db.Tx, tbl string, after int64, limit int) ([]db.ExchangeRow, error)

	// Run state: head as the run began, where the walk stopped, and the head
	// rows the walk has found locally.
	head   *headIndex
	cursor reconcileCursor
	seen   map[rowKey]bool
}

// reconcileCursor is where a chunked run resumes: phase, table, and stop point,
// so each row is read once per run however many chunks it takes.
// The two phases stop in different currencies: the upsert walk carries the last
// rowid read, the delete walk the last head key emitted.
type reconcileCursor struct {
	phase int
	table int
	after string
	rowID int64
}

// NewReconciler returns the reconciler for one installation's file and the log
// it reads head from. The origin decides which rows this machine may propose,
// and the schema version tells a reader whether it can understand the entries,
// so both are read from the file.
func NewReconciler(d *db.DB, t LogTransport) *Reconciler {
	have, _ := d.SchemaVersions()
	origin := d.Origin()
	mover, staging := blobWiring(t)
	return &Reconciler{
		db:         d,
		transport:  t,
		origin:     origin,
		schema:     have,
		chunk:      defaultReconcileChunk,
		blobBudget: MaxBatchBlobBytes - 2*MaxBlobBytes,
		now:        time.Now,
		mover:      mover,
		staging:    staging,
		readPage: func(tx *db.Tx, tbl string, after int64, limit int) ([]db.ExchangeRow, error) {
			return tx.SharedOwnedRowPage(tbl, origin, after, limit)
		},
		seen: make(map[rowKey]bool),
	}
}

// ReconcileResult is what one reconcile moved: the appends it made and the
// entries within them.
type ReconcileResult struct {
	Batches int
	// Repaired is how many bodies it uploaded for rows head already carries.
	Repaired int
	// Upserts is how many rows it proposed as changed or missing.
	Upserts int
	// Deletes is how many head rows it proposed as gone from this file.
	Deletes int
}

// More reports whether one batch did anything, which is whether the walk may
// have more to do.
func (r ReconcileResult) More() bool { return r.Batches > 0 || r.Repaired > 0 }

// Reconcile compares the whole file against head and appends every difference,
// in as many batches as the differences need. One run is one walk: cursor and
// found rows carry chunk to chunk, so a table is read once and the run ends with
// the walk. A second call on the same reconciler starts a new walk.
func (r *Reconciler) Reconcile() (ReconcileResult, error) {
	r.reset()
	var total ReconcileResult
	for {
		one, err := r.ReconcileBatch()
		if err != nil {
			return total, fmt.Errorf("synclog: reconcile: %w", err)
		}
		total.Batches += one.Batches
		total.Upserts += one.Upserts
		total.Deletes += one.Deletes
		total.Repaired += one.Repaired
		if !one.More() {
			return total, nil
		}
	}
}

// reset starts a run from the first row and an unread head.
func (r *Reconciler) reset() {
	r.head = nil
	r.cursor = reconcileCursor{}
	r.seen = make(map[rowKey]bool)
	r.pending = nil
}

// RepairBodies makes the run upload every body of this origin's rows that the
// bucket does not hold, present being the keys it does hold.
func (r *Reconciler) RepairBodies(present map[string]bool) {
	r.present = present
}

// ReconcileBatch appends one batch of the differences it finds, up to the chunk
// size, as a single transport call. A repair-only chunk uploads and appends
// nothing; the walk is finished only when a chunk finds neither.
func (r *Reconciler) ReconcileBatch() (ReconcileResult, error) {
	r.pending = nil
	entries, err := r.differences(r.chunk)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("synclog: reconcile: %w", err)
	}
	if len(entries) == 0 {
		return r.repairOnly()
	}
	// The bodies go first, as the exporter's do: an entry naming a missing object
	// would latch the importer over a body a later attempt could still have stored.
	if err := r.guard(r.transport, r.origin); err != nil {
		return ReconcileResult{}, fmt.Errorf("synclog: reconcile: %w", err)
	}
	if err := uploadPending(r.mover, r.staging, r.pending); err != nil {
		return ReconcileResult{}, fmt.Errorf("synclog: reconcile: upload: %w", err)
	}
	repaired := r.repairs()
	r.pending = nil
	r.sign(entries)
	written, err := r.appendTo(r.transport, r.origin, entries)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("synclog: reconcile: append: %w", err)
	}
	noteOwnHead(r.db.LocalOrSelf(), written)
	result := ReconcileResult{Batches: 1, Upserts: len(entries), Repaired: repaired}
	for _, e := range entries {
		if e.Op == OpDelete {
			result.Upserts--
			result.Deletes++
		}
	}
	return result, nil
}

// repairOnly uploads the bodies a difference-free chunk found missing.
func (r *Reconciler) repairOnly() (ReconcileResult, error) {
	if len(r.pending) == 0 {
		return ReconcileResult{}, nil
	}
	if err := uploadPending(r.mover, r.staging, r.pending); err != nil {
		return ReconcileResult{}, fmt.Errorf("synclog: reconcile: repair: %w", err)
	}
	repaired := r.repairs()
	r.pending = nil
	return ReconcileResult{Repaired: repaired}, nil
}

// repairs counts the pending bodies that were repairs and records them as present
// so a later chunk does not upload them again.
func (r *Reconciler) repairs() int {
	n := 0
	for _, b := range r.pending {
		if b.repair {
			n++
		}
		if r.present != nil {
			r.present[b.Key] = true
		}
	}
	return n
}

// differences is the batch the log is handed: this file's rows whose body hash
// head does not carry, upserts parents first, then the head rows this file no
// longer holds, deletes children first.
// The order is the exporter's: an importer applies with foreign keys on, so a
// parent precedes its child and a child precedes its removed parent. Cutting to
// the chunk size only shortens that order. Every chunk reads in one transaction,
// so the compared rows are one moment's state.
func (r *Reconciler) differences(limit int) ([]Entry, error) {
	if err := r.ensureHead(); err != nil {
		return nil, err
	}
	var out []Entry
	err := r.db.Tx(func(tx *db.Tx) error {
		upserts, err := r.nextUpserts(tx, limit)
		if err != nil {
			return err
		}
		out = upserts
		if len(out) == limit {
			return nil
		}
		deletes, err := r.nextDeletes(tx, limit-len(out))
		if err != nil {
			return err
		}
		out = append(out, deletes...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read owned rows: %w", err)
	}
	return out, nil
}

// nextUpserts fills out with upserts until it holds limit or the walk leaves the
// upsert phase. It pages each table in rowid order and stops where the chunk
// filled for the next chunk to resume after.
func (r *Reconciler) nextUpserts(tx *db.Tx, limit int) ([]Entry, error) {
	var out []Entry
	tables := SharedTablesInOrder()
	for r.cursor.phase == phaseUpserts && len(out) < limit {
		if r.cursor.table >= len(tables) {
			r.cursor = reconcileCursor{phase: phaseDeletes}
			return out, nil
		}
		tbl := tables[r.cursor.table]
		// Reading no more than the chunk still needs keeps the cursor from leaving
		// fetched rows behind: the chunk can only fill on such a page's last row.
		remaining := limit - len(out)
		page, err := r.readPage(tx, tbl, r.cursor.rowID, remaining)
		if err != nil {
			return nil, err
		}
		for _, row := range page {
			if err := r.inspectUpsert(&out, tbl, row); err != nil {
				return nil, err
			}
			r.cursor.rowID = row.RowID
			// The chunk also ends before its bodies could pass one importer's batch: the
			// next row adds at most two values.
			if len(out) == limit || pendingBytes(r.pending) > r.blobBudget {
				return out, nil
			}
		}
		if len(page) < remaining {
			r.cursor.table++
			r.cursor.after = ""
			r.cursor.rowID = 0
		}
	}
	return out, nil
}

// inspectUpsert compares one local row against head. A same-hash row is left alone
// but recorded as still here; every other row is proposed as an upsert. The body
// is encoded once for both comparison and entry, so the hash covers the bytes
// that travel.
// The comparison is over the ref form the entry travels in: hashing local bytes
// would compare a full value against a ref and propose the same row every run.
// Building the ref form costs one sha256 per over-threshold value on every owned
// row, the price of not re-exporting the file each time.
func (r *Reconciler) inspectUpsert(out *[]Entry, tbl string, row db.ExchangeRow) error {
	key := rowKey{table: tbl, pk: row.PK}
	if historyRowAged(tbl, row, r.historyCutoffMS()) {
		// An aged-out row is not missing from the log. It is recorded as seen so
		// the delete phase reads found rather than not held, and proposes nothing.
		r.seen[key] = true
		return nil
	}
	if r.head.names(key) {
		r.seen[key] = true
	}
	// A row is only rewritten when there is somewhere to write it: without a
	// mover the ref would name an object nobody put.
	moved, blobs := row, []pendingBlob(nil)
	if r.mover != nil {
		moved, blobs = refColumns(r.origin, tbl, row)
	}
	body, err := EncodeBody(moved)
	if err != nil {
		return err
	}
	hash, err := BodyHash(body)
	if err != nil {
		return err
	}
	if r.head.holds(key, hash) {
		r.queueRepairs(tbl, row.PK, blobs)
		return nil
	}
	// Only a proposed row moves its values: a headed row's bytes were stored with
	// its entry, so uploading again would duplicate a digest the store holds.
	if err := checkBlobSizes(tbl, row.PK, blobs); err != nil {
		return err
	}
	r.pending = append(r.pending, blobs...)
	entry, err := NewUpsert(r.origin, tbl, row.PK, r.schema, body, r.now())
	if err != nil {
		return err
	}
	afterOrigin, afterSeq, found, err := r.db.WriterAfter(tbl, row.PK)
	if err != nil {
		return err
	}
	if found {
		entry = entry.WithAfter(&Ref{Origin: afterOrigin, Seq: afterSeq})
	}
	*out = append(*out, entry)
	return nil
}

// queueRepairs queues the bodies of a headed row that the bucket lacks. A body
// queued once in this run is not queued again.
func (r *Reconciler) queueRepairs(tbl, pk string, blobs []pendingBlob) {
	if r.present == nil {
		return
	}
	for _, b := range blobs {
		if r.present[b.Key] || checkBlobSizes(tbl, pk, []pendingBlob{b}) != nil {
			continue
		}
		b.repair = true
		r.present[b.Key] = true
		r.pending = append(r.pending, b)
	}
}

// nextDeletes fills out with deletes until it holds limit or head runs out: the
// unfound head rows, children-first, one table's keys descending, so the importer
// order holds across chunks.
// A head row is read back before dropping: the walk found every owned row head
// names, so this separates a row held under another installation, not this
// walk's to remove, from one this file does not hold.
func (r *Reconciler) nextDeletes(tx *db.Tx, limit int) ([]Entry, error) {
	var out []Entry
	tables := SharedTablesInOrder()
	for r.cursor.phase == phaseDeletes && len(out) < limit {
		if r.cursor.table >= len(tables) {
			return out, nil
		}
		// Head is walked the other way round -- reversed table order, descending
		// key order -- so a child is removed before its parent, the order an
		// importer needs.
		tbl := tables[len(tables)-1-r.cursor.table]
		keys := r.head.unseen(tbl, r.cursor.after, r.seen)
		if len(keys) == 0 {
			r.cursor.table++
			r.cursor.after = ""
			continue
		}
		for _, pk := range keys {
			entry, err := r.deleteFor(tx, tbl, pk)
			if err != nil {
				return nil, err
			}
			r.cursor.after = pk
			if entry == nil {
				continue
			}
			out = append(out, *entry)
			if len(out) == limit {
				return out, nil
			}
		}
		r.cursor.table++
		r.cursor.after = ""
	}
	return out, nil
}

// deleteFor is the delete for one unfound head row, or nil when the file holds
// it, including under another installation. Reading the key back also surfaces a
// head key this schema cannot parse, which no delete could name.
// An append-only head row never becomes a delete: those rows are removed only by
// age and the age delete is never recorded, so a missing one aged out here or
// where it was written.
func (r *Reconciler) deleteFor(tx *db.Tx, tbl, pk string) (*Entry, error) {
	if db.AppendOnly(tbl) || r.left[db.RowRef{Table: tbl, PK: pk}] {
		return nil, nil
	}
	_, found, err := tx.ReadExchangeRow(tbl, pk)
	if err != nil {
		return nil, fmt.Errorf("read %s %s: %w", tbl, pk, err)
	}
	if found {
		return nil, nil
	}
	entry, err := NewDelete(r.origin, tbl, pk, r.schema, r.now())
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

// ensureHead reads head once for the run and holds it for the delete phase to
// compare against, keeping a chunked run to one head read.
func (r *Reconciler) ensureHead() error {
	if r.head != nil {
		return nil
	}
	head, err := r.transport.Head(r.origin)
	if err != nil {
		return fmt.Errorf("read head: %w", err)
	}
	r.head = newHeadIndex(head)
	left, err := r.db.LeftRows()
	if err != nil {
		return fmt.Errorf("read left rows: %w", err)
	}
	r.left = left
	return nil
}

// headIndex is one origin's head: every row it names, keyed for hash comparison
// and grouped by table in key order for single-table walks and key resumes.
type headIndex struct {
	hashes map[rowKey]string
	keys   map[string][]string
}

// newHeadIndex indexes the head rows the transport returned, sorting each table's
// keys ascending for the upsert walk's page order and the delete walk's reverse.
func newHeadIndex(head []HeadRow) *headIndex {
	index := &headIndex{
		hashes: make(map[rowKey]string, len(head)),
		keys:   make(map[string][]string),
	}
	for _, row := range head {
		key := rowKey{table: row.Table, pk: row.PK}
		index.hashes[key] = row.Hash
		index.keys[row.Table] = append(index.keys[row.Table], row.PK)
	}
	for _, keys := range index.keys {
		slices.Sort(keys)
	}
	return index
}

// names reports whether head lists the row at all, whatever hash it carries.
func (h *headIndex) names(key rowKey) bool {
	_, ok := h.hashes[key]
	return ok
}

// holds reports whether head names the row and carries this hash for it: unnamed
// is never heard of, another hash is changed contents.
func (h *headIndex) holds(key rowKey, hash string) bool {
	held, ok := h.hashes[key]
	return ok && held == hash
}

// unseen returns tbl's unfound head keys before the cursor key, descending: the
// order that resumes from a cursor yet still emits a child before its parent,
// since a fork carries the higher key.
func (h *headIndex) unseen(tbl, after string, seen map[rowKey]bool) []string {
	var out []string
	keys := h.keys[tbl]
	for i := len(keys) - 1; i >= 0; i-- {
		pk := keys[i]
		if after != "" && pk >= after {
			continue
		}
		if seen[rowKey{table: tbl, pk: pk}] {
			continue
		}
		out = append(out, pk)
	}
	return out
}
