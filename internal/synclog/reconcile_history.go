package synclog

import (
	"github.com/fuad-daoud/relevo/internal/db"
)

// The two rules keeping an age delete out of the reconciler. A head row of an
// append-only table missing here always aged out, here or on its writer, never a
// row this walk may remove: the delete is never recorded. A locally aged-out row
// is not missing from the log either: re-appending would put a thirty-day-old
// observation into every table, skipped on arrival. The window is judged at the
// prune's own clock.

func (r *Reconciler) historyCutoffMS() int64 {
	return db.UnixMS(r.now().Add(-db.HistoryWindow))
}

// historyRowAged reports whether one walked row aged out of retention. Other
// tables never age here, and a history row without a time is not judged: the
// column is NOT NULL at write, so a page without one is a fake.
func historyRowAged(tbl string, row db.ExchangeRow, cutoffMS int64) bool {
	at, ok := db.HistoryAtMS(row)
	if !db.AppendOnly(tbl) || !ok {
		return false
	}
	return at < cutoffMS
}
