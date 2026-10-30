package serve

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// CloseServedRound runs once per closed round on an owned binding, inside
// reconcileHeadless after closeOnMarker reports closed and before
// clearProcess. It never fails the close: any git error is a slog.Warn and
// the facts stay at their previous values, so the owner's next GET shows the
// round still running until the next tick retries.
func CloseServedRound(ctx context.Context, rt relevo.Runtime, b store.Binding) store.Binding {
	if b.Owner == "" || b.Serve == nil {
		return b
	}
	closed := b.Round - 1
	// A served reader that shares the builder's tree carries no branch and no
	// worktree of its own: there is nothing to resolve and nothing to commit,
	// so the close records the round with no git facts.
	if b.Branch == "" {
		b.Serve.ClosedRound = closed
		b.Serve.ResultCommit = ""
		b.Serve.DirtyCommit = ""
		return b
	}
	head, ok, err := rt.Git.RefSHA(ctx, b.Serve.BareRepo, "refs/heads/"+b.Branch)
	if err != nil || !ok {
		slog.Warn("branch missing at close", "binding", b.Name, "branch", b.Branch, "err", err)
		return b
	}
	dirty, err := rt.Git.Dirty(ctx, b.Worktree)
	if err != nil {
		slog.Warn("dirty check failed at close", "binding", b.Name, "err", err)
		return b
	}
	dirtyCommit := ""
	if dirty {
		tree, err := rt.Git.SnapshotTree(ctx, b.Worktree)
		if err != nil {
			slog.Warn("snapshot tree failed at close", "binding", b.Name, "err", err)
			return b
		}
		sha, err := rt.Git.CommitTree(ctx, b.Serve.BareRepo, tree, head,
			fmt.Sprintf("[relevo] %s: round %d, uncommitted work", b.Name, closed))
		if err != nil {
			slog.Warn("commit tree failed at close", "binding", b.Name, "err", err)
			return b
		}
		if err := rt.Git.UpdateRef(ctx, b.Serve.BareRepo, fmt.Sprintf("refs/relevo/%s/round-%d", b.Name, closed), sha, ""); err != nil {
			slog.Warn("update ref failed at close", "binding", b.Name, "err", err)
			return b
		}
		dirtyCommit = sha
	}
	b.Serve.ClosedRound = closed
	b.Serve.ResultCommit = head
	b.Serve.DirtyCommit = dirtyCommit
	return b
}
