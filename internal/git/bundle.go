package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CommitTree creates a commit object directly from a tree and parent commit,
// with the fixed relevo author and committer identity. commit-tree never signs
// unless -S is given, so a global commit.gpgsign does not apply.
func (c *Client) CommitTree(ctx context.Context, dir, tree, parent, message string) (string, error) {
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	args = append(args, "-m", message)
	env := []string{
		"GIT_AUTHOR_NAME=relevo",
		"GIT_AUTHOR_EMAIL=relevo@localhost",
		"GIT_COMMITTER_NAME=relevo",
		"GIT_COMMITTER_EMAIL=relevo@localhost",
	}
	out, err := c.run(ctx, dir, env, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CommitAll stages the whole working tree and commits it. It passes -c
// commit.gpgsign=false so a global commit.gpgsign cannot make it reach for gpg.
// With nothing to commit it leaves the repository untouched and returns
// ("", nil).
func (c *Client) CommitAll(ctx context.Context, dir, message string) (string, error) {
	if _, err := c.run(ctx, dir, nil, "add", "-A"); err != nil {
		return "", err
	}

	env := []string{
		"GIT_AUTHOR_NAME=relevo",
		"GIT_AUTHOR_EMAIL=relevo@localhost",
		"GIT_COMMITTER_NAME=relevo",
		"GIT_COMMITTER_EMAIL=relevo@localhost",
	}
	if _, err := c.run(ctx, dir, env, "-c", "commit.gpgsign=false", "commit", "-q", "-m", message); err != nil {
		if strings.Contains(err.Error(), "nothing to commit") {
			return "", nil
		}
		return "", err
	}

	out, err := c.run(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// InitBare initializes a bare git repository at path. An existing bare repo is
// left as is.
func (c *Client) InitBare(ctx context.Context, path string) error {
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_, err := c.run(ctx, dir, nil, "init", "--bare", path)
	return err
}

// BundleCreate creates a git bundle file at path containing refs relative to
// since. When since is non-empty and every ref's head already equals it, no
// bundle is created and empty is true.
func (c *Client) BundleCreate(ctx context.Context, dir, path string, refs []string, since string) (map[string]string, bool, error) {
	heads := make(map[string]string, len(refs))
	for _, ref := range refs {
		sha, ok, err := c.RefSHA(ctx, dir, ref)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, fmt.Errorf("%w: %s", ErrRefMissing, ref)
		}
		heads[ref] = sha
	}

	if since != "" && len(heads) > 0 {
		allEqual := true
		for _, head := range heads {
			if head != since {
				allEqual = false
				break
			}
		}
		if allEqual {
			return heads, true, nil
		}
	}

	if since != "" {
		for _, ref := range refs {
			_, err := c.run(ctx, dir, nil, "merge-base", "--is-ancestor", since, ref)
			if err != nil {
				if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return nil, false, err
				}
				return nil, false, fmt.Errorf("%w: since is not an ancestor of %s", ErrRefMissing, ref)
			}
		}
	}

	args := []string{"bundle", "create", path}
	for _, ref := range refs {
		if since != "" {
			args = append(args, since+".."+ref)
		} else {
			args = append(args, ref)
		}
	}

	_, err := c.run(ctx, dir, nil, args...)
	if err != nil {
		return nil, false, err
	}
	return heads, false, nil
}

// BundleHeads lists the heads carried in the bundle file at path as ref name ->
// commit SHA.
func (c *Client) BundleHeads(ctx context.Context, dir, path string) (map[string]string, error) {
	out, err := c.run(ctx, dir, nil, "bundle", "list-heads", path)
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", ErrBadBundle, err)
	}

	heads := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return nil, fmt.Errorf("%w: malformed bundle head entry %q", ErrBadBundle, line)
		}
		heads[parts[1]] = parts[0]
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadBundle, err)
	}
	return heads, nil
}

// FetchBundle verifies the bundle file at path and fetches each ref in refs that
// the bundle carries.
//
// The fetch is one ref at a time, in refs order, with refspec <ref>:<ref>
// without a leading '+', so every update is a fast-forward. A failed update
// stops the loop and refs already moved stay moved: callers treat a partial
// absorb as retryable, and a re-run is idempotent because a ref already at its
// target SHA is a no-op. gc.autoDetach=false keeps an auto-gc inside this call
// instead of forking a process that outlives it and could race a later worktree
// removal.
func (c *Client) FetchBundle(ctx context.Context, dir, path string, refs []string) (map[string]string, error) {
	_, err := c.run(ctx, dir, nil, "bundle", "verify", path)
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", ErrBadBundle, err)
	}

	heads, err := c.BundleHeads(ctx, dir, path)
	if err != nil {
		return nil, err
	}

	fetched := make(map[string]string)
	for _, ref := range refs {
		sha, ok := heads[ref]
		if !ok {
			continue
		}
		_, err := c.run(ctx, dir, nil, "-c", "gc.autoDetach=false", "fetch", "--no-tags", path, ref+":"+ref)
		if err != nil {
			if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fetched, err
			}
			errStr := strings.ToLower(err.Error())
			if strings.Contains(errStr, "non-fast-forward") || strings.Contains(errStr, "[rejected]") {
				return fetched, ErrNotFastForward
			}
			return fetched, err
		}
		fetched[ref] = sha
	}
	return fetched, nil
}
