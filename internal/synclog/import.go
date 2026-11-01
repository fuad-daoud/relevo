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
// It works in whole batches, one batch per transaction: entries go in in exporter
// order with the origin's mark, so a half-failed batch leaves neither rows nor
// mark and the next run sees it again from the same mark.
// A batch this machine cannot apply at all is dropped rather than failed on, and the
// mark moves past it so it is not offered again; a hard failure -- a log that will
// not answer, a busy file -- moves no mark, because nothing was applied.
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
	// origin whose remote boundary did not verify; all are set per run, only with a key.
	signedFrom  map[string]int
	schemaSeen  map[string]int
	localSigned map[string]int
	badBoundary map[string]error
	// cuts is the effective cut per origin -- this machine's own record and
	// every verified remote cut, the higher of the two per run -- localCut this
	// machine's records behind it and badCut the refusal for an unverified remote
	// cut. All are set per run, only with a keyring.
	cuts     map[string]EpochCut
	localCut map[string]db.EpochCut
	badCut   map[string]error
	// now is the clock the append-only tables' arrival window is judged against. It
	// is a field so a case can place a row on either side of the window without
	// moving the wall clock.
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
// reads from. An import writes with this machine's schema deciding what a body
// means, and a column this build does not carry is dropped rather than written.
// The compared version is this binary's own, read from the file: it decides
// whether an entry's body means anything here.
func NewImporter(d *db.DB, t LogTransport) *Importer {
	_, known := d.SchemaVersions()
	mover, staging := blobWiring(t)
	return &Importer{db: d, transport: t, known: known, mover: mover, staging: staging, now: time.Now}
}

// ImportResult is what one import moved: the batches it applied and the entries
// within them.
type ImportResult struct {
	Batches int
	Applied int
	// Held are the origins this machine could not follow past an entry their
	// writer wrote with a newer schema. They are a report rather than a
	// failure: the entries before the hold applied and the mark rests there for
	// an upgraded machine to resume from.
	Held []Hold
	// Dropped are the batches this machine would not apply at all and has moved
	// past. Each is reported rather than silently passed over so its entry can
	// be told apart.
	Dropped []Drop
	// Gaps are the origins the log left a hole in: a batch starting past the
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
// dropped a batch past one. A run that only held or reported a gap moved
// nothing -- both recur while true -- so a drain loop stops on it rather than
// re-reading the same entries until a deadline. Batches is not the test: a
// dropped batch moves the mark through past() without incrementing it.
func (r ImportResult) Moved() bool {
	return r.Applied > 0 || len(r.Dropped) > 0 || r.Snapshots > 0
}

// Hold is one origin stopped at an entry this machine's schema cannot read. The
// origin's mark rests at the last entry applied before it, which is also where
// later entries are re-read from.
type Hold struct {
	Origin string
	// Label is the name that installation's own file carries, so the report
	// names the machine rather than the id rows are stamped with.
	Label string
	// Seq is the sequence the origin's mark rests at: the entry before the one
	// this machine cannot read.
	Seq int
	// Schema is the writer's schema version at the entry that stopped it.
	Schema int
	// Reason, when set, is the whole report: the origin stopped at something
	// other than a schema this machine is behind.
	Reason string
}

// String is the report a hold reads as to whoever has to act on it. It names the
// installation rather than its sequence number: the fix is to upgrade this
// machine.
func (h Hold) String() string {
	if h.Reason != "" {
		return h.Reason
	}
	return fmt.Sprintf("relevo on this machine is older than %s, which writes schema %d: upgrade this machine to follow it (mark rests at %d)",
		h.Label, h.Schema, h.Seq)
}

// Drop is one batch this machine refused whole and passed over. It is a report
// and not a failure: the entries behind it are not offered again.
type Drop struct {
	Origin string
	// Seq is the last sequence the batch carried, and the mark it left behind.
	Seq int
	// Reason is the refusal itself, naming the entry and the table it named.
	Reason string
}

// String is the report a drop reads as to whoever has to act on it. It names the
// entry rather than the origin alone: the origin is usually fine and one entry
// inside it is what this build cannot take.
func (d Drop) String() string {
	return fmt.Sprintf("dropped entries from %s up to sequence %d: %s", d.Origin, d.Seq, d.Reason)
}

// ParseDrop parses the sentence Drop.String() produces. It returns the Drop and
// true when s has the shape "dropped entries from <origin> up to sequence <seq>:
// <reason>", or zero Drop and false otherwise.
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
// past the mark, or jumps inside itself, so the entries in between have not
// arrived. The origin holds rather than skipping, because advancing over the
// hole would lose what the missing entries carried.
type Gap struct {
	Origin string
	// Want is the first sequence the origin's next batch has to carry: its mark
	// plus one.
	Want int
	// Got is the sequence that did not follow the one before it: the batch's own
	// first when it begins late, or the entry that jumps inside it.
	Got int
}

// String is the report a gap reads as to whoever has to act on it: the origin,
// where it is held, and the sequence that arrived ahead of the expected one.
func (g Gap) String() string {
	return fmt.Sprintf("%s is held at %d: a batch reaches %d, want %d; the entries in between have not arrived",
		g.Origin, g.Want-1, g.Got, g.Want)
}

// errNotOwner reports an entry whose claimed origin is not the row owner's. It
// is an ErrInvalid refusal under a second name, so a dropped batch tells it
// apart from an unreadable body. Both move past: a batch left at the mark would
// sit at mark+1 forever and hold every batch behind it as a gap.
var errNotOwner = errors.New("the entry names a row another installation owns")

// Import pulls every origin's entries this file has not applied and writes them.
// The pull is per origin and per mark, in each origin's sequence order and never
// past what was applied; an origin with no mark is read from the start of its log.
// An entry is applied only when the origin it claims is the owner this file
// resolves for the row: the owner comes from this file and never from the body,
// so a forged origin in a body changes nothing.
// A writer ahead of this build holds its origin, a batch past the mark or jumping
// inside itself holds as a gap, and an inapplicable batch drops and moves past;
// none stops the run. Only this machine's own failure -- an unanswering log, a
// busy file -- ends the import with an error.
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
	// An origin that held once holds for the run: its later entries came from the
	// same newer binary, so reading them would split a batch the exporter wrote as one.
	held := make(map[string]bool)
	if err := i.snapshots(state, marks, &total, held); err != nil {
		return total, err
	}
	entries, err := i.transport.Pull(marks)
	if err != nil {
		return total, fmt.Errorf("synclog: import: pull: %w", err)
	}
	allBatches := batches(entries, marks)
	byOrigin := make(map[string][][]Entry)
	var originOrder []string
	for _, batch := range allBatches {
		orig := batch[0].Origin
		if _, exists := byOrigin[orig]; !exists {
			originOrder = append(originOrder, orig)
		}
		byOrigin[orig] = append(byOrigin[orig], batch)
	}

	if err := i.drainBatchesFixpoint(originOrder, byOrigin, marks, &total, held); err != nil {
		return total, fmt.Errorf("synclog: import: %w", err)
	}
	return total, nil
}

