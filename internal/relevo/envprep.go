package relevo

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/diskfree"
)

// spawnHaltText is the halt a failed round start records. A preflight refusal
// is the machine's, not the builder's, so it reads as itself; any other
// start error keeps the builder-spawn prefix.
func spawnHaltText(err error) string {
	var envErr *EnvironmentError
	if errors.As(err, &envErr) {
		return err.Error()
	}
	return "builder spawn failed: " + err.Error()
}

// preflightDisk refuses a process start when the worktree or the tmp root has
// less free space than the policy minimum. A runtime with no tmp root checks
// the worktree alone. The refusal is a plain *EnvironmentError: it is never
// a spawnFailure, so it gates no candidate.
func preflightDisk(rt Runtime, tree string) error {
	free := rt.DiskFree
	if free == nil {
		free = diskfree.Free
	}
	paths := []string{tree}
	if rt.TmpRoot != "" {
		paths = append(paths, rt.TmpRoot)
	}
	return CheckDiskFree(paths, rt.Policy.DiskMinFreeBytes(), free)
}

// goFlagsEnv is the GOFLAGS entry a process in tree starts with, or nil when
// it needs none. The inherited GOFLAGS is the parent the entry is built on, so
// a value the host already set is extended rather than replaced. A runtime
// with no Config -- every served runtime -- reads the repository default: on.
func goFlagsEnv(ctx context.Context, rt Runtime, tree string) ([]string, error) {
	settings, err := RepoSettingsFor(ctx, rt, tree)
	if err != nil {
		return nil, err
	}
	var parent []string
	if v := os.Getenv("GOFLAGS"); v != "" {
		parent = []string{goFlagsKey + v}
	}
	out := goFlagsEntry(parent, tree, settings.TrimpathOn())
	for _, e := range out {
		if strings.HasPrefix(e, goFlagsKey) && (len(parent) == 0 || e != parent[0]) {
			return []string{e}, nil
		}
	}
	return nil, nil
}
