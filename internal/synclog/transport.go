package synclog

import "time"

// LogTransport is where the per-origin log lives. It is the seam the exporter,
// importer and reconcile are written against, so every test in this slice runs
// against MemTransport and a second implementation over another wire needs no
// change here.
//
// The one rule a transport owns is sequence assignment: a machine names no
// sequence numbers of its own. It hands over a batch and the transport numbers
// it, so a local file restored from a backup never reuses a number and two
// appends can never hand out the same one.
type LogTransport interface {
	// Append writes one batch of this origin's entries as a single atomic
	// write, numbering them from one past the highest sequence this origin
	// already holds, and returns them numbered. Entries of another origin are
	// refused: an installation appends only for the rows it owns, and the
	// refusal is what stops two machines writing the same row.
	//
	// The rule above is the transport's, and it is not the only one. A transport
	// only knows what origin an entry claims; which rows that origin owns is a
	// question about the reader's own file, and so the importer answers it again
	// for itself before it writes anything. An entry that names a row the reader
	// resolves to another installation is refused there even when it arrived
	// through an honest transport, so the ownership rule does not rest on the
	// appender being honest.
	//
	// A batch is one unit for a reader: the entries it wrote share a Batch, the
	// sequence number of the first of them, and a reader applies whole batches.
	Append(entries []Entry) ([]Entry, error)

	// Pull returns the entries of every origin other than this one that follow
	// the mark given for it, in sequence order and in whole batches. An origin
	// with no mark is read from the start of its log. A batch that begins at or
	// before the mark is not split: the reader either has the whole batch or
	// none of it.
	//
	// The sequence numbers and the origin an entry carries are the transport's
	// to hand out, and the reader does not take them on trust: an entry at or
	// below the mark it already holds is not applied again, and a mark is only
	// ever written forward from the entries that were actually applied. A
	// transport that answers with a sequence behind the mark, or one far ahead
	// of it, therefore cannot rewind this machine's rows or skip the entries
	// behind it.
	Pull(marks map[string]int) ([]Entry, error)

	// Head returns the latest entry per row of one origin: the row, the
	// sequence number of the entry that last wrote it and that entry's body
	// hash. This is the state reconcile compares against, so a row missing here
	// is a row the origin has not written yet and a row present here with a
	// different hash is one whose contents changed.
	Head(origin string) ([]HeadRow, error)

	// Stats reports what the log holds, so a caller can tell an empty log from a
	// transport that is refusing to answer.
	Stats() (Stats, error)

	// PublishMarks records how far this reader has applied of each origin given,
	// as of at. The reader is the transport's own installation rather than a
	// field of the call: a mark is what a compaction floor is computed from, so
	// naming another reader's would be choosing what the log may drop on a
	// machine that does not hold the entries it speaks for.
	//
	// An origin's own mark is not published. A reader never needs one, and
	// writing it would only give the floor a row it must then be taught to
	// ignore.
	PublishMarks(marks map[string]int, at time.Time) error

	// ReadMarks answers with every published mark, the floors the log has
	// recorded, and each origin's head and entry count -- the three inputs a
	// compaction decides from. A transport that has no floors yet answers with
	// an empty set rather than an error, because nothing has compacted.
	ReadMarks() (MarkState, error)

	// PullRange returns entries of one origin with after < seq <= upto, in
	// sequence order. Unlike Pull it does not hand back whole batches: the
	// snapshot path reads a region that ends on a batch tail and keeps the
	// latest entry per key, so a batch cut by a page changes nothing.
	//
	// A transport may answer with fewer entries than the range holds. The caller
	// asks again from the last sequence it was given until an answer is empty.
	PullRange(origin string, after, upto int) ([]Entry, error)

	// CompactOwn drops this installation's own superseded entries up to floor and
	// records the floor, as one write. The origin is the transport's own for the
	// reason PublishMarks has: only the machine that wrote a log may drop from it.
	CompactOwn(floor int) (CompactResult, error)
}

// CompactResult is what one compaction did: how many entries it dropped and the
// floor the log now records, which is never below what it recorded before.
type CompactResult struct {
	Dropped int
	Floor   int
}

// MarkState is what a transport holds about compaction: the marks readers
// published, and the log's own size per origin.
//
// The maps are nil rather than empty when there is nothing to report, so an
// origin that has compacted nothing and one whose floor is at the start of its
// log stay apart -- the two mean different things to whoever reads them.
type MarkState struct {
	// Marks are the published positions, one per reader per origin.
	Marks []Mark
	// Floors is what each origin has already been compacted to.
	Floors map[string]int
	// Heads is each origin's highest sequence in the log.
	Heads map[string]int
	// Entries is how many entries the log holds for each origin.
	Entries map[string]int
	// Sizes is each origin's log size in bytes: the bodies plus a fixed
	// overhead per row.
	Sizes map[string]int64
}

// LogRowOverhead is the bytes a log row costs beside its body, for the columns
// the body does not carry.
const LogRowOverhead = 96

// HeadRow is one row of an origin's `head`: the materialized latest state of a
// single shared row, as the log holds it.
type HeadRow struct {
	// Table is the shared table the row is in.
	Table string
	// PK is the row's primary key as the json_array text the outbox records.
	PK string
	// Seq is the sequence number of the entry that last wrote the row.
	Seq int
	// Hash is that entry's body hash, which is what a row read locally is
	// compared against.
	Hash string
}

// Stats is what a transport reports about the log it holds.
type Stats struct {
	// Entries is how many entries the log holds across every origin.
	Entries int
	// Origins is how many distinct origins have appended to it.
	Origins int
	// Seq is the highest sequence number the log holds, across origins. A
	// sequence number belongs to one origin, so this is the largest rather than
	// a count.
	Seq int
	// TursoSent and TursoReceived are the network bytes the transport has pushed
	// and pulled over its life, and R2Put and R2Get the body bytes it has sent to
	// and taken from the bucket. All four are cumulative for one transport's life
	// and are what a month of sync costs; a zero means the transport has moved
	// nothing, which is how a fresh one is told from one that is not reporting.
	TursoSent     int64
	TursoReceived int64
	R2Put         int64
	R2Get         int64
}
