package synclog

import (
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// sweepEntries applies entries in order in the current transaction. ErrInvalid is
// an immediate refusal, returned for the caller to roll back and drop; db.ErrInvalid
// (FK or UNIQUE) leaves the transaction open and is deferred with its error;
// any other error halts.
func sweepEntries(tx *db.Tx, entries []Entry, transport LogTransport, now time.Time) (applied int, deferred []Entry, defErrs []error, refusal *entryRefusal, err error) {
	for n, e := range entries {
		handled, aerr := applyEntry(tx, e, transport, now)
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

// retryPasses retries deferred entries while each pass lands something and maxPasses
// is not reached; the remainder is dropped individually.
func retryPasses(tx *db.Tx, deferred []Entry, defErrs []error, maxPasses int, transport LogTransport, now time.Time) (applied int, remaining []Entry, remainingErrs []error, refusal *entryRefusal, err error) {
	cur := deferred
	curErrs := defErrs
	for pass := 1; pass < maxPasses && len(cur) > 0; pass++ {
		a, next, nextErrs, ref, aerr := sweepEntries(tx, cur, transport, now)
		if ref != nil {
			return applied, nil, nil, ref, ref.err
		}
		if aerr != nil {
			return applied, nil, nil, nil, aerr
		}
		applied += a
		if len(next) >= len(cur) || len(next) == 0 {
			cur = next
			curErrs = nextErrs
			break
		}
		cur = next
		curErrs = nextErrs
	}
	return applied, cur, curErrs, nil, nil
}

// writeBatch applies a batch of rows in one transaction, retrying without a
// refused entry and dropping the unresolvable remainder, with the mark moving in
// the same transaction.
func (i *Importer) writeBatch(batch, rows []Entry, docs, priorDrops int, marks map[string]int) (applied int, drops []Drop, refusal *entryRefusal, err error) {
	origin := batch[0].Origin
	var signedAt int
	var wrote bool
	err = i.db.Tx(func(tx *db.Tx) error {
		applied = 0
		drops = nil
		a, deferred, defErrs, ref, aerr := sweepEntries(tx, rows, i.transport, i.now())
		if ref != nil {
			refusal = ref
			return ref.err
		}
		if aerr != nil {
			return aerr
		}
		applied += a

		if len(deferred) > 0 {
			pa, rem, remErrs, pref, perr := retryPasses(tx, deferred, defErrs, orDefault(i.passes, maxSnapshotPasses), i.transport, i.now())
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

// applyEntries applies one batch's rows, rolling back and retrying without each
// ErrInvalid refusal; db.ErrInvalid entries retry in passes, then drop singly.
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

// applyEntry writes one entry and reports whether it took effect.
func applyEntry(tx *db.Tx, e Entry, transport LogTransport, now time.Time) (bool, error) {
	if IsCtlTable(e.Table) {
		if e.Table != CtlTakeover {
			return false, fmt.Errorf("synclog: import %s %s: unknown control entry: %w", e.Table, e.PK, ErrInvalid)
		}
		return true, applyTakeover(tx, e, transport, now)
	}
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
	dec, err := authorize(tx, e, values)
	if err != nil {
		return false, err
	}
	skip, err := historyEntry(e, values, now)
	if err != nil {
		return false, err
	}
	if skip {
		return true, nil
	}
	sinceSeq, err := tx.MaxOutboxSeq()
	if err != nil {
		return false, err
	}
	if e.Op == OpDelete {
		if err := tx.ExchangeDelete(e.Table, e.PK); err != nil {
			return false, fmt.Errorf("synclog: import %s %s: %w", e.Table, e.PK, err)
		}
		if err := tx.DiscardOutboxSince(e.Table, e.PK, sinceSeq); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := tx.ExchangeUpsert(e.Table, dec.owner, values); err != nil {
		return false, fmt.Errorf("synclog: import %s %s: %w", e.Table, e.PK, err)
	}
	if err := tx.DiscardOutboxSince(e.Table, e.PK, sinceSeq); err != nil {
		return false, err
	}
	return true, nil
}
