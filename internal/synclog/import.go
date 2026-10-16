package synclog

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Importer applies another installation's entries to this machine's file.
//
// It works in whole batches, and one batch is one transaction: the entries go
// in as the exporter ordered them -- every row removed before its parent,
// every parent before the rows that name it, with deletes before upserts --
// and the origin's mark moves in the same transaction. A batch that fails half way leaves neither the rows it had
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
	keyed
	db        *db.DB
	transport LogTransport
	known     int
	mover     BlobMover
	staging   string
	// chunk bounds a snapshot transaction's entries; zero is the default.
	chunk int
	// regionRows and passes bound a snapshot's rows and retry passes; zero is
	// the default.
	regionRows int
	passes     int
	// signedFrom is the sequence each origin must be signed from, localSigned
	// this machine's own records behind it and badBoundary the refusal for an
	// origin whose remote boundary did not verify; all are set per run and
	// only with a key.
	signedFrom  map[string]int
	localSigned map[string]int
	badBoundary map[string]error
	// cuts is the effective cut per origin -- this machine's own record and
	// every remote cut whose tag verified, the higher of the two per run --
	// localCut this machine's own records behind it and badCut the refusal
	// for an origin whose remote cut did not verify. Like the signed-from
	// facts, all are set per run and only with a keyring.
	cuts     map[string]EpochCut
	localCut map[string]db.EpochCut
	badCut   map[string]error
	// now is the clock the arrival window of the append-only tables is judged
	// against. It is a field rather than a call to time.Now so a case can place
	// a row on either side of the window without moving the wall clock.
	now func() time.Time
	// AfterBatch runs after each batch or snapshot chunk is written, so a
	// caller can account for the transfer before the next one starts.
	AfterBatch func()
}

// clock is the arrival window's clock, never nil after NewImporter.
func (i *Importer) clock() time.Time {
	if i.now == nil {
		return time.Now()
	}
	return i.now()
}

func (i *Importer) batchDone() {
	if i.AfterBatch != nil {
		i.AfterBatch()
	}
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
	mover, staging := blobWiring(t)
	return &Importer{db: d, transport: t, known: known, mover: mover, staging: staging, now: time.Now}
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
	// Gaps are the origins the log left a hole in: a batch that starts past the
	// sequence the origin stopped at. The origin holds for the run and the gap
	// is reported, so nothing below it is skipped and the missing batch is read
	// again once it arrives.
	Gaps []Gap
	// Stalled are the origins whose batch refers to a body that could not be
	// fetched and checked. Like a gap they hold for the run and are read again.
	Stalled []Stall
	// Snapshots is how many origins were brought up to their compaction floor.
	Snapshots int
}

// Moved reports whether a run moved any origin's mark: it applied an entry or
// dropped a batch past one. A run that only held an origin or reported a gap
// moved nothing -- a hold and a gap recur for as long as they are true -- so a
// drain loop stops on it rather than re-reading the same entries until a
// deadline. Batches is not the test: a dropped batch moves the mark through
// past() without incrementing it.
func (r ImportResult) Moved() bool {
	return r.Applied > 0 || len(r.Dropped) > 0 || r.Snapshots > 0
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
	// Reason, when set, is the whole report: the origin stopped at something
	// other than a schema this machine is behind.
	Reason string
}

// String is the report a hold reads as to whoever has to act on it. It names the
// installation rather than its sequence number, because the fix is to upgrade
// this machine and the sequence number says nothing about which writer's schema
// is the one waiting.
func (h Hold) String() string {
	if h.Reason != "" {
		return h.Reason
	}
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
	return fmt.Sprintf("dropped entries from %s up to sequence %d: %s", d.Origin, d.Seq, d.Reason)
}

// ParseDrop parses the sentence Drop.String() produces.
// It returns the Drop and true when s has the shape "dropped entries from <origin> up to sequence <seq>: <reason>",
// or zero Drop and false otherwise.
func ParseDrop(s string) (Drop, bool) {
	const prefix = "dropped entries from "
	if !strings.HasPrefix(s, prefix) {
		return Drop{}, false
	}
	rest := s[len(prefix):]
	const middle = " up to sequence "
	midIdx := strings.Index(rest, middle)
	if midIdx <= 0 {
		return Drop{}, false
	}
	origin := rest[:midIdx]
	rest = rest[midIdx+len(middle):]
	colonIdx := strings.Index(rest, ": ")
	if colonIdx <= 0 {
		return Drop{}, false
	}
	seqStr := rest[:colonIdx]
	reason := rest[colonIdx+2:]
	seq, err := strconv.Atoi(seqStr)
	if err != nil {
		return Drop{}, false
	}
	return Drop{
		Origin: origin,
		Seq:    seq,
		Reason: reason,
	}, true
}

