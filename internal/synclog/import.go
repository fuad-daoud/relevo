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
//
// An entry a newer binary wrote is the one failure the importer cannot retry
// past, because retrying it changes nothing: until this machine is upgraded the
// entry is unreadable however often it is offered. So the origin holds there
// and reports, and every other origin keeps moving.
type Importer struct {
	db        *db.DB
	transport LogTransport
	known     int
}

// NewImporter returns the importer for one installation's file and the log it
// reads from. The file is this machine's own: an import writes into it with
// this machine's schema deciding what a body means, and a column this build
// does not carry is dropped rather than written.
//
// The schema version the importer compares entries against is this binary's
// own, read from the file rather than passed in: it is what decides whether an
// entry's body means anything here, and a binary that knows more schemas than
// the file holds is the one that has to apply it.
func NewImporter(d *db.DB, t LogTransport) *Importer {
	_, known := d.SchemaVersions()
	return &Importer{db: d, transport: t, known: known}
}

// ImportResult is what one import moved: the batches it applied and the entries
// within them.
type ImportResult struct {
	// Batches is how many whole batches were applied.
	Batches int
	// Applied is how many entries were written.
	Applied int
	// Held are the origins this machine could not follow past an entry their
	// writer wrote with a newer schema. They are a report rather than a
	// failure: the entries before the hold applied and the mark rests there,
	// so an upgraded machine picks the rest up where this one left off.
	Held []Hold
}

// Hold is one origin stopped at an entry this machine's schema cannot read. The
// origin's mark rests at the last entry applied before it, which is also the
// point its later entries are re-read from.
type Hold struct {
	// Origin is the installation whose entries stopped.
	Origin string
	// Label is the name that installation's own file carries, so the report
	// names the machine rather than the id rows are stamped with.
	Label string
	// Seq is the sequence the origin's mark rests at: the entry before the one
	// this machine cannot read, so nothing applied is ever read past twice.
	Seq int
	// Schema is the writer's schema version at the entry that stopped it.
	Schema int
}

// String is the report a hold reads as to whoever has to act on it. It names the
// installation rather than its sequence number, because the fix is to upgrade
// this machine and the sequence number says nothing about which writer's schema
// is the one waiting.
func (h Hold) String() string {
	return fmt.Sprintf("relevo on this machine is older than %s, which writes schema %d: upgrade this machine to follow it (mark rests at %d)",
		h.Label, h.Schema, h.Seq)
}

// Import pulls every origin's entries this file has not applied and writes them.
//
// The pull is per origin and per mark, so a machine applies each origin's log in
// its own sequence order and never a sequence it has already passed. An origin
// with no mark is read from the start of its log, which is what a machine that
// has never imported from it needs.
//
// An origin whose entries outrun this machine's schema holds where it stopped,
// and the origins beside it keep going: one writer being ahead of this build is
// not a reason to stop reading the others.
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
	// An origin that held once holds for the rest of the run. Its later entries
	// were written by the same newer binary and are just as unreadable, so
	// reading them would apply part of a batch the exporter wrote as one.
	held := make(map[string]bool)
	for _, batch := range batches(entries) {
		if held[batch[0].Origin] {
			continue
		}
		applied, hold, err := i.applyBatch(batch)
		if err != nil {
			return total, fmt.Errorf("synclog: import: %w", err)
		}
		total.Applied += applied
		if hold == nil {
			total.Batches++
			continue
		}
		// A held batch was not applied, so it is neither counted nor marked:
		// counting it would report entries that are not here. The directory is
		// read only once something has held, because a run that applied
		// everything needs no name for anything.
		names, err := i.labels()
		if err != nil {
			return total, fmt.Errorf("synclog: import: %w", err)
		}
		hold.Label = names[hold.Origin]
		total.Held = append(total.Held, *hold)
		held[hold.Origin] = true
	}
	return total, nil
}

// labels is every installation the directory carries, keyed by id. The hold a
// reader is given names an installation rather than a sequence, and the
// directory is what turns one into the other; an origin the directory has not
// heard of keeps its own id, which is still enough to act on.
func (i *Importer) labels() (map[string]string, error) {
	list, err := i.db.InstallationList()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list))
	for _, inst := range list {
		out[inst.ID] = inst.Label
	}
	return out, nil
}

// applyBatch writes one batch in one transaction and advances the mark that
// covers it. The mark is the last sequence the batch carries, which is also the
// batch's own number, so a run that stops here resumes at the next batch rather
// than re-reading this one.
//
// A batch carrying an entry from a newer binary is not applied at all, and no
// mark moves for it. Applying the entries ahead of that one would rest the mark
// inside the batch, and the transport hands back whole batches past the mark --
// so every later run would skip the rest of the batch and the entries this
// machine cannot read would never arrive, however far the reader advanced. The
// whole batch waits instead, and the mark stays on the batch before it, which
// is also where an upgraded machine resumes from.
func (i *Importer) applyBatch(batch []Entry) (int, *Hold, error) {
	if len(batch) == 0 {
		return 0, nil, nil
	}
	if e, held := i.held(batch); held {
		return 0, &Hold{Origin: e.Origin, Seq: batch[0].Seq - 1, Schema: e.SchemaVersion}, nil
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
		return 0, nil, err
	}
	return applied, nil, nil
}

// held is the first entry in a batch a newer binary wrote, and whether the batch
// has one. The whole batch is searched rather than its first entry: an entry
// carries its writer's version, and the version can differ within one batch
// because the writer upgraded between two of its own appends.
func (i *Importer) held(batch []Entry) (Entry, bool) {
	for _, e := range batch {
		if e.SchemaVersion > i.known {
			return e, true
		}
	}
	return Entry{}, false
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
