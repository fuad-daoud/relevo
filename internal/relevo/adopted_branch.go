package relevo

import (
	"context"
	"fmt"
)

// resolveAdoptedBranch resolves the branch `bind --branch` adopts and returns
// its tip, so the local path and the --server path share one answer and one
// set of refusals.
//
// The order is the whole contract: the local branch first, then the origin
// cache, and only when neither has it one bounded fetch of exactly that ref.
// So a branch already resolvable costs no network at all, and a branch nobody
// has costs one fetch -- never a --all, never an extra ls-remote round trip.
// Both refusals that can remain, a fetch that failed and a fetch that left the
// branch absent, keep the original wording as a substring, name the manual
// recovery, and are classed as refusals rather than internal errors: nothing
// was written and a corrected invocation is the way on.
//
// It returns whether it created a local tracking branch, which the local path
// needs in order to explain a later checkout failure.
func resolveAdoptedBranch(ctx context.Context, rt Runtime, repo, branch string) (tip string, createdTracking bool, err error) {
	if err := branchDrivenByLiveBinding(rt, branch); err != nil {
		return "", false, err
	}
	exists, err := rt.Git.BranchExists(ctx, repo, branch)
	if err != nil {
		return "", false, err
	}
	if !exists {
		originRef := "origin/" + branch
		cached, err := originRefResolves(ctx, rt, repo, originRef)
		if err != nil {
			return "", false, err
		}
		if !cached {
			// Neither the local repo nor the cache has it: fetch exactly
			// this one ref, then re-read the cache the fetch just wrote.
			if ferr := rt.Git.Fetch(ctx, repo, "origin", branch); ferr != nil {
				return "", false, refuse("branch %q not found locally or on origin: fetching it failed (%v); run `git fetch origin %s` and retry", branch, ferr, branch)
			}
			if cached, err = originRefResolves(ctx, rt, repo, originRef); err != nil {
				return "", false, err
			}
		}
		if !cached {
			return "", false, refuse("branch %q not found locally or on origin: the fetch found no such branch; run `git fetch origin %s` and retry", branch, branch)
		}
		if err := rt.Git.CreateTrackingBranch(ctx, repo, branch, originRef); err != nil {
			return "", false, err
		}
		createdTracking = true
	}
	tip, ok, err := rt.Git.RefSHA(ctx, repo, "refs/heads/"+branch)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, fmt.Errorf("branch %q vanished", branch)
	}
	return tip, createdTracking, nil
}

// originRefResolves reports whether the remote-tracking ref originRef is
// present in the local cache. It is the stale-cache probe the two --branch
// paths have always made: nothing lists origin's refs, so each asks about
// the one ref it needs.
func originRefResolves(ctx context.Context, rt Runtime, repo, originRef string) (ok bool, err error) {
	_, ok, err = rt.Git.RefSHA(ctx, repo, "refs/remotes/"+originRef)
	return ok, err
}
