package sync

// Draining one origin's page at a time. The importer reads one page per origin
// per run, so a backlog larger than a page is drained by running Import again
// while a run moved a mark; a run that moved nothing -- an empty pull, a held
// origin, a gap -- stops the loop rather than re-reading the same entries.
//
// The two pipelines share the loop because they share the predicate, and the
// only difference is what bounds a step: the join has no tick to keep, the
// steady pipeline runs each Import under the step bound it already had.

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// drainImport runs step in a loop until a run moves no mark or the loop's own
// deadline passes. The marks a run moved stand and the loop's deadline is not an
// error: it exists so a backlog larger than one whole transfer stops a tick
// rather than holding it, and the next tick continues from where this one
// stopped.
func drainImport(step func() (synclog.ImportResult, error), deadline time.Time, now func() time.Time) (synclog.ImportResult, error) {
	var total synclog.ImportResult
	for {
		res, err := step()
		if err != nil {
			return total, err
		}
		mergeImport(&total, res)
		if !res.Moved() || !now().Before(deadline) {
			return total, nil
		}
	}
}

// mergeImport folds one run's report into the loop's running total. The counts
// add because they measure the whole drain; the trouble appends because each
// entry names an origin and a sequence a caller has to act on, and a drain that
// reported only its last page's trouble would hide the rest.
func mergeImport(total *synclog.ImportResult, res synclog.ImportResult) {
	total.Batches += res.Batches
	total.Applied += res.Applied
	total.Held = append(total.Held, res.Held...)
	total.Dropped = append(total.Dropped, res.Dropped...)
	total.Gaps = append(total.Gaps, res.Gaps...)
	total.Stalled = append(total.Stalled, res.Stalled...)
}
