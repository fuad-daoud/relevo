package relevo

import (
	"os"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
)

// needsGitShim reports whether a binding's round writes to the tree. It keys
// on the binding's shape, which is derived from the role's shape, never on an
// actor name.
func needsGitShim(b store.Binding) bool {
	return b.Shape == store.ShapeWriter
}

// gitShimEnv is the PATH entry a writer round's process is spawned with, nil
// for a round that needs no shim. The shim is in place when it returns.
func gitShimEnv(rt Runtime, b store.Binding) ([]string, error) {
	if !needsGitShim(b) {
		return nil, nil
	}
	dir := rt.Store.GitShimDir()
	if err := git.EnsureShim(dir); err != nil {
		return nil, err
	}
	return []string{"PATH=" + git.ShimPATH(os.Getenv("PATH"), dir)}, nil
}
