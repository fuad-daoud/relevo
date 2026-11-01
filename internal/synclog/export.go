package synclog

import (
	"fmt"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// DefaultBatchSize is how many outbox entries one drain reads. The batch is one
// transport append, so the size is the granularity a remote sees and the work a
// refused append repeats: small enough to lose little, large enough to avoid
// one-row appends.
const DefaultBatchSize = 256

// Exporter turns this installation's own outbox into appends on the log.
// Every drain is one consistent snapshot in one transaction, so the handed entries
// are an order an importer can apply with foreign keys on.
type Exporter struct {
	keyed
	db        *db.DB
	transport LogTransport
	origin    string
	schema    int
	batch     int
	now       func() time.Time

	// mover and staging are where the values too large to travel inline go. Both
	// are nil or empty on a transport that moves no bodies, keeping every value
	// inline for a log with no bucket behind it.
	mover   BlobMover
	staging string
	// blobCap is the most body bytes one appended batch may name.
	blobCap int64
}

// NewExporter returns the exporter for one installation's file and the log it
// appends to. Origin and schema version are read from the file: the origin decides
// which rows are this machine's to export, and the version tells a reader which
// entries a newer binary wrote.
func NewExporter(d *db.DB, t LogTransport) *Exporter {
	have, _ := d.SchemaVersions()
	mover, staging := blobWiring(t)
	return &Exporter{
		db:        d,
		transport: t,
		origin:    d.Origin(),
		schema:    have,
		batch:     DefaultBatchSize,
		now:       time.Now,
		mover:     mover,
		staging:   staging,
		blobCap:   MaxBatchBlobBytes,
	}
}

// ExportResult is what one export moved: the outbox rows it cleared and the
// entries it handed the transport.
type ExportResult struct {
	Drained  int
	Appended int
}

// Export drains and appends until the outbox holds nothing.
// Each batch is appended before its entries are deleted, so a stopped machine
// leaves the rest for the next run rather than dropping unhanded changes.
func (e *Exporter) Export() (ExportResult, error) {
	var total ExportResult
	ctl, err := e.exportCtl()
	total.Appended += ctl
	if err != nil {
		return total, fmt.Errorf("synclog: export: %w", err)
	}
	for {
		one, err := e.ExportBatch()
		total.Drained += one.Drained
		total.Appended += one.Appended
		if err != nil {
			return total, fmt.Errorf("synclog: export: %w", err)
		}
		if one.Drained == 0 {
			docs, err := e.exportDocs()
			total.Appended += docs
			return total, err
		}
	}
}

// ExportBatch drains one batch, appends it as a single transport call, and
// clears the drained outbox entries once taken.
// The delete comes last and is unconditional on what the batch held: unowned
// entries were read and dropped, and keeping them would re-read them every pass.
// A refused batch is never cleared -- its entries re-export next pass, safe
// because import is an idempotent upsert.
func (e *Exporter) ExportBatch() (ExportResult, error) {
	drained, entries, pending, err := e.build()
	if err != nil || len(drained) == 0 {
		return ExportResult{}, err
	}
	if len(entries) > 0 {
		if err := e.guard(e.transport, e.origin); err != nil {
			return ExportResult{}, err
		}
		// The bodies go first: an entry naming a missing object would latch the
		// importer over a body a later attempt could still have stored. A failed
		// upload returns like a failed append, so the batch is offered again next pass.
		if err := uploadPending(e.mover, e.staging, pending); err != nil {
			return ExportResult{}, fmt.Errorf("synclog: export: upload: %w", err)
		}
		e.sign(entries)
		written, err := e.appendTo(e.transport, e.origin, entries)
		if err != nil {
			return ExportResult{}, fmt.Errorf("synclog: export: append: %w", err)
		}
		noteOwnHead(e.db.LocalOrSelf(), written)
	}

	// The cut is by sequence rather than by count, so a batch that skipped entries
	// still clears exactly what it read and nothing that arrived while building.
	cut := drained[len(drained)-1].Seq
	if err := e.db.DeleteDrainedOutbox(cut); err != nil {
		return ExportResult{}, fmt.Errorf("synclog: export: clear: %w", err)
	}
	return ExportResult{Drained: len(drained), Appended: len(entries)}, nil
}

// build drains the outbox into the batch the log is given. An over-budget batch
// drains again at half the size, so no appended batch is one an importer refuses.
func (e *Exporter) build() ([]db.DrainedEntry, []Entry, []pendingBlob, error) {
	for n := e.batch; ; n /= 2 {
		drained, err := e.db.DrainOutbox(n)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("synclog: export: drain: %w", err)
		}
		if len(drained) == 0 {
			return nil, nil, nil, nil
		}
		entries, pending, err := e.entries(drained)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("synclog: export: %w", err)
		}
		if n <= 1 || pendingBytes(pending) <= e.blobCap {
			return drained, entries, pending, nil
		}
	}
}

