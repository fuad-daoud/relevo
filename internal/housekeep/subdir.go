package housekeep

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/store"
)

// NormalizeSubdir puts a subdirectory prefix into the form RoundDir joins: a
// slash-separated path relative to the tree root, with git's trailing slash
// dropped. An empty prefix is its own empty answer. A prefix that could move
// the round's directory outside the tree is refused here, at the one boundary
// every surface stores through, so the round's directory always stays inside
// the tree.
func NormalizeSubdir(raw string) (string, error) {
	s := strings.TrimSuffix(raw, "/")
	if strings.HasPrefix(s, "/") {
		return "", fmt.Errorf("subdirectory %q must be relative to the tree root", raw)
	}
	if s != "" {
		for _, seg := range strings.Split(s, "/") {
			if seg == "" || seg == "." || seg == ".." {
				return "", fmt.Errorf("subdirectory %q is not a plain path under the tree root", raw)
			}
		}
	} else if strings.Contains(raw, "/") {
		return "", fmt.Errorf("subdirectory %q is not a plain path under the tree root", raw)
	}
	return s, nil
}

// ResolveSubdir reads the subdirectory rt was started from, the prefix of repo
// the worktree or branch the round is cut from is cut relative to. A tree git
// cannot answer for -- not a repository, no git -- resolves to "", which is the
// room at its root, because that refusal belongs to the steps that need git
// and not to a rule that only offsets the round's directory.
func ResolveSubdir(ctx context.Context, git Prefixer, repo string) string {
	prefix, err := git.ShowPrefix(ctx, repo)
	if err != nil {
		return ""
	}
	out, err := NormalizeSubdir(prefix)
	if err != nil {
		return ""
	}
	return out
}

// RoundDir names the directory a binding's round runs in: the binding's tree
// plus its subdirectory. Every surface that launches a process in it goes
// through RoundTree, which calls this.
func RoundDir(b store.Binding) string {
	if b.Subdir == "" {
		return b.CWD
	}
	return filepath.Join(b.CWD, b.Subdir)
}
