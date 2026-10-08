package synclog

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
	// A batch is one unit for a reader: the entries it wrote share a Batch, the
	// sequence number of the first of them, and a reader applies whole batches.
	Append(entries []Entry) ([]Entry, error)

	// Pull returns the entries of every origin other than this one that follow
	// the mark given for it, in sequence order and in whole batches. An origin
	// with no mark is read from the start of its log. A batch that begins at or
	// before the mark is not split: the reader either has the whole batch or
	// none of it.
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
}

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
}
