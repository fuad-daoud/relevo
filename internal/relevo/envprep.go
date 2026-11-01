package relevo

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/diskfree"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/store"
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
// less free space than the policy minimum. The refusal is a plain
// *EnvironmentError: it is never a spawnFailure, so it gates no candidate.
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
// a value the host already set is extended rather than replaced. A binding
// with a stored trimpath value decides: only "off" opts out. Otherwise the
// repository's own settings answer, and a runtime with no Config reads the
// repository default: on.
func goFlagsEnv(ctx context.Context, rt Runtime, tree string, binding *store.Binding) ([]string, error) {
	on := true
	if binding != nil && binding.Serve != nil && binding.Serve.Trimpath != "" {
		on = binding.Serve.Trimpath != "off"
	} else {
		settings, err := RepoSettingsFor(ctx, rt, tree)
		if err != nil {
			return nil, err
		}
		on = settings.TrimpathOn()
	}
	var parent []string
	if v := os.Getenv("GOFLAGS"); v != "" {
		parent = []string{housekeep.GoFlagsKey + v}
	}
	out := housekeep.GoFlagsEntry(parent, tree, on)
	for _, e := range out {
		if strings.HasPrefix(e, housekeep.GoFlagsKey) && (len(parent) == 0 || e != parent[0]) {
			return []string{e}, nil
		}
	}
	return nil, nil
}
