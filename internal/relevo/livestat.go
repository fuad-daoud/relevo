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

type liveStatEntry struct {
	at   time.Time
	diff view.LiveDiff
}

// liveStatCache is keyed by binding name + "@" + the round's baseline tree,
// package-level and shared by every caller in this process (#143's design:
// "cached for the poll interval"). A new round gets a new baseline, so the
// key changes with it and a stale round's entry is simply never looked up
// again -- there is nothing to invalidate.
var (
	liveStatMu    sync.Mutex
	liveStatCache = map[string]liveStatEntry{}
)

// liveStat is the live "+N/-M in F" a status row shows while a round is
// open (#143): b's working tree against the round's baseline, no patch
// body. nil when there is nothing to diff -- no Git wired, no round open,
// no recorded baseline, or no working tree -- or when the git read itself
// fails; a status row degrades rather than fails because of it.
func liveStat(ctx context.Context, rt Runtime, b store.Binding) *view.LiveDiff {
	if rt.Git == nil || b.RoundStartedAt.IsZero() || b.RoundBaselineTree == "" || b.CWD == "" {
		return nil
	}

	now := time.Now()
	if rt.Now != nil {
		now = rt.Now()
	}

	key := b.Name + "@" + b.RoundBaselineTree
	liveStatMu.Lock()
	if entry, ok := liveStatCache[key]; ok && now.Sub(entry.at) < liveStatCacheTTL {
		liveStatMu.Unlock()
		d := entry.diff
		return &d
	}
	liveStatMu.Unlock()

	stat, err := rt.Git.DiffWorktreeStat(ctx, b.CWD, b.RoundBaselineTree)
	if err != nil {
		return nil
	}

	d := view.LiveDiff{
		Files:   stat.FilesChanged,
		Added:   stat.Insertions,
		Removed: stat.Deletions,
		// A --cwd binding has no worktree of its own (#143 design question
		// 1): its "live" diff is against the mastermind's own tree, shared
		// with whatever else is running there.
		Shared: b.Worktree == "",
	}

	liveStatMu.Lock()
	liveStatCache[key] = liveStatEntry{at: now, diff: d}
	liveStatMu.Unlock()

	return &d
}
