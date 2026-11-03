package serve

import (
	"context"
	"log/slog"
	"os"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// closeForwardedRound records a round a node ran as closed on the head. The
// close facts come from what the node sent back -- the absorbed branch tip
// and, when the node kept uncommitted work, its side ref -- never from the
// head worktree: the round did not run there, so that tree still holds the
// state before the round, and reading it as dirt would record the node's
// work as undone. The stale worktree is removed for the same reason; the
// next served round checks it out again from the branch. Anything an
// earlier local round left uncommitted there is already kept in that
// round's side ref.
func closeForwardedRound(ctx context.Context, rt relevo.Runtime, b *store.Binding, fwd store.Forward, nodeDirty bool) bool {
	if b.Serve == nil || b.Branch == "" {
		return false
	}
	head, ok, err := rt.Git.RefSHA(ctx, b.Serve.BareRepo, "refs/heads/"+b.Branch)
	if err != nil || !ok {
		slog.Warn("burst: forwarded close: branch missing", "binding", b.Name, "branch", b.Branch, "err", err)
		return false
	}
	dirty := ""
	if nodeDirty {
		side := "refs/relevo/" + b.Name + "/round-" + strconv.Itoa(fwd.Round)
		sha, ok, err := rt.Git.RefSHA(ctx, b.Serve.BareRepo, side)
		if err != nil || !ok {
			slog.Warn("burst: forwarded close: side ref missing", "binding", b.Name, "ref", side, "err", err)
			return false
		}
		dirty = sha
	}
	if b.Worktree != "" {
		if _, statErr := os.Stat(b.Worktree); statErr == nil {
			if err := rt.Git.RemoveWorktree(ctx, b.Serve.BareRepo, b.Worktree, true); err != nil {
				slog.Warn("burst: forwarded close: stale worktree not removed", "binding", b.Name, "err", err)
				return false
			}
		}
	}
	b.Serve.ClosedRound = fwd.Round
	b.Serve.ResultCommit = head
	b.Serve.DirtyCommit = dirty
	return true
}
