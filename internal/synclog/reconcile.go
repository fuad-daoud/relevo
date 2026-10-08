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
const defaultReconcileChunk = 256

// Reconciler compares what this installation's file holds against what the log
// already holds for it, and appends the difference.
//
// The outbox is a history of writes, so it answers only what this machine wrote
// since it last exported. It does not answer what the log already holds, which is
// the question a machine whose file was restored from a backup, or whose export
// was refused, or whose rows a migration rewrote, has to ask: is the log missing
// anything I own, and does it still hold anything I have dropped? Reconcile asks
// the log directly and emits the rows whose body hash differs from head, plus the
// deletes for head rows this file no longer holds.
//
// Every run recomputes from head rather than from a cursor of its own, so a run
// that stops half way is finished by the next one, and a row it already proposed
// is not proposed twice: the append it made is what head now carries.
type Reconciler struct {
	db        *db.DB
	transport LogTransport
	origin    string
	schema    int
	chunk     int
	now       func() time.Time
}

// NewReconciler returns the reconciler for one installation's file and the log
// it reads head from. The origin is read from the file because it decides which
// rows this machine may propose, and the schema version is what tells a reader
// whether it can understand the entries.
func NewReconciler(d *db.DB, t LogTransport) *Reconciler {
	have, _ := d.SchemaVersions()
	return &Reconciler{
		db:        d,
		transport: t,
		origin:    d.Origin(),
		schema:    have,
		chunk:     defaultReconcileChunk,
		now:       time.Now,
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
// A pass that finds nothing ends the run: the log already holds everything this
// file does, so there is nothing left to propose. A pass that appended something
// runs again, because the append moved head, and the rows it wrote are the rows
// the next pass compares against.
func (r *Reconciler) Reconcile() (ReconcileResult, error) {
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

// ReconcileBatch appends one batch of the differences it finds, up to the chunk
// size, as a single transport call. It appends nothing when the file and head
// already agree, and one batch when they do not.
func (r *Reconciler) ReconcileBatch() (ReconcileResult, error) {
	entries, err := r.differences(r.chunk)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("synclog: reconcile: %w", err)
	}
	if len(entries) == 0 {
		return ReconcileResult{}, nil
	}
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
// batch with the foreign keys on, so a parent has to be in place before the
// child naming it, and a child has to be gone before its parent is removed.
// Cutting the batch to the chunk size can only shorten that order, never
// rearrange it, so a chunked run emits the same sequence a single one would.
func (r *Reconciler) differences(limit int) ([]Entry, error) {
	head, err := r.transport.Head(r.origin)
	if err != nil {
		return nil, fmt.Errorf("read head: %w", err)
	}
	known := newHeadIndex(head)
	upserts, deletes, err := r.walk(known)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(upserts)+len(deletes))
	for _, group := range [][]Entry{upserts, deletes} {
		for _, e := range group {
			if len(out) == limit {
				return out, nil
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// walk is one pass over the shared tables in the order SharedTables lists them:
// the entries to propose for the rows this installation owns.
//
// The deletes come back reversed, because the walk's own order is the
// parents-first order an upsert needs and the opposite of what a delete needs: a
// row has to be removed before the parent it hangs off.
func (r *Reconciler) walk(known *headIndex) (upserts, deletes []Entry, err error) {
	for _, tbl := range SharedTablesInOrder() {
		rows, err := r.db.SharedOwnedRows(tbl, r.origin)
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", tbl, err)
		}
		if upserts, err = r.appendUpserts(upserts, tbl, rows, known); err != nil {
			return nil, nil, err
		}
		if deletes, err = r.appendDeletes(deletes, tbl, known); err != nil {
			return nil, nil, err
		}
	}
	slices.Reverse(deletes)
	return upserts, deletes, nil
}

// appendUpserts adds one entry per row of tbl whose body hash head does not
// carry. A row head names with the same hash is left alone, and a row head does
// not name at all is proposed whole: the log has no record of it, which is what a
// file restored from a backup looks like, since the rows come back with the file
// but not with the log's memory of them.
//
// The body is encoded once and both the comparison and the entry use it. The hash
// has to be taken over the bytes that travel, or the comparison would be against a
// digest of a body the log never saw.
func (r *Reconciler) appendUpserts(out []Entry, tbl string, rows []db.ExchangeRow, known *headIndex) ([]Entry, error) {
	for _, row := range rows {
		body, err := EncodeBody(row)
		if err != nil {
			return nil, err
		}
		hash, err := BodyHash(body)
		if err != nil {
			return nil, err
		}
		if known.holds(rowKey{table: tbl, pk: row.PK}, hash) {
			continue
		}
		entry, err := NewUpsert(r.origin, tbl, row.PK, r.schema, body, r.now())
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

// appendDeletes adds one entry per head row of tbl this file no longer holds. A
// row head names is one the log believes this origin wrote, so its absence here is
// a change the log has not been told about.
func (r *Reconciler) appendDeletes(out []Entry, tbl string, known *headIndex) ([]Entry, error) {
	for _, pk := range known.of(tbl) {
		_, found, err := r.db.ReadExchangeRow(tbl, pk)
		if err != nil {
			return nil, fmt.Errorf("read %s %s: %w", tbl, pk, err)
		}
		if found {
			continue
		}
		entry, err := NewDelete(r.origin, tbl, pk, r.schema, r.now())
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

// headIndex is one origin's head: every row it names, keyed for a hash
// comparison and grouped by table so a walk of one table reads only that table's
// rows.
type headIndex struct {
	hashes map[rowKey]string
	keys   map[string][]string
}

// newHeadIndex indexes the head rows the transport returned, keeping the order it
// returned them in. The order is kept rather than sorted because the walk reverses
// the deletes it gathers, and that reversal is the only ordering this needs.
func newHeadIndex(head []HeadRow) *headIndex {
	index := &headIndex{
		hashes: make(map[rowKey]string, len(head)),
		keys:   make(map[string][]string, len(head)),
	}
	for _, row := range head {
		key := rowKey{table: row.Table, pk: row.PK}
		index.hashes[key] = row.Hash
		index.keys[row.Table] = append(index.keys[row.Table], row.PK)
	}
	return index
}

// holds reports whether head names the row and carries this hash for it. A row
// head does not name is a row the log has never heard of, and one it names with
// another hash is a row whose contents changed.
func (h *headIndex) holds(key rowKey, hash string) bool {
	held, ok := h.hashes[key]
	return ok && held == hash
}

// of is the keys head holds for one table, in the order head listed them.
func (h *headIndex) of(tbl string) []string { return h.keys[tbl] }
