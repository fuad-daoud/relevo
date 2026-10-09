package relevo

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// liveStatRuntime is a runtime whose fake git answers every live diff read with
// the same four files, forty added and eight removed.
func liveStatRuntime(t *testing.T) (Runtime, *fakeGit) {
	t.Helper()
	rt := newRuntime(t)
	fg := &fakeGit{worktreeStat: git.Stat{FilesChanged: 4, Insertions: 40, Deletions: 8}}
	rt.Git = fg
	return rt, fg
}

// openRound is a binding mid-round with a baseline tree, which is what liveStat
// needs before it reads anything.
func openRound(name, cwd, worktree, tree string) store.Binding {
	return store.Binding{
		Name: name, CWD: cwd, Worktree: worktree,
		State: store.StateActive, Round: 2,
		RoundStartedAt: baseTime, RoundBaselineTree: tree,
	}
}

var wantLiveStat = view.LiveDiff{Files: 4, Added: 40, Removed: 8}

// TestLiveStatCacheIsKeyedOnCWDNotName pins what one live diff read actually
// decides: a working directory against a baseline tree. Two callers naming the
// same pair are one read, so keying the cache on the binding's name -- which
// names no input to the read -- made every second caller pay for a diff it had
// already been handed. A different tree is a different read.
func TestLiveStatCacheIsKeyedOnCWDNotName(t *testing.T) {
	rt, fg := liveStatRuntime(t)

	first := liveStat(context.Background(), rt, openRound("first", "/repo-shared", "/wt-shared", "tree-a"))
	second := liveStat(context.Background(), rt, openRound("second", "/repo-shared", "/wt-shared", "tree-a"))
	other := liveStat(context.Background(), rt, openRound("third", "/repo-shared", "/wt-shared", "tree-b"))

	for name, got := range map[string]*view.LiveDiff{"first": first, "second": second, "other": other} {
		if got == nil || *got != wantLiveStat {
			t.Errorf("%s Live = %+v, want %+v", name, got, wantLiveStat)
		}
	}
	if fg.worktreeStatCalls != 2 {
		t.Fatalf("worktreeStatCalls for two trees over two names on one worktree = %d, want 2", fg.worktreeStatCalls)
	}
	if fg.lastWorktreeStatDir != "/repo-shared" || fg.lastWorktreeStatTree != "tree-b" {
		t.Errorf("second read = %s@%s, want /repo-shared@tree-b", fg.lastWorktreeStatDir, fg.lastWorktreeStatTree)
	}
}

// TestLiveStatSharedIsPerBinding pins the one field of a live diff that is not
// a property of the diff: whether the binding owns a worktree or shares the
// directory's. Caching it under a worktree-wide key hands a --cwd binding a
// shared=false row read from a sibling that owns its own worktree, so it is
// filled in on the way out of the cache and never stored.
func TestLiveStatSharedIsPerBinding(t *testing.T) {
	rt, fg := liveStatRuntime(t)

	owned := liveStat(context.Background(), rt, openRound("owns-worktree", "/repo-shared-flag", "/wt-owned", "tree-a"))
	// Worktree left empty: this binding shares the directory's tree.
	shares := liveStat(context.Background(), rt, openRound("shares-cwd", "/repo-shared-flag", "", "tree-a"))

	if owned == nil || owned.Shared {
		t.Fatalf("owns-worktree Live = %+v, want non-nil Shared false", owned)
	}
	if shares == nil || !shares.Shared {
		t.Fatalf("shares-cwd Live = %+v, want non-nil Shared true", shares)
	}
	if fg.worktreeStatCalls != 1 {
		t.Fatalf("worktreeStatCalls for two bindings on one worktree = %d, want 1", fg.worktreeStatCalls)
	}

	// The cached entry itself carries no Shared, so a third reader resolves it
	// from its own binding rather than inheriting the first one's answer.
	third := liveStat(context.Background(), rt, openRound("third", "/repo-shared-flag", "", "tree-a"))
	if third == nil || !third.Shared {
		t.Fatalf("third Live = %+v, want non-nil Shared true", third)
	}
}

// TestLiveStatGivesUpOnAWedgedGit pins the deadline the live read carries: the
// detail pane opens a row and waits on this read, so a git that never answers
// costs the row its diff line, not the open. It pins the deadline itself too,
// since an unbounded read is the case this exists to prevent.
func TestLiveStatGivesUpOnAWedgedGit(t *testing.T) {
	rt, fg := liveStatRuntime(t)
	var hadDeadline bool
	fg.worktreeStatFunc = func(ctx context.Context, _, _ string) (git.Stat, error) {
		_, hadDeadline = ctx.Deadline()
		<-ctx.Done()
		return git.Stat{}, ctx.Err()
	}

	start := time.Now()
	got := liveStat(context.Background(), rt, openRound("wedged", "/repo-wedged", "/wt-wedged", "tree-a"))
	elapsed := time.Since(start)

	if !hadDeadline {
		t.Error("the live diff read carried no deadline")
	}
	if got != nil {
		t.Errorf("wedged Live = %+v, want nil", got)
	}
	if limit := 5 * liveDiffDeadline; elapsed > limit {
		t.Errorf("liveStat took %v past a %v deadline", elapsed, limit)
	}
}

// TestLiveStatDegradesOnAFailedRead is the not-a-repository case at the row's
// level: the git read reports the directory is no repository, and the row shows
// no diff rather than failing.
func TestLiveStatDegradesOnAFailedRead(t *testing.T) {
	rt, fg := liveStatRuntime(t)
	fg.worktreeStatErr = git.ErrNotRepo

	if got := liveStat(context.Background(), rt, openRound("plain", "/repo-plain", "/wt-plain", "tree-a")); got != nil {
		t.Fatalf("Live over a non-repository = %+v, want nil", got)
	}
	if fg.worktreeStatCalls != 1 {
		t.Fatalf("worktreeStatCalls = %d, want 1", fg.worktreeStatCalls)
	}
}
