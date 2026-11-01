package synclog

import (
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The three rules beside the generic importer for append-only tables. Deletes are
// refused: the only delete is the unrecorded age prune, so a delete cannot come
// from a trusted machine. Upserts older than the window are skipped with the mark
// still moving, so a month-away peer's window does not churn through this
// machine's outbox and log. Upserts with missing, non-whole or
// future-past-allowance times are refused: the age prune, one comparison of at_ms
// against an integer column, can never reach such rows, and writing one would pin
// it in every machine's table for the life of the file.

// errHistoryDelete refuses a delete of an append-only row. It is ErrInvalid under
// a second name, so the drop path tells it apart from an ununderstood body.
var errHistoryDelete = errors.New("a history row is deleted only by age, and that delete never travels")

// errHistoryAt refuses an arriving history row whose time cannot be placed:
// missing, non-whole, or past the skew allowance. Like errHistoryDelete it is
// moved past on the drop path.
var errHistoryAt = errors.New("a history row's time is missing, not a whole number, or ahead of this machine's clock")

// historyEntry applies the append-only rules to one arriving entry; values is the
// decoded body, nil for a delete. The window is judged against now, the same
// clock the prune and reconciler use.
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
