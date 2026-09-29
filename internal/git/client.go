// Package git runs the git CLI for relevo: tree snapshots, diffs, worktrees,
// refs, bundles and the fetch/rebase/push primitives.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Client runs the git CLI to capture tree snapshots and compare trees.
type Client struct {
	bin           string
	timeout       time.Duration
	maxPatchBytes int
}

// NewClient returns a Client invoking bin, defaulting to "git", a 10s timeout
// and DefaultMaxPatchBytes. Every call it makes is bounded by timeout.
func NewClient(bin string, timeout time.Duration, maxPatchBytes int) *Client {
	if bin == "" {
		bin = "git"
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if maxPatchBytes <= 0 {
		maxPatchBytes = DefaultMaxPatchBytes
	}
	return &Client{
		bin:           bin,
		timeout:       timeout,
		maxPatchBytes: maxPatchBytes,
	}
}

// gitEnv is the environment every git child process Client starts. The
// GIT_OPTIONAL_LOCKS=0 is the point: relevo reads a worktree while its builder
// commits in it, and without it a read such as `git status` refreshes the index,
// takes index.lock and makes that commit fail with "Unable to create
// '.../index.lock'". Commands that must write the index still take their
// mandatory lock, so it is safe everywhere. extra comes last, so a caller can
// override it.
func gitEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+1+len(extra))
	env = append(env, os.Environ()...)
	env = append(env, "GIT_OPTIONAL_LOCKS=0")
	return append(env, extra...)
}

func (c *Client) run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(env...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrGitUnavailable
		}
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return nil, fmt.Errorf("%w: %w", ErrGitUnavailable, execErr)
		}
		var pathErr *os.PathError
		if errors.As(err, &pathErr) && (errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist)) {
			return nil, fmt.Errorf("%w: %w", ErrGitUnavailable, pathErr)
		}

		outStr := strings.TrimSpace(stderr.String())
		if outStr == "" {
			outStr = strings.TrimSpace(stdout.String())
		}
		if strings.Contains(strings.ToLower(outStr), "not a git repository") {
			return nil, fmt.Errorf("%w: %s", ErrNotRepo, outStr)
		}
		return nil, fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), outStr, err)
	}

	return stdout.Bytes(), nil
}

func (c *Client) HeadCommit(ctx context.Context, dir string) (string, error) {
	out, err := c.run(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Dirty reports whether dir has uncommitted or untracked (non-ignored) changes.
func (c *Client) Dirty(ctx context.Context, dir string) (bool, error) {
	out, err := c.run(ctx, dir, nil, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}
