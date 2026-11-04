package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// clearWorktreeRegistration drops the admin entry and directory of a worktree
// whose add failed or never finished. A killed add leaves the entry locked,
// which prune skips; removing with double force is what clears a locked
// entry, while the half-checked-out directory still falls to os.RemoveAll.
// Cleanup never reports: the caller already failed, and a lost temp
// directory must not replace its error.
func (c *Client) clearWorktreeRegistration(ctx context.Context, dir, absPath string) {
	// A request-scoped deadline firing mid-checkout reaches cleanup with an
	// already-expired context; detaching lets each child still run under the
	// client's own timeout instead of dying before it starts.
	ctx = context.WithoutCancel(ctx)
	_, _ = c.run(ctx, dir, nil, "worktree", "remove", "--force", "--force", absPath)
	_ = os.RemoveAll(absPath)
}

// AddWorktree creates a worktree at path, checking out a NEW branch at commit.
// On any error nothing is left behind at path.
func (c *Client) AddWorktree(ctx context.Context, dir, path, branch, commit string) (retErr error) {
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(dir, absPath)
	}

	pathExisted := false
	if _, err := os.Stat(absPath); err == nil {
		pathExisted = true
	}
	defer func() {
		if retErr != nil && !pathExisted {
			c.clearWorktreeRegistration(ctx, dir, absPath)
			_, _ = c.run(ctx, dir, nil, "worktree", "prune")
		}
	}()

	branchName := strings.TrimPrefix(branch, "refs/heads/")

	exists, err := c.BranchExists(ctx, dir, branchName)
	if err != nil {
		return err
	}
	if exists {
		return ErrBranchExists
	}

	_, err = c.run(ctx, dir, nil, "worktree", "add", "-b", branchName, absPath, commit)
	if err != nil {
		// git's wording for an existing branch varies by version, so match on
		// the substrings that have stayed stable.
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "branch named") ||
			(strings.Contains(errStr, "branch") && strings.Contains(errStr, "already exists")) ||
			strings.Contains(errStr, "is already checked out") {
			return ErrBranchExists
		}
		// The add creates the branch before checking out, so a kill leaves a
		// branch a retry would refuse. A present branch is ours: the check
		// above refused a pre-existing one, and the race maps to
		// ErrBranchExists. Clear the entry first — git refuses to delete a
		// branch still checked out in a worktree — then the branch; the
		// deferred cleanup repeats the clearing harmlessly. The branch check and
		// delete are cleanup under the same deadline hazard, so they share one
		// detached context with the clear.
		cctx := context.WithoutCancel(ctx)
		c.clearWorktreeRegistration(cctx, dir, absPath)
		if existsNow, berr := c.BranchExists(cctx, dir, branchName); berr == nil && existsNow {
			_ = c.DeleteBranch(cctx, dir, branchName)
		}
		return err
	}

	return nil
}

// AddDetachedWorktree creates a worktree at path with a DETACHED HEAD at commit.
// It creates no branch, so it leaves no new ref in the repository. On any error
// nothing is left behind at path.
func (c *Client) AddDetachedWorktree(ctx context.Context, dir, path, commit string) (retErr error) {
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(dir, absPath)
	}

	pathExisted := false
	if _, err := os.Stat(absPath); err == nil {
		pathExisted = true
	}
	defer func() {
		if retErr != nil && !pathExisted {
			c.clearWorktreeRegistration(ctx, dir, absPath)
			_, _ = c.run(ctx, dir, nil, "worktree", "prune")
		}
	}()

	_, err := c.run(ctx, dir, nil, "worktree", "add", "--detach", absPath, commit)
	if err != nil {
		return err
	}

	return nil
}

// CheckoutWorktree adds a worktree at path on an existing branch. On any error
// nothing is left behind at path.
func (c *Client) CheckoutWorktree(ctx context.Context, dir, path, branch string) (retErr error) {
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(dir, absPath)
	}

	pathExisted := false
	if _, err := os.Stat(absPath); err == nil {
		pathExisted = true
	}
	defer func() {
		if retErr != nil && !pathExisted {
			c.clearWorktreeRegistration(ctx, dir, absPath)
			_, _ = c.run(ctx, dir, nil, "worktree", "prune")
		}
	}()

	branchName := strings.TrimPrefix(branch, "refs/heads/")

	_, err := c.run(ctx, dir, nil, "worktree", "add", absPath, branchName)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "is already checked out") ||
			strings.Contains(errStr, "is already used by worktree") {
			return ErrBranchCheckedOut
		}
		return err
	}

	return nil
}

// RemoveWorktree removes a worktree and prunes its administrative entry. It
// never removes the branch: a branch holds commits, and commits are work. A path
// that is already gone is success.
func (c *Client) RemoveWorktree(ctx context.Context, dir, path string, force bool) error {
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(dir, absPath)
	}

	// Already gone is success: prune the stale entry so the branch is no longer
	// "checked out" there and can be deleted. A killed add leaves the entry
	// locked, which prune skips, so clear it the way a failed add does first.
	if _, statErr := os.Stat(absPath); errors.Is(statErr, os.ErrNotExist) {
		c.clearWorktreeRegistration(ctx, dir, absPath)
		if _, err := c.run(ctx, dir, nil, "worktree", "prune"); errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) {
			return err
		}
		return nil
	}

	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, absPath)

	_, err := c.run(ctx, dir, nil, args...)
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return err
		}
		errStr := strings.ToLower(err.Error())
		if !force && (strings.Contains(errStr, "contains modified or untracked files") ||
			strings.Contains(errStr, "uncommitted changes") ||
			strings.Contains(errStr, "use --force")) {
			return ErrWorktreeDirty
		}
		if !force {
			if dirty, dirtyErr := c.Dirty(ctx, absPath); dirtyErr == nil && dirty {
				return ErrWorktreeDirty
			}
		}
		return err
	}
	return nil
}

// WorktreeRepair repairs the link between repo and a moved worktree, so a plain
// rename of a state root leaves a tree git still recognises.
func (c *Client) WorktreeRepair(ctx context.Context, repo, worktree string) error {
	_, err := c.run(ctx, repo, nil, "worktree", "repair", worktree)
	return err
}
