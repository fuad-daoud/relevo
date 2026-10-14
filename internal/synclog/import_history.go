package synclog

import (
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The two rules that sit beside the generic importer for the append-only tables.
// Everything else the importer does -- the owner gate, the ordering, the mark --
// is the same for a history row as for any other shared row, and stays here
// unchanged. These two are what makes a history row a history row.
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

// historyEntry applies the two rules to one arriving entry and reports whether
// the importer may write it. values is the decoded body, which is nil for a
// delete.
//
// The window is judged against now, the same clock the age prune deletes against,
// so a row that arrives old is one this machine would have deleted on sight: it
// is skipped rather than written and then deleted.
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
		return false, nil
	}
	if at >= db.UnixMS(now.Add(-db.HistoryWindow)) {
		return false, nil
	}
	return true, nil
}
