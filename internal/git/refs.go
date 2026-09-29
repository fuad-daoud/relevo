package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// TreeFingerprint is a short, stable hash of dir's HEAD and porcelain status,
// the daemon's cheap "did the tree move?" signal. It reads no diff and writes
// no snapshot. An unborn HEAD is not an error: rev-parse fails there and the
// fingerprint is the status alone.
func (c *Client) TreeFingerprint(ctx context.Context, dir string) (string, error) {
	head, err := c.run(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		head = nil
	}
	st, err := c.run(ctx, dir, nil, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(string(head)) + "\n" + string(st)))
	return hex.EncodeToString(sum[:])[:16], nil
}

// RevListCount is `git rev-list --count from..to`, 0 when they are the same
// commit.
func (c *Client) RevListCount(ctx context.Context, dir, from, to string) (int, error) {
	out, err := c.run(ctx, dir, nil, "rev-list", "--count", from+".."+to)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count: parse %q: %w", strings.TrimSpace(string(out)), err)
	}
	return n, nil
}

func (c *Client) BranchExists(ctx context.Context, dir, branch string) (bool, error) {
	ref := branch
	if !strings.HasPrefix(ref, "refs/heads/") {
		ref = "refs/heads/" + ref
	}

	_, err := c.run(ctx, dir, nil, "rev-parse", "--verify", "--quiet", ref)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false, err
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func (c *Client) CreateBranch(ctx context.Context, dir, branch, commit string) error {
	branchName := strings.TrimPrefix(branch, "refs/heads/")
	exists, err := c.BranchExists(ctx, dir, branchName)
	if err != nil {
		return err
	}
	if exists {
		return ErrBranchExists
	}
	_, err = c.run(ctx, dir, nil, "branch", branchName, commit)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "already exists") {
			return ErrBranchExists
		}
		return err
	}
	return nil
}

// CreateTrackingBranch is the existing-branch form of CreateBranch: the
// upstream ref is the start point.
func (c *Client) CreateTrackingBranch(ctx context.Context, dir, branch, upstream string) error {
	branchName := strings.TrimPrefix(branch, "refs/heads/")
	_, err := c.run(ctx, dir, nil, "branch", "--track", branchName, upstream)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "already exists") {
			return ErrBranchExists
		}
		return err
	}
	return nil
}

// DeleteBranch force-removes branch. A missing branch is not an error, so
// cleanup calls it without checking first.
func (c *Client) DeleteBranch(ctx context.Context, dir, branch string) error {
	branchName := strings.TrimPrefix(branch, "refs/heads/")
	_, err := c.run(ctx, dir, nil, "branch", "-D", branchName)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "not found") {
			return nil
		}
		return err
	}
	return nil
}

// RootCommit returns the single root commit SHA. With several roots (a grafted
// or multi-root history) it returns the lexicographically smallest, so the
// result is deterministic.
func (c *Client) RootCommit(ctx context.Context, dir string) (string, error) {
	out, err := c.run(ctx, dir, nil, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", err
		}
		return "", ErrRefMissing
	}
	roots := strings.Fields(string(out))
	if len(roots) == 0 {
		return "", ErrRefMissing
	}
	sort.Strings(roots)
	return roots[0], nil
}

// RefSHA resolves ref to a commit SHA. (sha, true, nil) when it resolves,
// ("", false, nil) when ref does not exist.
func (c *Client) RefSHA(ctx context.Context, dir, ref string) (sha string, ok bool, err error) {
	out, err := c.run(ctx, dir, nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", false, err
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	res := strings.TrimSpace(string(out))
	if res == "" {
		return "", false, nil
	}
	return res, true, nil
}

// UpdateRef updates ref to newSHA, verifying oldSHA when it is non-empty.
func (c *Client) UpdateRef(ctx context.Context, dir, ref, newSHA, oldSHA string) error {
	args := []string{"update-ref", ref, newSHA}
	if oldSHA != "" {
		args = append(args, oldSHA)
	}
	_, err := c.run(ctx, dir, nil, args...)
	return err
}

// DeleteRef removes ref. A missing ref is not an error (git succeeds), so
// cleanup can delete what it listed without a check-then-delete race.
func (c *Client) DeleteRef(ctx context.Context, dir, ref string) error {
	_, err := c.run(ctx, dir, nil, "update-ref", "-d", ref)
	return err
}

// ListRefs returns every ref whose name begins with prefix, in git's order.
func (c *Client) ListRefs(ctx context.Context, dir, prefix string) ([]string, error) {
	out, err := c.run(ctx, dir, nil, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		refs = append(refs, line)
	}
	return refs, nil
}

// RefOnRemote reports whether ref's commit is contained in a remote-tracking
// ref, i.e. whether it was pushed. Local refs only: no fetch, no network, and a
// stale remote-tracking ref still counts.
func (c *Client) RefOnRemote(ctx context.Context, dir, ref string) (bool, error) {
	out, err := c.run(ctx, dir, nil, "for-each-ref", "--count=1", "--format=%(refname)", "--contains", ref, "refs/remotes/")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			return true, nil
		}
	}
	return false, nil
}

// ListTags maps each tag short name to the commit it points at: an annotated
// tag is peeled to its commit, a lightweight tag already is one.
func (c *Client) ListTags(ctx context.Context, dir string) (map[string]string, error) {
	out, err := c.run(ctx, dir, nil,
		"for-each-ref",
		"--format=%(refname:strip=2)%00%(objectname)%00%(*objectname)",
		"refs/tags")
	if err != nil {
		return nil, err
	}

	tags := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) < 2 || fields[0] == "" || fields[1] == "" {
			continue
		}
		sha := fields[1]
		if len(fields) > 2 && fields[2] != "" {
			sha = fields[2]
		}
		tags[fields[0]] = sha
	}
	return tags, nil
}

// CurrentBranch is dir's checked-out branch, or "" when HEAD is detached:
// `rev-parse --abbrev-ref HEAD` answers the literal "HEAD" there, which is not
// a branch name and must never be recorded as one.
func (c *Client) CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := c.run(ctx, dir, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(out))
	if name == "HEAD" {
		return "", nil
	}
	return name, nil
}
