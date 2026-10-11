package serve

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// TestOwnerRuntimeGitShimDirIsPerOwner: a served round reaches the same
// startProcess funnel as a local one, so the only server-side fact the shim
// needs is that each owner's store resolves its own directory.
func TestOwnerRuntimeGitShimDirIsPerOwner(t *testing.T) {
	s := newAdminServer(t, time.Now())
	idA := enrol(t, s, "alice")
	idB := enrol(t, s, "bob")
	a := ownerRuntime(t, s, idA).Store
	b := ownerRuntime(t, s, idB).Store
	if a.GitShimDir() == b.GitShimDir() {
		t.Fatalf("both owners share shim dir %q", a.GitShimDir())
	}
	if !strings.HasPrefix(a.GitShimDir(), a.Root()+string(filepath.Separator)) {
		t.Errorf("alice shim dir %q is not under her store root %q", a.GitShimDir(), a.Root())
	}
	if !strings.HasPrefix(b.GitShimDir(), b.Root()+string(filepath.Separator)) {
		t.Errorf("bob shim dir %q is not under his store root %q", b.GitShimDir(), b.Root())
	}
}

// TestServedContainerRoundMountsTheOwnersGitShim: a container round still runs
// in a worktree of the owner's shared repository, so a writer round's PATH
// entry for the owner's shim must reach a read-only bind of that directory.
func TestServedContainerRoundMountsTheOwnersGitShim(t *testing.T) {
	env := newContainerTestEnv(t)
	root := ownerRootOf(t, env)
	rt := env.srv.runtimeAt(root)
	shim := rt.Store.GitShimDir()
	spec := spawn.ProcSpec{
		Dir:        root,
		Argv:       []string{"claude", "-p", "plan"},
		Env:        []string{"PATH=" + shim + ":/usr/bin"},
		StreamPath: filepath.Join(root, "round-runner.jsonl"),
	}
	if _, err := rt.Runner.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := specAt(t, env.runner, 0)
	if want := shim + ":" + shim + ":ro"; !containsString(got.Argv, want) {
		t.Errorf("Argv = %v, want volume %q", got.Argv, want)
	}
}
