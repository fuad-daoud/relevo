package synclog

import (
	"errors"
	"fmt"
	"sort"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Importer applies another installation's entries to this machine's file.
//
// It works in whole batches, and one batch is one transaction: the entries go
// in as the exporter ordered them -- every parent before the rows that name it,
// every row removed before its parent -- and the origin's mark moves in the same
// transaction. A batch that fails half way leaves neither the rows it had
// written nor the mark behind, so the next run sees the same batch again from
// the same mark rather than a mark that has moved over rows that were never
// applied.
//
// A batch this machine cannot apply at all is dropped rather than failed on: an
// entry a newer binary wrote, a body it cannot read, a row that breaks a
// constraint. Re-reading such a batch refuses it again for as long as the log
// holds it, so one entry nobody can act on would otherwise stop every origin on
// this machine, forever. A mark moves past a dropped batch so it is not offered
// again; a hard failure -- a log that will not answer, a busy file -- moves no
// mark at all, because nothing was applied.
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
	// Dropped are the batches this machine would not apply at all and has moved
	// past. Each is a body it cannot act on, and each is reported rather than
	// silently passed over so whoever has to act on it can tell which entry it
	// was.
	Dropped []Drop
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

// Drop is one batch this machine refused whole and passed over. It is a report
// and not a failure: the entries behind it are not offered again, so the machine
// keeps importing every other origin's batches while this one waits.
type Drop struct {
	// Origin is the installation the refused batch came from.
	Origin string
	// Seq is the last sequence the batch carried, and the mark it left behind.
	Seq int
	// Reason is the refusal itself, naming the entry and the table it named.
	Reason string
}

// String is the report a drop reads as to whoever has to act on it. It names the
// entry rather than the origin alone, because the origin is usually fine and it
// is one entry inside its log that this build cannot take.
func (d Drop) String() string {
	return fmt.Sprintf("dropped a batch of %d entries from %s: %s", d.Seq, d.Origin, d.Reason)
}

// errNotOwner reports an entry whose claimed origin is not the owner of the row
// it names. It is an ErrInvalid refusal under a second name, so the code that
// drops a batch it cannot apply can tell this one apart from a body it merely
// does not understand: a batch dropped for an unreadable body is moved past,
// while one dropped for a forged claim moves no mark at all. The sequence behind
// a claim this machine refused is not progress, and recording progress over it
// would hide every genuine entry that follows.
var errNotOwner = errors.New("the entry names a row another installation owns")

// Import pulls every origin's entries this file has not applied and writes them.
//
// The pull is per origin and per mark, so a machine applies each origin's log in
// its own sequence order and never a sequence it has already passed. An origin
// with no mark is read from the start of its log, which is what a machine that
// has never imported from it needs.
//
// An entry is applied only when the origin it claims is the owner this machine's
// own file resolves for the row it names, so one installation cannot write, or
// remove, a row another installation owns. The owner comes from this file and
// never from the body, which is why a forged origin in a body changes nothing.
//
// Three things stop a batch, and none of them stops the run: a writer ahead of
// this build holds its own origin where it stopped, a batch this machine cannot
// act on is dropped and moved past, and a batch naming a row somebody else owns
// is dropped with the mark resting. Only a failure of this machine's own -- a log
// that will not answer, a file that is busy -- ends the import with an error.
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
	for _, batch := range batches(entries, marks) {
		origin := batch[0].Origin
		if held[origin] {
			continue
		}
		applied, hold, drop, err := i.applyBatch(batch, marks)
		if err != nil {
			return total, fmt.Errorf("synclog: import: %w", err)
		}
		if hold != nil {
			names, err := i.labels()
			if err != nil {
				return total, fmt.Errorf("synclog: import: %w", err)
			}
			hold.Label = names[hold.Origin]
			total.Held = append(total.Held, *hold)
			held[hold.Origin] = true
			continue
		}
		if drop != nil {
			total.Dropped = append(total.Dropped, *drop)
		}
		total.Applied += applied
		if applied > 0 {
			total.Batches++
		}
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

// applyBatch writes one batch in one transaction and moves the mark that covers
// it. It reports the entries that took effect, or the one thing that kept the
// batch from taking effect at all.
//
// Three answers, because the mark follows them. A batch carrying an entry from a
// newer binary is held: applying the entries ahead of that one would rest the
// mark inside the batch, and the transport hands back whole batches past the mark
// -- so every later run would skip the rest of it and the entries this machine
// cannot read would never arrive. A batch this machine refuses outright is
// dropped and the mark moves past it, because re-reading it would refuse it
// again for as long as the log holds it. A batch naming a row another
// installation owns is dropped with the mark resting, because the sequence
// behind a forged claim is not progress and a mark over it would hide every
// genuine entry behind it.
//
// marks is how far each origin has been applied, read from the file and kept
// current as the run goes, so a batch behind one already applied writes nothing.
func (i *Importer) applyBatch(batch []Entry, marks map[string]int) (int, *Hold, *Drop, error) {
	if len(batch) == 0 {
		return 0, nil, nil, nil
	}
	origin := batch[0].Origin
	if e, held := i.held(batch); held {
		return 0, &Hold{Origin: e.Origin, Seq: batch[0].Seq - 1, Schema: e.SchemaVersion}, nil, nil
	}
	applied, err := i.applyEntries(batch, marks)
	if err == nil {
		return applied, nil, nil, nil
	}
	if !refused(err) {
		return 0, nil, nil, err
	}
	drop := &Drop{Origin: origin, Seq: tail(batch), Reason: err.Error()}
	if errors.Is(err, errNotOwner) {
		return 0, nil, drop, nil
	}
	// The batch was refused for a body this machine cannot read, so the
	// transaction that would have carried its rows rolled back and the mark it
	// would have moved with them went with it. The mark moves here instead, in a
	// transaction of its own: without that the batch would be offered again on
	// every run and refuse itself again for as long as the log holds it.
	if err := i.past(batch, marks); err != nil {
		return 0, nil, nil, err
	}
	return 0, nil, drop, nil
}

// applyEntries writes one batch's entries in the order the exporter wrote them
// and moves the mark in the same transaction, so the rows and the mark that
// covers them commit together or neither does.
//
// handled counts the entries that did their work, which includes an entry
// naming a row this file no longer holds: a delete that finds nothing has still
// been applied, which is what keeps a re-read batch from stalling.
func (i *Importer) applyEntries(batch []Entry, marks map[string]int) (int, error) {
	origin := batch[0].Origin
	applied := 0
	err := i.db.Tx(func(tx *db.Tx) error {
		for _, e := range batch {
			handled, err := applyEntry(tx, e)
			if err != nil {
				return err
			}
			if handled {
				applied++
			}
		}
		return i.mark(tx, marks, origin, tail(batch), applied > 0)
	})
	return applied, err
}

// mark writes the mark for one origin, and only forwards. A batch whose tail is
// not past the mark this file already holds persists nothing, so a replayed or
// out-of-order sequence cannot rewind what has been applied and have the entries
// behind it offered again as though they were new.
func (i *Importer) mark(tx *db.Tx, marks map[string]int, origin string, seq int, moved bool) error {
	if !moved || seq <= marks[origin] {
		return nil
	}
	marks[origin] = seq
	return tx.SetImportMark(origin, seq)
}

// past moves the mark of a dropped batch beyond it, in a transaction of its own.
// It is the other half of dropping: without it the refused batch would be offered
// again on every run and would refuse itself again for as long as the log holds
// it, which is the state a reader cannot recover from on its own.
func (i *Importer) past(batch []Entry, marks map[string]int) error {
	origin, seq := batch[0].Origin, tail(batch)
	if err := i.db.SetImportMark(origin, seq); err != nil {
		return err
	}
	if seq > marks[origin] {
		marks[origin] = seq
	}
	return nil
}

// refused reports whether a batch that failed to apply is one this machine may
// pass over rather than fail on. A refusal is: it came from a body this machine
// does not control, and reading it again would refuse it again. Everything else
// -- a busy file, a failed read, a failed write -- is this machine's own trouble
// and ends the run with an error.
func refused(err error) bool {
	return errors.Is(err, ErrInvalid) || errors.Is(err, db.ErrInvalid)
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

// applyEntry writes one entry, and reports whether it did its work.
//
// The table name and every column name come from this machine's own schema and
// its shared-table list, never from the entry's body, so a body can carry values
// without carrying names to write them under.
//
// The owner of the row is the one thing a body never decides. It is resolved
// from this machine's own file, and an entry whose claimed origin is not the
// owner is refused rather than applied: otherwise one installation could remove
// a row another installation owns, or overwrite the owner column and take the
// row out of its machine's hands. A row this file does not hold has no owner to
// check against, so an upsert of one places the row under the claiming origin
// and a delete of one has already happened.
func applyEntry(tx *db.Tx, e Entry) (bool, error) {
	if _, ok := SharedTable(e.Table); !ok {
		return false, fmt.Errorf("synclog: import %s %s: %q is not a shared table: %w",
			e.Table, e.PK, e.Table, db.ErrInvalid)
	}
	var values map[string]any
	switch e.Op {
	case OpUpsert:
		decoded, err := DecodeBody(e.Body)
		if err != nil {
			return false, fmt.Errorf("synclog: import %s %s: %w", e.Table, e.PK, err)
		}
		values = decoded
	case OpDelete:
	default:
		return false, fmt.Errorf("synclog: import %s %s: op %q: %w", e.Table, e.PK, e.Op, ErrInvalid)
	}
	if err := owns(tx, e); err != nil {
		return false, err
	}
	if e.Op == OpDelete {
		if err := tx.ExchangeDelete(e.Table, e.PK); err != nil {
			return false, fmt.Errorf("synclog: import %s %s: %w", e.Table, e.PK, err)
		}
		return true, nil
	}
	// The owner is passed rather than taken from the body, so what this row's
	// owner column ends up holding is the owner this file resolved and not what
	// a remote claimed.
	if err := tx.ExchangeUpsert(e.Table, e.Origin, values); err != nil {
		return false, fmt.Errorf("synclog: import %s %s: %w", e.Table, e.PK, err)
	}
	return true, nil
}

// owns is the gate an entry passes before it is written: the owner this
// machine's file resolves for the row it names, which has to be the origin the
// entry claims. A row this file does not hold resolves to no owner, which is not
// a refusal -- an upsert of one places the row, and a delete of one is a no-op --
// so only a row that is here under somebody else's name is refused.
func owns(tx *db.Tx, e Entry) error {
	owner, found, err := tx.ResolveOwner(e.Table, e.PK)
	if err != nil {
		return fmt.Errorf("synclog: import %s %s: %w", e.Table, e.PK, err)
	}
	if !found || owner == e.Origin {
		return nil
	}
	return fmt.Errorf("synclog: import %s %s: %s does not own it, %s does: %w: %w",
		e.Table, e.PK, e.Origin, owner, errNotOwner, ErrInvalid)
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
func batches(entries []Entry, marks map[string]int) [][]Entry {
	order := make([]batchKey, 0, len(entries))
	grouped := make(map[batchKey][]Entry, len(entries))
	for _, e := range entries {
		if replayed(marks, e) {
			continue
		}
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

// replayed reports whether an entry's sequence is at or below the mark its origin
// has been applied to, and so is not offered to the importer again. Both the
// entry's own sequence and its batch's decide it: the transport hands back whole
// batches, so a batch that began before the mark is a batch whose order this
// machine has already followed, and applying any of it again would rewrite rows
// with content the log has since moved past.
func replayed(marks map[string]int, e Entry) bool {
	mark := marks[e.Origin]
	return e.Seq <= mark || e.Batch <= mark
}

// tail is the highest sequence a batch carries, which is the mark that covers it.
// The maximum is taken rather than the last entry read, so a transport handing
// entries back out of order cannot lower the mark this machine writes.
func tail(batch []Entry) int {
	high := 0
	for _, e := range batch {
		if e.Seq > high {
			high = e.Seq
		}
	}
	return high
}
