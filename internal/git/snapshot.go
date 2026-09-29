package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// SnapshotTree writes a git tree object capturing dir's entire working tree --
// tracked and untracked, honouring .gitignore -- and returns its object id.
// The repository's own index, HEAD, refs and working tree are left byte-for-byte
// unchanged.
func (c *Client) SnapshotTree(ctx context.Context, dir string) (string, error) {
	out, err := c.run(ctx, dir, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}

	// A clean tree is HEAD's own tree: no temp index, no object written. The
	// status read takes no index lock (gitEnv sets GIT_OPTIONAL_LOCKS=0).
	status, err := c.run(ctx, dir, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if len(status) == 0 {
		if treeOut, err := c.run(ctx, dir, nil, "rev-parse", "--verify", "-q", "HEAD^{tree}"); err == nil {
			return strings.TrimSpace(string(treeOut)), nil
		}
	}

	tempDir, err := os.MkdirTemp("", "relevo-git-index-*")
	if err != nil {
		return "", fmt.Errorf("create temp index dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	tempIndex := filepath.Join(tempDir, "index")
	if err := c.prepareTempIndex(gitDir, tempIndex); err != nil {
		return "", err
	}

	env := []string{"GIT_INDEX_FILE=" + tempIndex}
	if _, err := c.run(ctx, dir, env, "add", "-A"); err != nil {
		return "", err
	}

	treeOut, err := c.run(ctx, dir, env, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(treeOut)), nil
}

// prepareTempIndex copies the repository index to tempIndex. It keeps the
// repository index's mtime: git calls an entry whose mtime is not older than the
// index file's own racy-clean and re-reads its content, and io.Copy's fresh
// mtime would otherwise let a same-size edit written in the index's timestamp
// tick be taken from the cached stat. A repository with no index yet needs no
// copy: git creates one.
func (c *Client) prepareTempIndex(gitDir, tempIndex string) error {
	repoIndex := filepath.Join(gitDir, "index")
	src, err := os.Open(repoIndex)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open repo index: %w", err)
	}
	defer func() { _ = src.Close() }()

	dst, err := os.OpenFile(tempIndex, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create temp index: %w", err)
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		return fmt.Errorf("copy index: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close temp index: %w", closeErr)
	}

	info, err := os.Stat(repoIndex)
	if err != nil {
		return fmt.Errorf("preserve index mtime: %w", err)
	}
	if err := os.Chtimes(tempIndex, info.ModTime(), info.ModTime()); err != nil {
		return fmt.Errorf("preserve index mtime: %w", err)
	}
	return nil
}

// DiffTrees compares two tree objects and returns the patch and its stat. Patch
// is nil when the body exceeded maxPatchBytes (Truncated) or nothing changed;
// Stat is exact either way.
func (c *Client) DiffTrees(ctx context.Context, dir, from, to string) (Diff, error) {
	if _, err := c.run(ctx, dir, nil, "rev-parse", "--git-dir"); err != nil {
		return Diff{}, err
	}

	stat, err := c.numstat(ctx, dir, "diff", "--numstat", from, to)
	if err != nil {
		return Diff{}, err
	}
	if stat.Empty() {
		return Diff{Stat: stat}, nil
	}

	patch, truncated, err := c.diffPatch(ctx, dir, from, to)
	if err != nil {
		return Diff{}, err
	}
	if truncated {
		return Diff{Stat: stat, Truncated: true}, nil
	}
	return Diff{Stat: stat, Patch: patch}, nil
}

// diffPatch reads the unified diff body, stopping one byte past the client's cap
// so the caller only learns it was too large.
func (c *Client) diffPatch(ctx context.Context, dir, from, to string) ([]byte, bool, error) {
	ctxTimeout, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctxTimeout, c.bin, "diff", from, to)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, fmt.Errorf("git diff stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(ctxTimeout.Err(), context.DeadlineExceeded) {
			return nil, false, ctxTimeout.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, false, ErrGitUnavailable
		}
		return nil, false, err
	}

	limited := io.LimitReader(stdoutPipe, int64(c.maxPatchBytes)+1)
	body, readErr := io.ReadAll(limited)
	if readErr != nil {
		_ = cmd.Wait()
		return nil, false, fmt.Errorf("read git diff: %w", readErr)
	}

	if len(body) > c.maxPatchBytes {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, true, nil
	}

	if err := cmd.Wait(); err != nil {
		if errors.Is(ctxTimeout.Err(), context.DeadlineExceeded) {
			return nil, false, ctxTimeout.Err()
		}
		outStr := strings.TrimSpace(stderr.String())
		if strings.Contains(strings.ToLower(outStr), "not a git repository") {
			return nil, false, fmt.Errorf("%w: %s", ErrNotRepo, outStr)
		}
		return nil, false, fmt.Errorf("git diff %s %s: %s: %w", from, to, outStr, err)
	}

	return body, false, nil
}

// DiffWorktreeStat compares a tree object against dir's current working tree
// (staged changes included) and returns just the stat, with no patch body.
func (c *Client) DiffWorktreeStat(ctx context.Context, dir, tree string) (Stat, error) {
	if _, err := c.run(ctx, dir, nil, "rev-parse", "--git-dir"); err != nil {
		return Stat{}, err
	}
	return c.numstat(ctx, dir, "diff", "--numstat", tree)
}

func (c *Client) numstat(ctx context.Context, dir string, args ...string) (Stat, error) {
	out, err := c.run(ctx, dir, nil, args...)
	if err != nil {
		return Stat{}, err
	}
	return parseNumstat(out)
}

// parseNumstat reads `git diff --numstat` output; a "-" count marks a binary
// path, which contributes to FilesChanged but not to the line counts.
func parseNumstat(out []byte) (Stat, error) {
	var stat Stat
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		stat.FilesChanged++
		if parts[0] != "-" {
			if ins, err := strconv.Atoi(parts[0]); err == nil {
				stat.Insertions += ins
			}
		}
		if parts[1] != "-" {
			if del, err := strconv.Atoi(parts[1]); err == nil {
				stat.Deletions += del
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Stat{}, fmt.Errorf("scan numstat: %w", err)
	}
	return stat, nil
}

// MaterializeTree writes tree into dir's working files without committing:
// `read-tree --reset -u` deletes files missing from the tree, then a mixed reset
// leaves the index equal to HEAD, so every difference is an unstaged change and
// files not in HEAD are untracked. On error dir may be partly written; the
// caller removes it.
func (c *Client) MaterializeTree(ctx context.Context, dir, tree string) error {
	if _, err := c.run(ctx, dir, nil, "read-tree", "--reset", "-u", tree); err != nil {
		return err
	}
	_, err := c.run(ctx, dir, nil, "reset", "-q")
	return err
}
