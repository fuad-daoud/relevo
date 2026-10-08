package relevo

import (
	"context"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// liveStatCacheTTL bounds how long a cached live diff stat is reused. ui
// polls every 2s and would otherwise pay for a git diff on every tick;
// status is one-shot and still reads close to live within this window.
const liveStatCacheTTL = 5 * time.Second

// liveDiffDeadline bounds one live diff read, the git sibling of
// liveDeadline: the detail pane opens a row and waits on it, so a slow or
// wedged git shows no diff line rather than stalling the open.
const liveDiffDeadline = 500 * time.Millisecond

type liveStatEntry struct {
	at   time.Time
	diff view.LiveDiff
}

// liveStatCache is keyed by the binding's working directory + "@" + the
// round's baseline tree, package-level and shared by every caller in this
// process. The two halves are what the diff actually reads -- one worktree
// against one tree -- so two bindings sharing a worktree share an entry and
// one binding read twice in a window reads it once. The binding's name is not
// in the key because it names no input to the read: a new round gets a new
// baseline, so the key changes with it, and a stale round's entry is simply
// never looked up again -- there is nothing to invalidate.
//
// Shared is per binding, not per diff, so it is filled in on the way out and
// never cached: two bindings with one worktree can disagree on it.
var (
	liveStatMu    sync.Mutex
	liveStatCache = map[string]liveStatEntry{}
)

// liveStat is the live "+N/-M in F" a status row shows while a round is
// open: b's working tree against the round's baseline, no patch
// body. nil when there is nothing to diff -- no Git wired, no round open,
// no recorded baseline, or no working tree -- or when the git read itself
// fails, is refused, or runs past liveDiffDeadline; a status row degrades
// rather than fails because of it.
func liveStat(ctx context.Context, rt Runtime, b store.Binding) *view.LiveDiff {
	if rt.Git == nil || b.RoundStartedAt.IsZero() || b.RoundBaselineTree == "" || b.CWD == "" {
		return nil
	}

	now := time.Now()
	if rt.Now != nil {
		now = rt.Now()
	}

	key := b.CWD + "@" + b.RoundBaselineTree
	liveStatMu.Lock()
	if entry, ok := liveStatCache[key]; ok && now.Sub(entry.at) < liveStatCacheTTL {
		liveStatMu.Unlock()
		d := entry.diff
		// A --cwd binding has no worktree of its own: its "live" diff is
		// against the worktree's own tree, shared with whatever else is
		// running there.
		d.Shared = b.Worktree == ""
		return &d
	}
	liveStatMu.Unlock()

	dctx, cancel := context.WithTimeout(ctx, liveDiffDeadline)
	defer cancel()
	stat, err := rt.Git.DiffWorktreeStat(dctx, b.CWD, b.RoundBaselineTree)
	if err != nil {
		return nil
	}

	d := view.LiveDiff{
		Files:   stat.FilesChanged,
		Added:   stat.Insertions,
		Removed: stat.Deletions,
		Shared:  b.Worktree == "",
	}

	liveStatMu.Lock()
	liveStatCache[key] = liveStatEntry{at: now, diff: view.LiveDiff{
		Files: d.Files, Added: d.Added, Removed: d.Removed,
	}}
	liveStatMu.Unlock()

	return &d
}
