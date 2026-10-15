package synclog

import (
	"github.com/fuad-daoud/relevo/internal/db"
)

// The two rules that keep an age delete out of the reconciler. Everything else
// the reconciler does with a shared row -- comparing body hashes, paging the
// file, walking head children-first -- is unchanged for a history row; these two
// say what an aged one is not.
//
// A row of availability_event or latency_sample is deleted only by age, and that
// delete is never recorded, so it never reaches the log and never comes back. A
// head row of one of those tables that this file does not hold is therefore
// always a row that aged out -- here, or on the machine that wrote it and has
// since pruned it too -- and never one this walk may remove. And a local row that
// has aged out is not a row the log is missing: re-appending it would put a
// thirty-day-old observation back into every other machine's table, where it
// would be skipped on arrival and gone again by the next read.
//
// The window is judged at the same clock the age prune deletes against, so a row
// on one side of it here is on the same side of it in the table.

// historyCutoffMS is the instant before which a history row is out of window.
func (r *Reconciler) historyCutoffMS() int64 {
	return db.UnixMS(r.now().Add(-db.HistoryWindow))
}

// historyRowAged reports whether one row the walk read has aged out of the
// retention window. A row of any other table is never aged by this rule, and a
// history row the walk read without a time is not judged here: the column is
// NOT NULL at write time, so a page without one is a fake rather than a row.
func historyRowAged(tbl string, row db.ExchangeRow, cutoffMS int64) bool {
	at, ok := db.HistoryAtMS(row)
	if !db.AppendOnly(tbl) || !ok {
		return false
	}
	return at < cutoffMS
}
