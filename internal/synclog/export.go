package synclog

import (
	"fmt"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// defaultBatchSize is how many outbox entries one drain reads. The batch is one
// transport append, so the size is the granularity a remote sees and the amount
// of work a refused append repeats; it is small enough that a failure loses
// little and large enough that a full outbox is not appended one row at a time.
const defaultBatchSize = 256

// Exporter turns this installation's own outbox into appends on the log.
//
// Every drain is one consistent snapshot: the entries and the state of the rows
// they name are read in one transaction, so a batch never reports a row as it
// was before a write that had already committed when the drain began. The
// entries the log holds afterwards are therefore an order an importer can apply
// with foreign keys on.
type Exporter struct {
	db        *db.DB
	transport LogTransport
	origin    string
	schema    int
	batch     int
	now       func() time.Time
}

// NewExporter returns the exporter for one installation's file and the log it
// appends to. The installation and the schema version are read from the file
// rather than passed in: the origin decides which rows are this machine's to
// export, and the version is what tells a reader which of its entries a newer
// binary wrote.
func NewExporter(d *db.DB, t LogTransport) *Exporter {
	have, _ := d.SchemaVersions()
	return &Exporter{
		db:        d,
		transport: t,
		origin:    d.Origin(),
		schema:    have,
		batch:     defaultBatchSize,
		now:       time.Now,
	}
}

// ExportResult is what one export moved: the outbox rows it cleared and the
// entries it handed the transport.
type ExportResult struct {
	// Drained is how many outbox entries the export removed.
	Drained int
	// Appended is how many entries the export handed to the transport.
	Appended int
}

// Export drains and appends until the outbox holds nothing.
//
// Each batch is appended before its entries are deleted, so a machine that stops
// part way leaves the rest of the outbox for the next run rather than dropping
// changes it never handed over.
func (e *Exporter) Export() (ExportResult, error) {
	var total ExportResult
	for {
		one, err := e.ExportBatch()
		total.Drained += one.Drained
		total.Appended += one.Appended
		if err != nil {
			return total, fmt.Errorf("synclog: export: %w", err)
		}
		if one.Drained == 0 {
			return total, nil
		}
	}
}

// ExportBatch drains one batch, appends it as a single transport call, and
// clears the outbox entries it drained once the transport has taken them.
//
// The delete comes last and is unconditional on what the batch held: entries
// this installation does not own were read and dropped, and keeping them would
// have them read again on every pass. What is never cleared is a batch the
// transport refused -- those entries are re-exported next pass, which is safe
// because import is an idempotent upsert.
func (e *Exporter) ExportBatch() (ExportResult, error) {
	drained, err := e.db.DrainOutbox(e.batch)
	if err != nil {
		return ExportResult{}, fmt.Errorf("synclog: export: drain: %w", err)
	}
	if len(drained) == 0 {
		return ExportResult{}, nil
	}

	entries, err := e.entries(drained)
	if err != nil {
		return ExportResult{}, fmt.Errorf("synclog: export: %w", err)
	}
	if len(entries) > 0 {
		if _, err := e.transport.Append(entries); err != nil {
			return ExportResult{}, fmt.Errorf("synclog: export: append: %w", err)
		}
	}

	// The cut is by sequence rather than by count, so a batch that held fewer
	// entries than it drained -- every one of them skipped -- still clears
	// exactly what it read and nothing that arrived while it was building.
	cut := drained[len(drained)-1].Seq
	if err := e.db.DeleteDrainedOutbox(cut); err != nil {
		return ExportResult{}, fmt.Errorf("synclog: export: clear: %w", err)
	}
	return ExportResult{Drained: len(drained), Appended: len(entries)}, nil
}

// entries is the batch the log is given: the drained rows this installation
// owns, one per row, ordered so an importer can apply it with foreign keys on.
func (e *Exporter) entries(drained []db.DrainedEntry) ([]Entry, error) {
	upserts, deletes := split(coalesce(owned(drained, e.origin)))
	sortParentsFirst(upserts)
	sortChildrenFirst(deletes)
	out := make([]Entry, 0, len(upserts)+len(deletes))
	for _, d := range upserts {
		body, err := EncodeBody(*d.Row)
		if err != nil {
			return nil, err
		}
		entry, err := NewUpsert(e.origin, d.Table, d.PK, e.schema, body, e.now())
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	for _, d := range deletes {
		entry, err := NewDelete(e.origin, d.Table, d.PK, e.schema, e.now())
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

// split divides the drained rows into the two kinds of entry the log carries: a
// row the file holds becomes an upsert with its body, and a row the file does
// not hold becomes a delete naming only its key.
//
// The outbox's own operation is not consulted. Whether a row is present is the
// question that decides the entry, because every entry of one drain read the
// same snapshot: an insert and the updates that follow it are one row, and an
// insert whose row is gone by the time the drain runs is a delete.
func split(rows []db.DrainedEntry) (upserts, deletes []db.DrainedEntry) {
	for _, d := range rows {
		if d.Row == nil {
			deletes = append(deletes, d)
			continue
		}
		upserts = append(upserts, d)
	}
	return upserts, deletes
}

// owned keeps the drained rows this installation is the owner of. A row whose
// owner is absent is one a cascade removed after its parent's entry was
// recorded: the parent's own delete carries the removal to every importer, so
// the orphan names nothing an importer could act on.
//
// A row another installation owns is here for the same reason it must not be
// appended: an entry for it would travel back to the machine that wrote it, and
// would be applied here a second time. Skipping it is what stops an imported row
// from echoing across the log.
func owned(drained []db.DrainedEntry, origin string) []db.DrainedEntry {
	var out []db.DrainedEntry
	for _, d := range drained {
		if !d.Origin.Valid || d.Origin.String != origin {
			continue
		}
		out = append(out, d)
	}
	return out
}

// rowKey names the row one drained entry is about, so repeats of the same row
// in one drain collapse to one.
type rowKey struct {
	table string
	pk    string
}

// coalesce folds the drained entries of one batch to one per row, keeping the
// position the row first appeared at.
//
// The position is the drain order the entries arrived in, and it is kept only so
// that rows of the same table keep their commit order; what decides the batch's
// order is the table sort below. The state is taken from the last entry for the
// row, which is the snapshot the drain read: every entry of one drain saw the
// same state, and taking the last makes the rule hold without depending on
// which of them a caller looks at.
func coalesce(drained []db.DrainedEntry) []db.DrainedEntry {
	var out []db.DrainedEntry
	at := make(map[rowKey]int, len(drained))
	for _, d := range drained {
		key := rowKey{table: d.Table, pk: d.PK}
		if i, seen := at[key]; seen {
			out[i].Row = d.Row
			continue
		}
		at[key] = len(out)
		out = append(out, d)
	}
	return out
}

// sortParentsFirst orders a batch's upserts so a row meets its parent before the
// children that reference it. SharedTables lists parents first, so the table's
// position in it is the order to write in; a row whose parent is not in the
// batch was exported by an earlier one.
//
// The sort is stable, so rows of one table keep the commit order the drain read
// them in rather than an arbitrary one.
func sortParentsFirst(rows []db.DrainedEntry) {
	sort.SliceStable(rows, func(i, j int) bool {
		return tableIndex(rows[i].Table) < tableIndex(rows[j].Table)
	})
}

// sortChildrenFirst orders a batch's deletes the other way round, so a row is
// removed before its parent. The parent rows of the 001 tables carry no cascade,
// so an importer that deleted a parent first would have the child's delete
// refused by the very foreign key the importer needs satisfied.
func sortChildrenFirst(rows []db.DrainedEntry) {
	sort.SliceStable(rows, func(i, j int) bool {
		return tableIndex(rows[i].Table) > tableIndex(rows[j].Table)
	})
}
