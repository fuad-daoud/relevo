package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

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
			_ = os.RemoveAll(absPath)
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
			_ = os.RemoveAll(absPath)
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
			_ = os.RemoveAll(absPath)
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

	// Already gone -- removed by hand, or by an earlier relevo done -- is
	// success: prune the stale entry so the branch is no longer "checked out"
	// there and can be deleted.
	if _, statErr := os.Stat(absPath); errors.Is(statErr, os.ErrNotExist) {
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

// WorktreeRepair repairs the administrative link between repo and a worktree
// whose directory was moved, so a plain rename of a state root leaves a working
// tree git still recognises.
func (c *Client) WorktreeRepair(ctx context.Context, repo, worktree string) error {
	_, err := c.run(ctx, repo, nil, "worktree", "repair", worktree)
	return err
}