// batchWaitingAfter names the entry a batch waits for in another origin's log.
// An entry of this installation's own log is applied here, so a takeover fencing
// this machine's own entries never waits for them.
func batchWaitingAfter(batch []Entry, marks map[string]int, own string) *Ref {
	for _, e := range batch {
		if e.After != nil && e.After.Origin != own && marks[e.After.Origin] < e.After.Seq {
			return e.After
		}
	}
	return nil
}

func (i *Importer) drainOriginBatch(orig string, batch []Entry, marks map[string]int, total *ImportResult, held map[string]bool) (bool, error) {
	if waitingOn := batchWaitingAfter(batch, marks, i.db.Origin()); waitingOn != nil {
		return false, nil
	}
	want := marks[orig] + 1
	if got, broken := firstHole(batch, want); broken {
		total.Gaps = append(total.Gaps, Gap{Origin: orig, Want: want, Got: got})
		held[orig] = true
		return false, nil
	}
	applied, hold, drops, err := i.applyBatch(batch, marks)
	if holdsOrigin(err) {
		total.Stalled = append(total.Stalled, Stall{Origin: orig, Label: i.labelOf(orig), Reason: err.Error()})
		held[orig] = true
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hold != nil {
		names, err := i.labels()
		if err != nil {
			return false, err
		}
		hold.Label = names[hold.Origin]
		total.Held = append(total.Held, *hold)
		held[hold.Origin] = true
		return false, nil
	}
	total.Dropped = append(total.Dropped, drops...)
	total.Applied += applied
	if applied > 0 {
		total.Batches++
		i.batchDone()
	}
	return true, nil
}

func (i *Importer) drainBatchesFixpoint(originOrder []string, byOrigin map[string][][]Entry, marks map[string]int, total *ImportResult, held map[string]bool) error {
	for {
		progress := false
		for _, orig := range originOrder {
			if held[orig] || len(byOrigin[orig]) == 0 {
				continue
			}
			batch := byOrigin[orig][0]
			advanced, err := i.drainOriginBatch(orig, batch, marks, total, held)
			if err != nil {
				return err
			}
			if advanced {
				progress = true
				byOrigin[orig] = byOrigin[orig][1:]
			}
		}
		if !progress {
			break
		}
	}
	return i.recordRemainingAfterHolds(originOrder, byOrigin, marks, total, held)
}

func (i *Importer) recordRemainingAfterHolds(originOrder []string, byOrigin map[string][][]Entry, marks map[string]int, total *ImportResult, held map[string]bool) error {
	for _, orig := range originOrder {
		if held[orig] || len(byOrigin[orig]) == 0 {
			continue
		}
		batch := byOrigin[orig][0]
		if waitingOn := batchWaitingAfter(batch, marks, i.db.Origin()); waitingOn != nil {
			names, err := i.labels()
			if err != nil {
				return err
			}
			afterLabel := names[waitingOn.Origin]
			if afterLabel == "" {
				afterLabel = waitingOn.Origin
			}
			reason := fmt.Sprintf("%s is waiting for %s at seq %d", orig, afterLabel, waitingOn.Seq)
			total.Held = append(total.Held, Hold{
				Origin: orig,
				Label:  names[orig],
				Seq:    marks[orig],
				Reason: reason,
			})
			held[orig] = true
		}
	}
	return nil
}

// labels is every installation the directory carries, keyed by id. A hold names
// an installation rather than a sequence, and the directory turns one into the
// other; an unheard-of origin keeps its own id.
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

// labelOf is the name an installation's file carries, or its id when unknown.
func (i *Importer) labelOf(origin string) string {
	if names, err := i.labels(); err == nil && names[origin] != "" {
		return names[origin]
	}
	return origin
}

// applyBatch writes one batch in one transaction and moves the mark that covers
// it. It reports the entries that took effect, or the one thing that kept the
// batch from taking effect.
// A batch from a newer binary is held: applying ahead of it would rest the mark
// inside the batch, and the transport hands back whole batches past the mark, so
// later runs would skip the rest and the unreadable entries would never arrive.
// A refused batch drops and the mark moves past it: re-reading would refuse it
// again while the log holds it, and leaving it at the mark would hold every
// batch behind it as a gap.
// marks is how far each origin is applied, kept current as the run goes, so a
// batch behind one already applied writes nothing.
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
		var holdErr *HoldError
		if errors.As(err, &holdErr) {
			return 0, &Hold{Origin: origin, Seq: batch[0].Seq - 1, Reason: holdErr.Reason}, nil, nil
		}
		return 0, nil, nil, err
	}
	if err := i.noteSchema(origin, batch); err != nil {
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
	var holdErr *HoldError
	if errors.As(err, &holdErr) {
		return 0, &Hold{Origin: origin, Seq: batch[0].Seq - 1, Reason: holdErr.Reason}, nil, nil
	}
	if !refused(err) {
		return 0, nil, nil, err
	}
	drop := Drop{Origin: origin, Seq: tail(batch), Reason: err.Error()}
	// The refused batch's transaction rolled back with its mark, so the mark
	// moves here instead, in a transaction of its own: without that the batch
	// would be offered and refused again on every run while the log holds it.
	if err := i.past(batch, marks); err != nil {
		return 0, nil, nil, err
	}
	return 0, nil, []Drop{drop}, nil
}

