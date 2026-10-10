package synclog

import (
	"fmt"
	"slices"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// defaultReconcileChunk is how many entries one reconcile batch carries. A
// reconcile batch is one transport append, so the size is the granularity a
// remote sees and the work a refused append repeats. It matches the exporter's
// drain, so the two paths put the same amount in one write.
//
// It is also the bound a local read uses: a page asks for no more rows than the
// chunk still needs, so a chunk that fills does so on its page's last row and no
// fetched row is left to be read twice. One number serves both.
const defaultReconcileChunk = 256

// The two phases of a chunked run. Upserts run first, parents-first, because an
// importer needs a row's parent in place before the row; deletes run second,
// children-first, so a child is gone before the parent it hangs off.
const (
	phaseUpserts = iota
	phaseDeletes
)

// Reconciler compares what this installation's file holds against what the log
// already holds for it, and appends the difference.
//
// The outbox is a history of writes, so it answers only what this machine wrote
// since it last exported. It does not answer what the log already holds, which is
// the question a machine whose file was restored from a backup, or whose export
// was refused, or whose rows a migration rewrote, has to ask: is the log missing
// anything I own, and does it still hold anything I have dropped? Reconcile asks
// the log directly and emits the rows whose body hash differs from head, plus the
// deletes for head rows this file does not hold.
//
// A run walks the file once, in key-ordered pages, and keeps where it stopped
// between chunks, so a table larger than a chunk is read once rather than
// restarted. Head is read once for the run and held, which is what lets the
// delete phase name the rows the walk did not find without asking the file again.
type Reconciler struct {
	db        *db.DB
	transport LogTransport
	origin    string
	schema    int
	chunk     int
	now       func() time.Time

	// mover and staging are where the values too large to travel inline go.
	// Both are nil or empty on a transport that moves no bodies, which is what
	// keeps every value inline for a log with no bucket behind it.
	mover   BlobMover
	staging string

	// pending is what the current chunk's rows will need uploaded before its
	// entries may be appended. It is emptied by the batch that consumed it, so a
	// chunk which is never appended -- one whose entries all turn out to be
	// already held -- leaves nothing behind.
	pending []pendingBlob
	// blobBudget is the body bytes past which a chunk ends early: below what an
	// importer takes in one batch by the most one more row can add.
	blobBudget int64

	// readPage reads one rowid-ordered page of a table's owned rows inside the
	// chunk's read transaction. It is a field so a test can count the pages a run
	// reads and the rows each one returned.
	readPage func(tx *db.Tx, tbl string, after int64, limit int) ([]db.ExchangeRow, error)

	// Run state: head as it stood when the run began, where the walk stopped, and
	// the head rows the walk has found locally so far.
	head   *headIndex
	cursor reconcileCursor
	seen   map[rowKey]bool
}

// reconcileCursor is where a chunked run resumes: which phase it is in, which
// table it was walking, and where it stopped there. The next page continues
// after that point, so each row is read once per run however many chunks it
// takes.
//
// The two phases stop in different currencies. The upsert walk pages the file,
// so it carries the last rowid it read; the delete walk pages head's keys, so it
// carries the last key it emitted.
type reconcileCursor struct {
	phase int
	table int
	after string
	rowID int64
}

// NewReconciler returns the reconciler for one installation's file and the log
// it reads head from. The origin is read from the file because it decides which
// rows this machine may propose, and the schema version is what tells a reader
// whether it can understand the entries.
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
	// Batches is how many appends the log took.
	Batches int
	// Upserts is how many rows it proposed as changed or missing.
	Upserts int
	// Deletes is how many head rows it proposed as gone from this file.
	Deletes int
}

// Reconcile compares the whole file against head and appends every difference,
// in as many batches as the differences need.
//
// A run is one walk of the file: the cursor and the rows the walk has found
// carry from chunk to chunk, so a table is read once and the run ends when the
// walk does rather than when an append changed head. A second call on the same
// reconciler starts a new walk.
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
		if one.Batches == 0 {
			return total, nil
		}
	}
}

// reset starts a run from the first row and an unread head, so a run does not
// resume a walk an earlier call already finished.
func (r *Reconciler) reset() {
	r.head = nil
	r.cursor = reconcileCursor{}
	r.seen = make(map[rowKey]bool)
	r.pending = nil
}

// ReconcileBatch appends one batch of the differences it finds, up to the chunk
// size, as a single transport call. It appends nothing when the walk is finished,
// and one batch when it is not.
func (r *Reconciler) ReconcileBatch() (ReconcileResult, error) {
	r.pending = nil
	entries, err := r.differences(r.chunk)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("synclog: reconcile: %w", err)
	}
	if len(entries) == 0 {
		return ReconcileResult{}, nil
	}
	// The bodies go first, for the reason the exporter's do: an entry naming an
	// object the store does not hold would latch the importer over a body a
	// later attempt could still have stored. The chunk is not appended, and the
	// next one re-reads it from head, so a failed upload costs nothing but the
	// sha256s the walk already paid.
	if err := uploadPending(r.mover, r.staging, r.pending); err != nil {
		return ReconcileResult{}, fmt.Errorf("synclog: reconcile: upload: %w", err)
	}
	r.pending = nil
	if _, err := r.transport.Append(entries); err != nil {
		return ReconcileResult{}, fmt.Errorf("synclog: reconcile: append: %w", err)
	}
	result := ReconcileResult{Batches: 1, Upserts: len(entries)}
	for _, e := range entries {
		if e.Op == OpDelete {
			result.Upserts--
			result.Deletes++
		}
	}
	return result, nil
}

