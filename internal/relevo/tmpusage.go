package relevo

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// TmpUsage is the reading of one bounded walk over a tmp root: the
// regular-file bytes under rounds/, under checks/, and under the loose
// entries, plus a flag saying the walk stopped before it finished. A
// truncated total is a floor, never an exact figure.
type TmpUsage struct {
	Rounds    int64
	Checks    int64
	Loose     int64
	Truncated bool
}

// tmpUsageWalk carries the two budgets and the running totals, so the
// recursive measure reads as one loop over entries instead of a growing
// argument list.
type tmpUsageWalk struct {
	usage      TmpUsage
	deadline   time.Time
	maxEntries int
	seen       int
}

// TmpUsageOf measures the tmp root under a time budget and an entry budget;
// whichever runs out first ends the walk and flags the result truncated.
// maxEntries <= 0 means the walk is bounded by time alone. Symlinks are never
// followed -- a root is a tenant's space whose links can name anything
// outside it -- so only bytes physically under the root are counted, and a
// link is neither descended into nor sized.
func TmpUsageOf(root string, budget time.Duration, maxEntries int) TmpUsage {
	w := tmpUsageWalk{deadline: time.Now().Add(budget), maxEntries: maxEntries}
	entries, err := os.ReadDir(root)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			w.usage.Truncated = true
		}
		return w.usage
	}
	for _, e := range entries {
		if !w.spend() {
			break
		}
		w.measure(w.bucket(e.Name()), filepath.Join(root, e.Name()), e)
	}
	return w.usage
}

// bucket routes a top-level entry's bytes by the two names the per-process
// routes make and the sweep's rules own; anything else is loose.
func (w *tmpUsageWalk) bucket(name string) *int64 {
	switch name {
	case tmpSubRounds:
		return &w.usage.Rounds
	case tmpSubChecks:
		return &w.usage.Checks
	default:
		return &w.usage.Loose
	}
}

// spend consumes one entry of the walk's budgets and reports whether the
// entry may be looked at: either budget running out truncates the result,
// and every later spend agrees, so a truncated total never grows again.
func (w *tmpUsageWalk) spend() bool {
	if !time.Now().Before(w.deadline) {
		w.usage.Truncated = true
		return false
	}
	if w.maxEntries > 0 && w.seen >= w.maxEntries {
		w.usage.Truncated = true
		return false
	}
	w.seen++
	return true
}

// measure adds one entry to its bucket: a regular file's bytes, or a whole
// directory walked under the same budgets. An unreadable directory truncates
// too, because the bytes below it are then unknown and the totals are floors.
// A symlink reaches neither branch: it is neither a directory to descend nor
// a regular file to size, and its target is not this root's to count.
func (w *tmpUsageWalk) measure(to *int64, path string, e fs.DirEntry) {
	switch {
	case e.IsDir():
		entries, err := os.ReadDir(path)
		if err != nil {
			w.usage.Truncated = true
			return
		}
		for _, c := range entries {
			if !w.spend() {
				return
			}
			w.measure(to, filepath.Join(path, c.Name()), c)
		}
	case e.Type().IsRegular():
		info, err := e.Info()
		if err != nil {
			w.usage.Truncated = true
			return
		}
		*to += info.Size()
	}
}