// mark writes the mark for one origin, and only forwards. A batch whose tail is
// not past the held mark persists nothing, so a replayed or out-of-order sequence
// cannot rewind what was applied.
func (i *Importer) mark(tx *db.Tx, marks map[string]int, origin string, seq int, moved bool) error {
	if !moved || seq <= marks[origin] {
		return nil
	}
	marks[origin] = seq
	return tx.SetImportMark(origin, seq)
}

// past moves the mark of a dropped batch beyond it, in a transaction of its own:
// without it the refused batch would be offered and refused again on every run.
// Like mark it only moves forward, so an out-of-order batch cannot rewind the mark.
func (i *Importer) past(batch []Entry, marks map[string]int) error {
	origin, seq := batch[0].Origin, tail(batch)
	if seq <= marks[origin] {
		return nil
	}
	marks[origin] = seq
	return i.db.SetImportMark(origin, seq)
}

// refused reports whether a failed batch may be passed over rather than failed
// on. A refusal came from a body this machine does not control, and reading it
// again would refuse it again. Anything else -- a busy file, a failed read or
// write -- is this machine's own trouble and ends the run.
func refused(err error) bool {
	return errors.Is(err, ErrInvalid) || errors.Is(err, db.ErrInvalid)
}

// held is the first entry in a batch a newer binary wrote, and whether the batch
// has one. The whole batch is searched: the version can differ within one batch
// when the writer upgraded between two of its own appends.
func (i *Importer) held(batch []Entry) (Entry, bool) {
	for _, e := range batch {
		if e.SchemaVersion > i.known {
			return e, true
		}
	}
	return Entry{}, false
}
