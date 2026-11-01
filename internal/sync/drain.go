package sync

// Draining one origin's page at a time: a backlog larger than a page is drained
// by running Import again while a run moved a mark, and a run that moved
// nothing stops the loop rather than re-reading the same entries. The two
// pipelines share the loop because they share the predicate; only the step
// bound differs, since the join has no tick to keep.

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// drainImport runs step until a run moves no mark or the loop's deadline passes.
// The deadline is not an error: a backlog larger than one whole transfer stops
// a tick rather than holding it, and the next tick continues where this one
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
