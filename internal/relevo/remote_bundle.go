package relevo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// fetchCatchUpBundle absorbs the round bundle and brings an adopted branch to
// the absorbed result, both without the lock. A discarded fetch leaves
// LastKnown unchanged, so the next catch-up asks for the same bundle again.
//
// A server branch rewritten behind the client's back no longer descends from
// the base this client holds, so the incremental request is refused. The
// fallback asks for the whole branch and re-bases the client's own mirror onto
// it, because Absorb only ever fast-forwards a ref the repo already carries.
func fetchCatchUpBundle(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView, cf *catchUpFetch) {
	n := view.ClosedRound
	server, name := b.Builder.Server, b.Name
	rcBundle, err := rt.Remote.RoundBundle(ctx, server, name, n, b.Builder.LastKnown)
	// Only the stale-base refusal earns the retry: the fallback force-moves the
	// client's own ref, which a mirror that is fine must not lose to a
	// transient failure.
	rewritten := err != nil && b.Builder.LastKnown != "" && sinceStale(err)
	if rewritten {
		slog.Warn("round base was rewritten; fetching the whole branch", "server", server, "name", name, "round", n)
		rcBundle, err = rt.Remote.RoundBundle(ctx, server, name, n, "")
	}
	if err != nil {
		slog.Warn("fetch round bundle failed", "server", server, "name", name, "round", n, "err", err)
		cf.Abort = true
		cf.release()
		return
	}
	if rcBundle == nil {
		return
	}
	if rewritten && holdsRoundMirror(b, name) {
		serverRef := "refs/heads/relevo/" + name
		if rerr := resetRoundBase(ctx, rt, b, serverRef); rerr != nil {
			_ = rcBundle.Close()
			if checkedOut(rerr) {
				warnCheckedOut(name, b.Branch)
				cf.CheckedOut = true
				return
			}
			cf.AbsorbErr = fmt.Errorf("%s: cannot re-base %s for round %d from %s: %s", name, b.Branch, n, server, rerr.Error())
			return
		}
	}
	absorbCatchUpBundle(ctx, rt, b, view, cf, rcBundle)
}

// holdsRoundMirror reports whether the binding's branch is the client's own
// mirror of the server's branch rather than a branch relevo did not cut: an
// adopted binding keeps its own name, and force-moving the mastermind's branch
// is not the fetch's call, so its rewritten base stays an absorb failure.
func holdsRoundMirror(b store.Binding, name string) bool {
	branchRef := b.Branch
	if !strings.HasPrefix(branchRef, "refs/heads/") {
		branchRef = "refs/heads/" + branchRef
	}
	return branchRef == "refs/heads/relevo/"+name
}

// sinceStale reports whether err is the server's refusal to cut an incremental
// bundle, which it gives when the base this client holds is no longer an
// ancestor of the branch the round produced.
func sinceStale(err error) bool {
	var httpErr *client.HTTPError
	return errors.As(err, &httpErr) &&
		httpErr.Status == 422 &&
		httpErr.Body.Code == remote.CodeNotFastForward
}

// resetRoundBase removes the client's own mirror of the server's branch so the
// rewritten history can land as a new one. git refuses this for a branch any
// worktree holds; the caller reads that refusal.
func resetRoundBase(ctx context.Context, rt Runtime, b store.Binding, serverRef string) error {
	return rt.Git.DeleteBranch(ctx, b.Repo, serverRef)
}

// absorbCatchUpBundle applies the open bundle to the binding's repo: the
// server's own ref through Absorb, then the adopted branch fast-forwarded onto
// the result. rcBundle's caller owns the request; this consumes the stream.
func absorbCatchUpBundle(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView, cf *catchUpFetch, rcBundle io.ReadCloser) {
	defer rcBundle.Close()
	n := view.ClosedRound
	server, name := b.Builder.Server, b.Name

	branchRef := b.Branch
	if !strings.HasPrefix(branchRef, "refs/heads/") {
		branchRef = "refs/heads/" + branchRef
	}
	// The server always cuts its own relevo/<name> branch and ships that, so a
	// binding that adopted a branch keeps the adopted name locally: the
	// allow-list names the server's ref, and the adopted branch is
	// fast-forwarded to the absorbed result below.
	serverRef := "refs/heads/relevo/" + name
	refs := []string{serverRef}
	if view.DirtyCommit != "" {
		refs = append(refs, fmt.Sprintf("refs/relevo/%s/round-%d", name, n))
	}
	if _, err := rt.Transport.Absorb(ctx, b.Repo, remote.ContentTypeGitBundle, rcBundle, refs); err != nil {
		if checkedOut(err) {
			warnCheckedOut(name, b.Branch)
			cf.CheckedOut = true
			return
		}
		cf.AbsorbErr = fmt.Errorf("%s: cannot absorb round %d from %s: %s", name, n, server, err.Error())
		return
	}
	checkedOutWarned.Delete(name)

	if serverRef == branchRef {
		return
	}
	sha, ok, err := rt.Git.RefSHA(ctx, b.Repo, serverRef)
	if err != nil {
		cf.Fatal = fmt.Errorf("resolve server branch %s after absorb: %w", serverRef, err)
		return
	}
	if !ok {
		cf.Fatal = fmt.Errorf("server branch %s missing after absorb", serverRef)
		return
	}
	old, _, _ := rt.Git.RefSHA(ctx, b.Repo, branchRef)
	if err := rt.Git.UpdateRef(ctx, b.Repo, branchRef, sha, old); err != nil {
		if checkedOut(err) {
			warnCheckedOut(name, b.Branch)
			cf.CheckedOut = true
			return
		}
		cf.AbsorbErr = fmt.Errorf("%s: cannot fast-forward %s to round %d from %s: %s", name, b.Branch, n, server, err.Error())
		return
	}
}
