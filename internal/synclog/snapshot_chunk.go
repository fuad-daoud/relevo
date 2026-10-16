package synclog

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

// entryRefusal is the one entry of a chunk the file refused outright, and why.
type entryRefusal struct {
	at    int
	entry Entry
	err   error
}

// applyChunk resolves a chunk's bodies and writes it in one transaction. It
// returns the entries a constraint refused, to be retried once the rest is in.
//
// An entry refused for what it is -- a row another installation owns, a body that
// cannot be read -- rolls the chunk back, is reported and dropped, and the chunk
// is written again without it. Leaving the rest of the chunk applied would keep
// a row that landed under the wrong owner.
func (i *Importer) applyChunk(origin string, chunk []Entry, total *ImportResult) (int, []Entry, error) {
	for {
		if err := resolveRefs(i.mover, i.staging, chunk); err != nil {
			return 0, nil, i.chunkFetchFailure(origin, chunk, err, total)
		}
		applied, deferred, refusal, err := i.writeChunk(chunk)
		if err != nil {
			return 0, nil, fmt.Errorf("synclog: import: %w", err)
		}
		if refusal == nil {
			return applied, deferred, nil
		}
		bad := chunk[refusal.at]
		total.Dropped = append(total.Dropped, Drop{Origin: origin, Seq: bad.Seq, Reason: refusal.err.Error()})
		chunk = append(append([]Entry(nil), chunk[:refusal.at]...), chunk[refusal.at+1:]...)
	}
}

// chunkFetchFailure says what a failed fetch makes of a chunk, the way a batch's
// does: a missing object ends the run naming the row, a chunk whose refs
// contradict their rows is dropped, and anything else holds the origin with the
// mark where it was. A dropped chunk is not an error: it is reported and the
// run goes on.
func (i *Importer) chunkFetchFailure(origin string, chunk []Entry, err error, total *ImportResult) error {
	var missing *missingBlob
	switch {
	case errors.As(err, &missing):
		return i.missingError(missing)
	case refused(err):
		total.Dropped = append(total.Dropped, Drop{Origin: origin, Seq: tail(chunk), Reason: err.Error()})
		return nil
	default:
		return &stallError{origin: origin, cause: fmt.Errorf("a body it refers to could not be fetched, %w", err)}
	}
}

// writeChunk applies a chunk in one transaction.
//
// A statement a foreign key refuses fails alone and leaves the transaction open,
// which the engine's tests pin, so the entry is set aside and the rest go on.
func (i *Importer) writeChunk(chunk []Entry) (applied int, deferred []Entry, refusal *entryRefusal, err error) {
	err = i.db.Tx(func(tx *db.Tx) error {
		applied, deferred = 0, nil
		a, d, _, ref, aerr := sweepEntries(tx, chunk, i.clock())
		if ref != nil {
			refusal = ref
			return ref.err
		}
		if aerr != nil {
			return aerr
		}
		applied, deferred = a, d
		return nil
	})
	if refusal != nil {
		return 0, nil, refusal, nil
	}
	return applied, deferred, nil, err
}
