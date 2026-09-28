package git

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoFacts reports dir's repository identity: the absolute path of the main
// worktree's .git directory, and the origin remote's raw URL (normalisation is
// the caller's job via NormalizeOriginURL). A missing origin remote is ("", nil),
// not an error.
func (c *Client) RepoFacts(ctx context.Context, dir string) (originURL, commonDir string, err error) {
	out, err := c.run(ctx, dir, nil, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	commonDir = strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(dir, commonDir)
	}
	// Canonical: commonDir is a database key, and git resolves symlinks when
	// answering from a worktree but not from the main tree (macOS /var ->
	// /private/var), which would give one repo two keys.
	if real, rerr := filepath.EvalSymlinks(commonDir); rerr == nil {
		commonDir = real
	}

	out, err = c.run(ctx, dir, nil, "remote", "get-url", "origin")
	if err != nil {
		if errors.Is(err, ErrNotRepo) || errors.Is(err, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", "", err
		}
		// No origin remote configured: relevo simply has nothing to say about
		// origin.
		return "", commonDir, nil
	}
	originURL = strings.TrimSpace(string(out))

	return originURL, commonDir, nil
}

// Identity reports the git identity dir's repository would commit as, including
// global and system config. An unset key is ("", nil): `git config --get` exits
// 1 with no output for it, which is a fact about the machine, not a failure.
func (c *Client) Identity(ctx context.Context, dir string) (name, email string, err error) {
	get := func(key string) (string, error) {
		out, gerr := c.run(ctx, dir, nil, "config", "--get", key)
		if gerr != nil {
			if errors.Is(gerr, ErrNotRepo) || errors.Is(gerr, ErrGitUnavailable) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", gerr
			}
			var exitErr *exec.ExitError
			if errors.As(gerr, &exitErr) && exitErr.ExitCode() == 1 {
				return "", nil
			}
			return "", gerr
		}
		return strings.TrimSpace(string(out)), nil
	}

	name, err = get("user.name")
	if err != nil {
		return "", "", err
	}
	email, err = get("user.email")
	if err != nil {
		return "", "", err
	}
	return name, email, nil
}

// NormalizeOriginURL puts an origin remote URL into one comparable form, so the
// same repository reached over SSH and HTTPS groups as one:
//
//	git@host:owner/repo(.git)       -> https://host/owner/repo
//	ssh://git@host/owner/repo(.git) -> https://host/owner/repo
//	https://host/owner/repo(.git)/  -> https://host/owner/repo
//	http://...                      stays http://
//
// The host is lowercased; the path keeps its case. Trailing "/" and ".git" are
// removed. Anything else is returned trimmed and otherwise unchanged.
func NormalizeOriginURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return s
	}

	// git@host:owner/repo(.git) -- scp-like syntax, matched only when there is
	// no "://" scheme, which the ssh:// and http(s):// forms below own.
	if !strings.Contains(s, "://") {
		if at := strings.Index(s, "@"); at >= 0 {
			rest := s[at+1:]
			if colon := strings.Index(rest, ":"); colon >= 0 {
				host := rest[:colon]
				path := rest[colon+1:]
				if host != "" && path != "" && !strings.Contains(host, "/") {
					return "https://" + strings.ToLower(host) + "/" + trimRepoPath(path)
				}
			}
		}
	}

	if rest, ok := strings.CutPrefix(s, "ssh://"); ok {
		if at := strings.Index(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		if slash := strings.Index(rest, "/"); slash >= 0 {
			host := rest[:slash]
			path := rest[slash+1:]
			return "https://" + strings.ToLower(host) + "/" + trimRepoPath(path)
		}
	}

	for _, scheme := range []string{"https://", "http://"} {
		rest, ok := strings.CutPrefix(s, scheme)
		if !ok {
			continue
		}
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return scheme + strings.ToLower(rest)
		}
		host := rest[:slash]
		path := rest[slash+1:]
		return scheme + strings.ToLower(host) + "/" + trimRepoPath(path)
	}

	return s
}

// trimRepoPath strips a trailing "/" and then a trailing ".git" (in that order,
// since "owner/repo.git/" is a valid trailing form) from a repo path.
func trimRepoPath(p string) string {
	p = strings.TrimSuffix(p, "/")
	p = strings.TrimSuffix(p, ".git")
	p = strings.TrimSuffix(p, "/")
	return p
}

// OwnerRepo returns "owner/repo" from the last two path segments of a
// normalised origin URL (as NormalizeOriginURL returns it), and "" for anything
// that is not a URL with a host and at least two path segments: a local path, a
// bare host, an empty string.
func OwnerRepo(normalised string) string {
	s := strings.TrimSpace(normalised)
	scheme := strings.Index(s, "://")
	if scheme < 0 {
		return ""
	}
	rest := s[scheme+3:]
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return ""
	}
	segs := strings.Split(strings.Trim(rest[slash+1:], "/"), "/")
	if len(segs) < 2 {
		return ""
	}
	owner, repo := segs[len(segs)-2], segs[len(segs)-1]
	if owner == "" || repo == "" {
		return ""
	}
	return owner + "/" + repo
}