// differences is the batch the log is handed: this file's rows whose body hash
// head does not carry, upserts parents first, then the head rows this file no
// longer holds, deletes children first.
//
// The order is the exporter's, and for the same reason: an importer applies a
// batch with the foreign keys on, so a parent has to be in place before the child
// naming it, and a child has to be gone before its parent is removed. Cutting the
// batch to the chunk size can only shorten that order, never rearrange it, so a
// chunked run emits the same sequence a single one would.
//
// Every read one chunk makes runs in one transaction, so the rows it compares are
// the state at one moment and a row cannot arrive before a parent it references.
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

// nextUpserts fills out with upserts until it holds limit of them or the walk
// leaves the upsert phase. It pages each table through SharedOwnedRowPage in
// rowid order and stops where the chunk filled, so the next chunk resumes after
// it.
func (r *Reconciler) nextUpserts(tx *db.Tx, limit int) ([]Entry, error) {
	var out []Entry
	tables := SharedTablesInOrder()
	for r.cursor.phase == phaseUpserts && len(out) < limit {
		if r.cursor.table >= len(tables) {
			r.cursor = reconcileCursor{phase: phaseDeletes}
			return out, nil
		}
		tbl := tables[r.cursor.table]
		// Reading no more than the chunk still needs is what keeps the cursor
		// from leaving fetched rows behind: the chunk can only fill on the last
		// row of such a page, so no row is fetched and then read again.
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
			// The chunk also ends before its bodies could pass what an importer
			// takes in one batch: the next row adds at most two values.
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

// inspectUpsert compares one local row against head. A row head names with the
// same hash is left alone but recorded as still here; every other row is proposed
// as an upsert. The body is encoded once and both the comparison and the entry use
// it, so the hash is taken over the bytes that travel rather than a digest of a
// body the log never saw.
//
// The comparison is over the ref form, which is the form the entry travels in:
// the worker hashes the body as it was appended, so hashing the local bytes would
// compare a digest of the full value against a digest of a ref and propose the
// same row on every run forever. Building the ref form costs one sha256 per value
// over the threshold, on every owned row of the walk, whatever the comparison
// then decides -- which is the price of not re-exporting the file each time.
func (r *Reconciler) inspectUpsert(out *[]Entry, tbl string, row db.ExchangeRow) error {
	key := rowKey{table: tbl, pk: row.PK}
	if r.head.names(key) {
		r.seen[key] = true
	}
	// A row is only rewritten when there is somewhere to write it, for the
	// reason the exporter's is: without a mover the ref would name an object
	// nobody put.
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
		return nil
	}
	// Only a row that is actually proposed moves its values. A row head already
	// carries was stored when its entry was appended, so uploading again would
	// put the same bytes under a digest the store already holds.
	if err := checkBlobSizes(tbl, row.PK, blobs); err != nil {
		return err
	}
	r.pending = append(r.pending, blobs...)
	entry, err := NewUpsert(r.origin, tbl, row.PK, r.schema, body, r.now())
	if err != nil {
		return err
	}
	*out = append(*out, entry)
	return nil
}

// nextDeletes fills out with deletes until it holds limit of them or head runs
// out: the head rows the walk did not find, walked children-first, with the keys
// of one table taken in descending order, so the order an importer needs holds
// across chunks.
//
// A head row is read back from the file before it is dropped. The walk already
// found every owned row head names, so this is the check that separates a head row
// the file holds under another installation, which is not this walk's to remove,
// from one this file does not hold.
func (r *Reconciler) nextDeletes(tx *db.Tx, limit int) ([]Entry, error) {
	var out []Entry
	tables := SharedTablesInOrder()
	for r.cursor.phase == phaseDeletes && len(out) < limit {
		if r.cursor.table >= len(tables) {
			return out, nil
		}
		// Head is walked the other way round: a child is removed before the
		// parent it hangs off, which is the order an importer needs. That holds
		// across tables by the reversed table order and inside one table by the
		// descending key order, so a fork goes before the binding it forked
		// from.
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

// deleteFor is the delete for one head row the walk did not find, or nil when the
// file does hold the row: a head row the file holds under another installation is
// not this walk's to remove. Reading the key back also surfaces a head key this
// schema cannot parse, which no delete could name correctly.
func (r *Reconciler) deleteFor(tx *db.Tx, tbl, pk string) (*Entry, error) {
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

// ensureHead reads head once for the run and holds it. Holding it is what lets
// the delete phase compare against the state the walk compared against, and it
// keeps a chunked run to one head read rather than one per chunk.
func (r *Reconciler) ensureHead() error {
	if r.head != nil {
		return nil
	}
	head, err := r.transport.Head(r.origin)
	if err != nil {
		return fmt.Errorf("read head: %w", err)
	}
	r.head = newHeadIndex(head)
	return nil
}

// headIndex is one origin's head: every row it names, keyed for a hash
// comparison and grouped by table in key order so a walk of one table reads only
// that table's rows and a delete phase resumes from a key.
type headIndex struct {
	hashes map[rowKey]string
	keys   map[string][]string
}

// newHeadIndex indexes the head rows the transport returned, sorting each table's
// keys ascending so the upsert walk matches the local page's own key order and
// the delete walk has one order to take in reverse.
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

// holds reports whether head names the row and carries this hash for it. A row
// head does not name is a row the log has never heard of, and one it names with
// another hash is a row whose contents changed.
func (h *headIndex) holds(key rowKey, hash string) bool {
	held, ok := h.hashes[key]
	return ok && held == hash
}

// unseen returns tbl's head keys the walk has not found locally, before the
// cursor key, in descending key order. The order is what lets the delete phase
// resume from a cursor and still emit a child before its parent within one table:
// a fork is created after its source and carries the higher key, so descending
// proposes the fork first.
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
