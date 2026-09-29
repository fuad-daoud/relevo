package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// MergeFF fast-forwards dir's current branch to ref.
func (c *Client) MergeFF(ctx context.Context, dir, ref string) error {
	_, err := c.run(ctx, dir, nil, "merge", "--ff-only", ref)
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return err
		}
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "not possible to fast-forward") {
			return ErrNotFastForward
		}
		if strings.Contains(errStr, "would be overwritten") || strings.Contains(errStr, "local changes") {
			return ErrMergeConflict
		}
		return err
	}
	return nil
}

// Fetch fetches ref from remote. git opportunistically updates the matching
// remote-tracking ref when the remote has a fetch refspec, which is what makes
// "origin/<base>" resolvable afterwards.
func (c *Client) Fetch(ctx context.Context, dir, remote, ref string) error {
	_, err := c.run(ctx, dir, nil, "fetch", remote, ref)
	return err
}

// Rebase rebases dir's current branch onto onto. On a conflict the unmerged
// paths are returned, the rebase is aborted, and the worktree and HEAD are
// exactly where they were.
func (c *Client) Rebase(ctx context.Context, dir, onto string) ([]string, error) {
	_, err := c.run(ctx, dir, nil, "rebase", onto)
	if err == nil {
		return nil, nil
	}
	if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, err
	}

	paths, perr := c.unmergedPaths(ctx, dir)
	if perr != nil {
		return nil, perr
	}
	// No unmerged path means this was not a conflict at all -- an unresolvable
	// onto, a missing committer identity, a dirty index -- so the original
	// failure is the honest answer. The abort still runs: git may have left a
	// rebase in progress before failing.
	if len(paths) == 0 {
		_, _ = c.run(ctx, dir, nil, "rebase", "--abort")
		return nil, err
	}
	if _, aerr := c.run(ctx, dir, nil, "rebase", "--abort"); aerr != nil {
		return nil, fmt.Errorf("%w: rebase %s: %w; and rebase --abort failed: %w", ErrMergeConflict, onto, err, aerr)
	}
	return paths, ErrMergeConflict
}

// Merge merges ref into dir's current branch. It is the --merge escape hatch:
// it integrates the base without rewriting the branch, so the push that follows
// needs no lease. On a conflict the unmerged paths are returned and the merge is
// aborted, leaving the worktree and HEAD where they were.
func (c *Client) Merge(ctx context.Context, dir, ref string) ([]string, error) {
	_, err := c.run(ctx, dir, nil, "merge", "--no-edit", ref)
	if err == nil {
		return nil, nil
	}
	if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, err
	}

	paths, perr := c.unmergedPaths(ctx, dir)
	if perr != nil {
		return nil, perr
	}
	// As in Rebase: no unmerged path means the failure was not a conflict, so
	// the original error is returned instead of inventing one.
	if len(paths) == 0 {
		_, _ = c.run(ctx, dir, nil, "merge", "--abort")
		return nil, err
	}
	if _, aerr := c.run(ctx, dir, nil, "merge", "--abort"); aerr != nil {
		return nil, fmt.Errorf("%w: merge %s: %w; and merge --abort failed: %w", ErrMergeConflict, ref, err, aerr)
	}
	return paths, ErrMergeConflict
}

// unmergedPaths lists the paths git marked unmerged, the conflict set Rebase and
// Merge return.
func (c *Client) unmergedPaths(ctx context.Context, dir string) ([]string, error) {
	out, err := c.run(ctx, dir, nil, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimRight(line, "\r"); strings.TrimSpace(line) != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

// Push pushes branch to remote and sets it as branch's upstream. A rebase
// rewrites the branch and so needs --force-with-lease; --merge does not rewrite
// it and does not.
func (c *Client) Push(ctx context.Context, dir, remote, branch string, forceWithLease bool) error {
	args := []string{"push", "-u", remote, branch}
	if forceWithLease {
		args = append(args, "--force-with-lease")
	}
	_, err := c.run(ctx, dir, nil, args...)
	return err
}

// RemoteBranchExists reports whether remote already has a branch named branch.
func (c *Client) RemoteBranchExists(ctx context.Context, dir, remote, branch string) (bool, error) {
	out, err := c.run(ctx, dir, nil, "ls-remote", "--heads", remote, branch)
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}