// Gap is one origin the log handed a batch to out of order: the batch starts
// past the sequence the origin's mark rests at, or jumps past one inside itself,
// so the entries in between have not arrived. The origin holds where it stands
// rather than skipping to the batch, because advancing the mark over the hole
// would silently lose whatever the missing entries carried.
type Gap struct {
	// Origin is the installation whose batch starts past its mark.
	Origin string
	// Want is the first sequence the origin's next batch has to carry: its mark
	// plus one.
	Want int
	// Got is the sequence in the batch that did not follow the one before it:
	// the batch's own first when it begins late, or the entry that jumps inside
	// it.
	Got int
}

// String is the report a gap reads as to whoever has to act on it: the origin,
// where it is being held, and the sequence that arrived ahead of the one it
// expected.
func (g Gap) String() string {
	return fmt.Sprintf("%s is held at %d: a batch reaches %d, want %d; the entries in between have not arrived",
		g.Origin, g.Want-1, g.Got, g.Want)
}

// errNotOwner reports an entry whose claimed origin is not the owner of the row
// it names. It is an ErrInvalid refusal under a second name, so the code that
// drops a batch it cannot apply can tell this one apart from a body it merely
// does not understand. Both are moved past: a batch left resting at the origin's
// mark would sit at mark+1 forever and hold every batch behind it as a gap.
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
// Four things stop a batch, and none of them stops the run: a writer ahead of
// this build holds its own origin where it stopped, a batch that begins past the
// origin's mark -- or jumps inside itself -- holds it and is reported as a gap,
// and a batch this machine cannot act on, whether its body is unreadable or it
// names a row somebody else owns, is dropped and moved past. Only a failure of
// this machine's own -- a log that will not answer, a file that is busy -- ends
// the import with an error.
func (i *Importer) Import() (ImportResult, error) {
	marks, err := i.db.ImportMarks()
	if err != nil {
		return ImportResult{}, fmt.Errorf("synclog: import: %w", err)
	}
	state, err := i.transport.ReadMarks()
	if err != nil {
		return ImportResult{}, fmt.Errorf("synclog: import: read the floors: %w", err)
	}
	if err := i.loadSigned(state); err != nil {
		return ImportResult{}, err
	}
	total, err := i.DrainPending()
	if err != nil {
		return total, err
	}
	// An origin that held once holds for the rest of the run. Its later entries
	// were written by the same newer binary and are just as unreadable, so
	// reading them would apply part of a batch the exporter wrote as one.
	held := make(map[string]bool)
	if err := i.snapshots(state, marks, &total, held); err != nil {
		return total, err
	}
	entries, err := i.transport.Pull(marks)
	if err != nil {
		return total, fmt.Errorf("synclog: import: pull: %w", err)
	}
	for _, batch := range batches(entries, marks) {
		origin := batch[0].Origin
		if held[origin] {
			continue
		}
		// Sequence numbers per origin are contiguous by construction, so a
		// batch has to carry exactly the sequences from the origin's mark on. A
		// batch that begins later, or that jumps past a sequence inside itself,
		// is a hole: applying it would advance the mark over the entries in
		// between and lose them for good, so the origin holds and the hole is
		// reported.
		want := marks[origin] + 1
		if got, broken := firstHole(batch, want); broken {
			total.Gaps = append(total.Gaps, Gap{Origin: origin, Want: want, Got: got})
			held[origin] = true
			continue
		}
		applied, hold, drops, err := i.applyBatch(batch, marks)
		if holdsOrigin(err) {
			total.Stalled = append(total.Stalled, Stall{Origin: origin, Label: i.labelOf(origin), Reason: err.Error()})
			held[origin] = true
			continue
		}
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
		total.Dropped = append(total.Dropped, drops...)
		total.Applied += applied
		if applied > 0 {
			total.Batches++
			i.batchDone()
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

// labelOf is the name an installation's file carries, or its id when the
// directory cannot say.
func (i *Importer) labelOf(origin string) string {
	if names, err := i.labels(); err == nil && names[origin] != "" {
		return names[origin]
	}
	return origin
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
// dropped and the mark moves past it, whether its body is one this machine
// cannot read or it claims a row another installation owns: re-reading it would
// refuse it again for as long as the log holds it, and leaving it at the mark
// would hold every batch behind it as a gap.
//
// marks is how far each origin has been applied, read from the file and kept
// current as the run goes, so a batch behind one already applied writes nothing.
func (i *Importer) applyBatch(batch []Entry, marks map[string]int) (applied int, _ *Hold, _ []Drop, _ error) {
	if len(batch) == 0 {
		return 0, nil, nil, nil
	}
	origin := batch[0].Origin
	if e, held := i.held(batch); held {
		return 0, &Hold{Origin: e.Origin, Seq: batch[0].Seq - 1, Schema: e.SchemaVersion}, nil, nil
	}
	if hold := i.unknownDoc(origin, batch[0].Seq-1, batch); hold != nil {
		return 0, hold, nil, nil
	}
	if err := i.verify(origin, batch); err != nil {
		return 0, nil, nil, err
	}
	docs, rows := splitDocs(batch)
	handled, err := i.takeDocs(origin, docs)
	if err != nil && !refused(err) {
		return 0, nil, nil, err
	}
	var drops []Drop
	if err == nil {
		applied, drops, err = i.fetchAndApply(batch, rows, handled, marks)
	}
	if err == nil {
		return applied, nil, drops, nil
	}
	if !refused(err) {
		return 0, nil, nil, err
	}
	drop := Drop{Origin: origin, Seq: tail(batch), Reason: err.Error()}
	// The batch was refused, so the transaction that would have carried its rows
	// rolled back and the mark it would have moved with them went with it. The
	// mark moves here instead, in a transaction of its own: without that the
	// batch would be offered again on every run and refuse itself again for as
	// long as the log holds it.
	if err := i.past(batch, marks); err != nil {
		return 0, nil, nil, err
	}
	return 0, nil, []Drop{drop}, nil
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
//
// Like mark, it only moves forward. A dropped batch whose tail is at or behind
// the mark moves nothing, so a transport handing a batch back out of order
// cannot rewind the mark and have the entries behind it offered again as though
// they were new.
func (i *Importer) past(batch []Entry, marks map[string]int) error {
	origin, seq := batch[0].Origin, tail(batch)
	if seq <= marks[origin] {
		return nil
	}
	marks[origin] = seq
	return i.db.SetImportMark(origin, seq)
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
//
// now is the clock the append-only tables' arrival window is judged against; it
// decides nothing about any other table. A row that arrived already older than
// the window is skipped rather than written, and still counts as handled: it did
// its work by being read, so the mark that covers it moves in the same
// transaction and the batch is not offered again.
func applyEntry(tx *db.Tx, e Entry, now time.Time) (bool, error) {
	if _, ok := SharedTable(e.Table); !ok {
		return false, fmt.Errorf("synclog: import %s %s: %q is not a shared table: %w",
			e.Table, e.PK, e.Table, ErrInvalid)
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
	skip, err := historyEntry(e, values, now)
	if err != nil {
		return false, err
	}
	if skip {
		// A row that arrives already out of the window did its work by being
		// read: it is reported applied so the mark moves in this same
		// transaction and the batch is not offered again.
		return true, nil
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
	if err := ownsAfterWrite(tx, e); err != nil {
		return false, err
	}
	return true, nil
}

// ownsAfterWrite resolves the row's owner again once it is written, inside the
// same transaction, and refuses it unless that owner is the entry's origin. The
// gate before the write cannot answer for a row this file did not hold -- a new
// child -- nor for the parent a row was just re-pointed at, because both are
// facts about the write itself. A child that landed under another installation's
// parent would otherwise become that parent's history, and a forged entry could
// move a row out of the hands of the installation that reconciles it.
func ownsAfterWrite(tx *db.Tx, e Entry) error {
	owner, found, err := tx.ResolveOwner(e.Table, e.PK)
	if err != nil {
		return fmt.Errorf("synclog: import %s %s: %w", e.Table, e.PK, err)
	}
	if !found || owner == e.Origin {
		return nil
	}
	return fmt.Errorf("synclog: import %s %s: landed under %s, not %s: %w: %w",
		e.Table, e.PK, owner, e.Origin, errNotOwner, ErrInvalid)
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
