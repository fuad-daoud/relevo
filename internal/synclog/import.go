package synclog

import (
	"fmt"
	"sort"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Importer applies another installation's entries to this machine's file.
//
// It works in whole batches, and one batch is one transaction: the entries go
// in as the exporter ordered them -- every parent before the rows that name it,
// every row removed before its parent -- and the origin's mark moves in the
// same transaction. A batch that fails half way leaves neither the rows it had
// written nor the mark behind, so the next run sees the same batch again from
// the same mark rather than a mark that has moved over rows that were never
// applied.
type Importer struct {
	db        *db.DB
	transport LogTransport
}

// NewImporter returns the importer for one installation's file and the log it
// reads from. The file is this machine's own: an import writes into it with
// this machine's schema deciding what a body means, and a column this build
// does not carry is dropped rather than written.
func NewImporter(d *db.DB, t LogTransport) *Importer {
	return &Importer{db: d, transport: t}
}

// ImportResult is what one import moved: the batches it applied and the entries
// within them.
type ImportResult struct {
	// Batches is how many whole batches were applied.
	Batches int
	// Applied is how many entries were written.
	Applied int
}

// Import pulls every origin's entries this file has not applied and writes them.
//
// The pull is per origin and per mark, so a machine applies each origin's log in
// its own sequence order and never a sequence it has already passed. An origin
// with no mark is read from the start of its log, which is what a machine that
// has never imported from it needs.
func (i *Importer) Import() (ImportResult, error) {
	marks, err := i.db.ImportMarks()
	if err != nil {
		return ImportResult{}, fmt.Errorf("synclog: import: %w", err)
	}
	entries, err := i.transport.Pull(marks)
	if err != nil {
		return ImportResult{}, fmt.Errorf("synclog: import: pull: %w", err)
	}
	var total ImportResult
	for _, batch := range batches(entries) {
		applied, err := i.applyBatch(batch)
		if err != nil {
			return total, fmt.Errorf("synclog: import: %w", err)
		}
		total.Batches++
		total.Applied += applied
	}
	return total, nil
}

// applyBatch writes one batch in one transaction and advances the mark that
// covers it. The mark is the last sequence the batch carries, which is also the
// batch's own number, so a run that stops here resumes at the next batch rather
// than re-reading this one.
func (i *Importer) applyBatch(batch []Entry) (int, error) {
	if len(batch) == 0 {
		return 0, nil
	}
	applied := 0
	err := i.db.Tx(func(tx *db.Tx) error {
		for _, e := range batch {
			if err := applyEntry(tx, e); err != nil {
				return err
			}
			applied++
		}
		return tx.SetImportMark(batch[0].Origin, batch[len(batch)-1].Seq)
	})
	if err != nil {
		return 0, err
	}
	return applied, nil
}

// applyEntry writes one entry. The table name and every column name come from
// this machine's own schema and its shared-table list, never from the entry's
// body, so a body can carry values without carrying names to write them under.
func applyEntry(tx *db.Tx, e Entry) error {
	if _, ok := SharedTable(e.Table); !ok {
		return fmt.Errorf("synclog: import %s %s: %q is not a shared table: %w", e.Table, e.PK, e.Table, db.ErrInvalid)
	}
	switch e.Op {
	case OpUpsert:
		values, err := DecodeBody(e.Body)
		if err != nil {
			return fmt.Errorf("synclog: import %s %s: %w", e.Table, e.PK, err)
		}
		if err := tx.ExchangeUpsert(e.Table, values); err != nil {
			return fmt.Errorf("synclog: import %s %s: %w", e.Table, e.PK, err)
		}
		return nil
	case OpDelete:
		if err := tx.ExchangeDelete(e.Table, e.PK); err != nil {
			return fmt.Errorf("synclog: import %s %s: %w", e.Table, e.PK, err)
		}
		return nil
	default:
		return fmt.Errorf("synclog: import %s %s: op %q: %w", e.Table, e.PK, e.Op, ErrInvalid)
	}
}

// batchKey is one batch of one origin: the entries sharing a batch number, which
// is the sequence of the first of them. A batch is keyed by origin too because
// the number belongs to the origin that issued it and two origins' sequences
// overlap by design.
type batchKey struct {
	origin string
	batch  int
}

// batches divides pulled entries into the whole batches they arrived in, in the
// order to apply them. Entries are grouped by the batch they share rather than
// split on the mark, because a batch that begins at or before the mark is
// re-read whole and applying half of it would write an order the exporter never
// produced.
func batches(entries []Entry) [][]Entry {
	order := make([]batchKey, 0, len(entries))
	grouped := make(map[batchKey][]Entry, len(entries))
	for _, e := range entries {
		key := batchKey{origin: e.Origin, batch: e.Batch}
		if _, seen := grouped[key]; !seen {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], e)
	}
	out := make([][]Entry, 0, len(order))
	for _, key := range order {
		entries := grouped[key]
		// Within a batch the exporter's own order is the order to apply: it put
		// every parent before the rows naming it and every removal before its
		// parent, and the foreign keys on are what make that order necessary.
		sort.SliceStable(entries, func(a, b int) bool { return entries[a].Seq < entries[b].Seq })
		out = append(out, entries)
	}
	return out
}
