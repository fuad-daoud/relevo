package synclog

import (
	"errors"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// sweepEntries applies a slice of entries in order in the current transaction.
// An entry that succeeds increments applied.
// An entry that fails with ErrInvalid is an immediate refusal (the transaction
// must be rolled back and the entry dropped).
// An entry that fails with db.ErrInvalid is a constraint refusal (FK or UNIQUE);
// it leaves the transaction open and is set aside in deferred with its error
// recorded in defErrs.
// Any other error halts the transaction.
func sweepEntries(tx *db.Tx, entries []Entry, now time.Time) (applied int, deferred []Entry, defErrs []error, refusal *entryRefusal, err error) {
	for n, e := range entries {
		handled, aerr := applyEntry(tx, e, now)
		switch {
		case aerr == nil:
			if handled {
				applied++
			}
		case errors.Is(aerr, ErrInvalid):
			refusal = &entryRefusal{at: n, entry: e, err: aerr}
			return applied, nil, nil, refusal, aerr
		case errors.Is(aerr, db.ErrInvalid):
			deferred = append(deferred, e)
			defErrs = append(defErrs, aerr)
		default:
			return applied, nil, nil, nil, aerr
		}
	}
	return applied, deferred, defErrs, nil, nil
}

// retryPasses retries deferred entries in passes while each pass makes progress.
// When passes stop (no progress or limit reached), remaining entries and their
// latest errors are returned to be dropped individually.
func retryPasses(tx *db.Tx, deferred []Entry, defErrs []error, maxPasses int, now time.Time) (applied int, remaining []Entry, remainingErrs []error, refusal *entryRefusal, err error) {
	cur := deferred
	curErrs := defErrs
	for pass := 1; pass < maxPasses && len(cur) > 0; pass++ {
		a, next, nextErrs, ref, aerr := sweepEntries(tx, cur, now)
		if ref != nil {
			return applied, nil, nil, ref, ref.err
		}
		if aerr != nil {
			return applied, nil, nil, nil, aerr
		}
		applied += a
		if a == 0 || len(next) == 0 {
			cur = next
			curErrs = nextErrs
			break
		}
		cur = next
		curErrs = nextErrs
	}
	return applied, cur, curErrs, nil, nil
}

// writeBatch attempts to apply a batch of rows within one transaction.
// If an entry is refused with ErrInvalid, the transaction rolls back and the
// refusal is reported so the batch can be retried without that entry.
// Remaining deferred entries after all passes become individual drops, and the
// mark moves in the same transaction.
func (i *Importer) writeBatch(batch, rows []Entry, docs, priorDrops int, marks map[string]int) (applied int, drops []Drop, refusal *entryRefusal, err error) {
	origin := batch[0].Origin
	var signedAt int
	var wrote bool
	err = i.db.Tx(func(tx *db.Tx) error {
		applied = 0
		drops = nil
		a, deferred, defErrs, ref, aerr := sweepEntries(tx, rows, i.now())
		if ref != nil {
			refusal = ref
			return ref.err
		}
		if aerr != nil {
			return aerr
		}
		applied += a

		if len(deferred) > 0 {
			pa, rem, remErrs, pref, perr := retryPasses(tx, deferred, defErrs, orDefault(i.passes, maxSnapshotPasses), i.now())
			if pref != nil {
				refusal = pref
				return pref.err
			}
			if perr != nil {
				return perr
			}
			applied += pa
			for idx, e := range rem {
				drops = append(drops, Drop{Origin: origin, Seq: e.Seq, Reason: remErrs[idx].Error()})
			}
		}

		if signedAt, wrote, err = i.recordSigned(tx, origin, batch); err != nil {
			return err
		}
		moved := applied+docs > 0 || len(drops) > 0 || priorDrops > 0
		return i.mark(tx, marks, origin, tail(batch), moved)
	})
	if refusal != nil {
		return 0, nil, refusal, nil
	}
	if err == nil {
		i.noteSigned(origin, signedAt, wrote)
	}
	return applied, drops, nil, err
}

// applyEntries applies rows for one batch, isolating individual entry refusals
// without abandoning the batch. An entry failing ErrInvalid causes a rollback
// and retry without that entry; entries failing db.ErrInvalid are retried in
// passes and refused individually if unresolved.
func (i *Importer) applyEntries(batch, rows []Entry, docs int, marks map[string]int) (int, []Drop, error) {
	var totalDrops []Drop
	for {
		applied, batchDrops, refusal, err := i.writeBatch(batch, rows, docs, len(totalDrops), marks)
		if err != nil {
			return 0, nil, err
		}
		if refusal == nil {
			totalDrops = append(totalDrops, batchDrops...)
			return applied + docs, totalDrops, nil
		}
		bad := refusal.entry
		origin := bad.Origin
		if origin == "" && len(batch) > 0 {
			origin = batch[0].Origin
		}
		totalDrops = append(totalDrops, Drop{Origin: origin, Seq: bad.Seq, Reason: refusal.err.Error()})
		idx := indexOfEntry(rows, bad)
		if idx < 0 {
			idx = refusal.at
		}
		rows = append(append([]Entry(nil), rows[:idx]...), rows[idx+1:]...)
	}
}

func indexOfEntry(entries []Entry, target Entry) int {
	for i, e := range entries {
		if e.Seq == target.Seq {
			return i
		}
	}
	return -1
}
