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
	"syscall"
	"time"
)

// Client runs the git CLI to capture tree snapshots and compare trees.
type Client struct {
	bin           string
	timeout       time.Duration
	maxPatchBytes int

	// credential, when non-nil, is the tenant identity every git child runs as.
	// nil means the git child runs as the serve uid.
	credential *syscall.Credential
	// gitEnvExtra is the tenant environment appended to every git child's env
	// (a user-mode client carries the tenant's HOME/USER/LOGNAME), so a git
	// process running as the tenant resolves paths against its own home.
	gitEnvExtra []string
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
//
// deny names variables removed from parent before extra is appended: a
// credentialed client passes its own tenant names so the daemon's HOME/USER/
// LOGNAME cannot survive beside the tenant's, which would leave the lookup to
// duplicate-name semantics (undefined by POSIX).
func gitEnv(parent, deny []string, extra []string) []string {
	if len(deny) > 0 {
		denied := make(map[string]struct{}, len(deny))
		for _, d := range deny {
			denied[d] = struct{}{}
		}
		kept := parent[:0:0]
		for _, e := range parent {
			name, _, _ := strings.Cut(e, "=")
			if _, ok := denied[name]; ok {
				continue
			}
			kept = append(kept, e)
		}
		parent = kept
	}
	env := make([]string, 0, len(parent)+1+len(extra))
	env = append(env, parent...)
	env = append(env, "GIT_OPTIONAL_LOCKS=0")
	return append(env, extra...)
}

// envNames returns the variable names an environment slice sets, in order.
func envNames(env []string) []string {
	names := make([]string, 0, len(env))
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		names = append(names, name)
	}
	return names
}

// WithCredential returns a copy of c whose every git child runs as uid/gid with
// env appended to its environment. It never mutates the receiver, so the
// server-wide client stays available for none-mode owners.
func (c *Client) WithCredential(uid, gid uint32, env []string) *Client {
	cp := *c
	cp.credential = &syscall.Credential{Uid: uid, Gid: gid}
	cp.gitEnvExtra = append([]string(nil), env...)
	return &cp
}

// command is the one exec.Cmd assembler for every git child the client starts:
// the binary, args, working directory, the git environment (with the client's
// own tenant env appended) and, when set, the tenant credential. It is pure --
// no child is started -- so the identity and environment a git call carries can
// be pinned without running git. run and diffPatch both build through it.
func (c *Client) command(ctx context.Context, dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Dir = dir
	extra := append(append([]string(nil), c.gitEnvExtra...), env...)
	// Deny the inherited copies of every name the tenant env sets, so the
	// tenant's HOME/USER/LOGNAME are the only ones the git child sees.
	cmd.Env = gitEnv(os.Environ(), envNames(c.gitEnvExtra), extra)
	if c.credential != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: c.credential}
	}
	return cmd
}

func (c *Client) run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := c.command(ctx, dir, env, args...)
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
