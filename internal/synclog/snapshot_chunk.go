package synclog

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

type entryRefusal struct {
	at    int
	entry Entry
	err   error
}

// applyChunk resolves a chunk's bodies and writes it in one transaction,
// returning constraint-refused entries for retry once the rest is in. An entry
// refused for what it is rolls the chunk back and drops: leaving the rest applied
// would keep a row landed under the wrong owner.
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

// chunkFetchFailure maps a chunk's failed fetch as a batch's: missing ends the run
// naming the row, contradictory refs drop the chunk, anything else holds the
// origin. A dropped chunk is reported, not an error.
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

// writeChunk applies a chunk in one transaction. A foreign-key refusal fails alone
// with the transaction open, which the engine's tests pin, so the entry is set
// aside and the rest go on.
func (i *Importer) writeChunk(chunk []Entry) (applied int, deferred []Entry, refusal *entryRefusal, err error) {
	err = i.db.Tx(func(tx *db.Tx) error {
		applied, deferred = 0, nil
		for n, e := range chunk {
			_, aerr := applyEntry(tx, e, i.transport, i.clock())
			switch {
			case aerr == nil:
				applied++
			case errors.Is(aerr, ErrInvalid):
				refusal = &entryRefusal{at: n, entry: e, err: aerr}
				return aerr
			case errors.Is(aerr, db.ErrInvalid):
				deferred = append(deferred, e)
			default:
				return aerr
			}
		}
		return nil
	})
	if refusal != nil {
		return 0, nil, refusal, nil
	}
	return applied, deferred, nil, err
}
