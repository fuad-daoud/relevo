package synclog

import (
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The three rules that sit beside the generic importer for the append-only
// tables. Everything else the importer does -- the owner gate, the ordering, the
// mark -- is the same for a history row as for any other shared row, and stays
// here unchanged. These three are what makes a history row a history row.
//
// A row of availability_event or latency_sample is never updated and is deleted
// only by age, and the delete is never recorded. So:
//
//   - A delete naming one of those tables is refused. It cannot have come from a
//     machine this one trusts, because the only delete that table ever performs
//     is the age prune and the age prune reaches the outbox through no trigger at
//     all. Refusing it is the existing drop path: the batch is reported and moved
//     past, exactly as an entry naming another installation's row is.
//   - An upsert of a row already older than the window is skipped rather than
//     written. The mark still moves in the same transaction, so the row is not
//     offered again and never reaches this machine's table to be exported from.
//   - An upsert of a row whose time this machine cannot read, or cannot place,
//     is refused. A body that names no at_ms, names one that is not a whole
//     number, or names one past now plus the clock-skew allowance is naming a
//     row the age prune can never reach, and writing it would pin a row in
//     every machine's table for the life of the file.
//
// The second is what a joiner needs. A peer that was away for a month hands back
// a batch carrying its whole window; writing every row of it and then pruning
// would put thirty days of history through this machine's outbox and into the log
// on the way in, and a row this machine then deleted would be a row it had just
// exported.

// errHistoryDelete reports a delete naming a row of an append-only table. It is
// an ErrInvalid refusal under a second name, so the drop path can tell it apart
// from a body it merely does not understand; both are moved past.
var errHistoryDelete = errors.New("a history row is deleted only by age, and that delete never travels")

// errHistoryAt reports an arriving history row whose time this machine cannot
// place: no at_ms at all, one that is not a whole number, or one further ahead
// than the clock-skew allowance admits. It is an ErrInvalid refusal like
// errHistoryDelete, and is moved past on the same drop path.
//
// These three are refusals rather than writes because the age prune can never
// reach such a row. It is one comparison, `at_ms < cutoff`, against an integer
// column; a row whose at_ms is text sorts above every number in the file and
// never matches it, and a row far enough ahead simply never ages. A machine that
// wrote one would hold it for the life of the file, and every machine that
// imported it would hold one too -- so a body may not choose a time the
// retention rule cannot act on.
var errHistoryAt = errors.New("a history row's time is missing, not a whole number, or ahead of this machine's clock")

// historyEntry applies the two rules to one arriving entry and reports whether
// the importer may write it. values is the decoded body, which is nil for a
// delete.
//
// The window is judged against now, the same clock the age prune deletes against
// and the same one the reconciler proposes against, so a row on either side of
// it here is on the same side of it there. A row that arrives old is one this
// machine would have deleted on sight, so it is skipped rather than written and
// then deleted; a row whose time cannot be read, or that names a time further
// ahead than the clock-skew allowance, is refused outright.
func historyEntry(e Entry, values map[string]any, now time.Time) (skip bool, err error) {
	if !db.AppendOnly(e.Table) {
		return false, nil
	}
	if e.Op == OpDelete {
		return false, fmt.Errorf("synclog: import %s %s: %w: %w",
			e.Table, e.PK, errHistoryDelete, ErrInvalid)
	}
	at, ok := db.HistoryAt(e.Table, values)
	if !ok {
		return false, fmt.Errorf("synclog: import %s %s: %w: %w",
			e.Table, e.PK, errHistoryAt, ErrInvalid)
	}
	if at > db.HistoryFutureMS(now) {
		return false, fmt.Errorf("synclog: import %s %s: at_ms %d is past now plus %s: %w: %w",
			e.Table, e.PK, at, db.HistoryClockSkew, errHistoryAt, ErrInvalid)
	}
	if at >= db.UnixMS(now.Add(-db.HistoryWindow)) {
		return false, nil
	}
	return true, nil
}