// entries is the batch the log is given: the drained rows this installation
// owns, one per row, ordered so an importer can apply it with foreign keys on.
// Deletes precede upserts, children-first and parents-first respectively, so a
// re-used key is freed before the new row is written. Oversized values come back
// beside the batch, to be uploaded before it is appended. A delete carries no body.
func (e *Exporter) entries(drained []db.DrainedEntry) ([]Entry, []pendingBlob, error) {
	ownedRows, err := e.owned(drained)
	if err != nil {
		return nil, nil, err
	}
	upserts, deletes := split(coalesce(ownedRows))

	upserts = orderUpserts(upserts, e.origin)
	sortChildrenFirst(deletes)

	afterCache := make(map[rowKey]*Ref)
	getAfter := func(tbl, pk string) (*Ref, error) {
		k := rowKey{table: tbl, pk: pk}
		if ref, ok := afterCache[k]; ok {
			return ref, nil
		}
		afterOrigin, afterSeq, found, err := e.db.WriterAfter(tbl, pk)
		if err != nil {
			return nil, err
		}
		var ref *Ref
		if found {
			ref = &Ref{Origin: afterOrigin, Seq: afterSeq}
		}
		afterCache[k] = ref
		return ref, nil
	}

	delEntries, err := e.deleteEntries(deletes, getAfter)
	if err != nil {
		return nil, nil, err
	}
	out := make([]Entry, 0, len(delEntries)+len(upserts))
	out = append(out, delEntries...)
	var pending []pendingBlob
	for _, d := range upserts {
		// A row is only rewritten when there is somewhere to write it: without a
		// mover the ref would name an object nobody put.
		row, blobs := *d.Row, []pendingBlob(nil)
		if e.mover != nil {
			row, blobs = refColumns(e.origin, d.Table, *d.Row)
		}
		if err := checkBlobSizes(d.Table, d.PK, blobs); err != nil {
			return nil, nil, err
		}
		pending = append(pending, blobs...)
		body, err := EncodeBody(row)
		if err != nil {
			return nil, nil, err
		}
		entry, err := NewUpsert(e.origin, d.Table, d.PK, e.schema, body, e.now())
		if err != nil {
			return nil, nil, err
		}
		ref, err := getAfter(d.Table, d.PK)
		if err != nil {
			return nil, nil, err
		}
		if ref != nil {
			entry = entry.WithAfter(ref)
		}
		out = append(out, entry)
	}
	return out, pending, nil
}

func (e *Exporter) deleteEntries(deletes []db.DrainedEntry, getAfter func(string, string) (*Ref, error)) ([]Entry, error) {
	out := make([]Entry, 0, len(deletes))
	for _, d := range deletes {
		entry, err := NewDelete(e.origin, d.Table, d.PK, e.schema, e.now())
		if err != nil {
			return nil, err
		}
		ref, err := getAfter(d.Table, d.PK)
		if err != nil {
			return nil, err
		}
		if ref != nil {
			entry = entry.WithAfter(ref)
		}
		out = append(out, entry)
	}
	return out, nil
}

