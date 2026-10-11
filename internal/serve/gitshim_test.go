package serve

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
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