// orderUpserts orders regular upserts first (parents before children) and
// handover roots last (parents before children).
func orderUpserts(upserts []db.DrainedEntry, origin string) []db.DrainedEntry {
	var regularUpserts, handoverRoots []db.DrainedEntry
	for _, u := range upserts {
		if isHandoverRoot(u, origin) {
			handoverRoots = append(handoverRoots, u)
		} else {
			regularUpserts = append(regularUpserts, u)
		}
	}
	sortParentsFirst(regularUpserts)
	sortParentsFirst(handoverRoots)
	return append(regularUpserts, handoverRoots...)
}

// isHandoverRoot reports whether d is a root row whose body hands over ownership
// to another writer.
func isHandoverRoot(d db.DrainedEntry, origin string) bool {
	if (d.Table != "binding_record" && d.Table != "binding") || d.Row == nil {
		return false
	}
	val, ok := d.Row.Value("writer")
	if !ok || val == nil {
		return false
	}
	writer, _ := val.(string)
	return writer != "" && writer != origin
}

// split divides the drained rows into the log's two entry kinds: a held row
// becomes an upsert with its body, a missing one a delete naming only its key.
// The outbox's own operation is not consulted: every entry of one drain read the
// same snapshot, so presence decides.
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

// owned keeps the drained rows this installation owns. An ownerless row is one a
// cascade removed after its parent's entry was recorded: the parent's delete
// carries the removal, so the orphan names nothing actionable.
// A foreign-owned row must not be appended: its entry would travel back to its
// writer and apply here twice; skipping stops an imported row echoing.
// A row whose root carries a takeover fence against this installation moves to
// sync_fenced_change and drops (C1).
func (e *Exporter) owned(drained []db.DrainedEntry) ([]db.DrainedEntry, error) {
	var out []db.DrainedEntry
	fencedCache := make(map[string]bool)
	for _, d := range drained {
		if !d.Origin.Valid || d.Origin.String != e.origin {
			continue
		}
		rootID, found, err := e.db.ResolveRoot(d.Table, d.PK)
		if err != nil {
			return nil, err
		}
		if found && rootID != "" {
			fenced, ok := fencedCache[rootID]
			if !ok {
				f, err := e.db.IsTakeoverFenced(rootID)
				if err != nil {
					return nil, err
				}
				fenced = f
				fencedCache[rootID] = fenced
			}
			if fenced {
				if err := e.db.RecordFencedChange(rootID, d.Table, d.PK, e.now()); err != nil {
					return nil, err
				}
				continue
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// rowKey names the row one drained entry is about, so repeats in one drain
// collapse to one.
type rowKey struct {
	table string
	pk    string
}

// coalesce folds one batch's drained entries to one per row, keeping the first
// position so same-table rows keep commit order; the table sort below decides the
// batch order. The state is the last entry for the row, the drain's snapshot.
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

// sortParentsFirst orders a batch's upserts parents-first: SharedTables lists
// parents first, so table position is write order; a parent absent from the batch
// came in an earlier one. The sort is stable, keeping the drain's commit order.
func sortParentsFirst(rows []db.DrainedEntry) {
	sort.SliceStable(rows, func(i, j int) bool {
		return tableIndex(rows[i].Table) < tableIndex(rows[j].Table)
	})
}

// sortChildrenFirst orders a batch's deletes children-first. The 001 parents carry
// no cascade, so deleting a parent first would have the child's delete refused
// by the very foreign key the importer needs satisfied.
func sortChildrenFirst(rows []db.DrainedEntry) {
	sort.SliceStable(rows, func(i, j int) bool {
		return tableIndex(rows[i].Table) > tableIndex(rows[j].Table)
	})
}
