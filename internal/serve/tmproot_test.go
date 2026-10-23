package serve

import (
	"path/filepath"
	"testing"
	"time"
)

// TestRuntimeAtTmpRootPerOwner pins that every non-container mode gives each
// owner its own tmp root: a sweep of one owner's entries must never see
// another owner's.
func TestRuntimeAtTmpRootPerOwner(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{DB: testServeDB(t), Root: root, Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dirA := filepath.Join(root, "bindings", "aaa")
	dirB := filepath.Join(root, "bindings", "bbb")
	rtA := s.runtimeAt(dirA)
	rtB := s.runtimeAt(dirB)

	if rtA.TmpRoot == "" || rtB.TmpRoot == "" {
		t.Fatalf("TmpRoots = %q and %q, want both owners to get one", rtA.TmpRoot, rtB.TmpRoot)
	}
	if rtA.TmpRoot == rtB.TmpRoot {
		t.Errorf("both owners got the tmp root %q, want one per owner", rtA.TmpRoot)
	}
	if want := s.ownerTmpDir(dirA); rtA.TmpRoot != want {
		t.Errorf("owner a TmpRoot = %q, want %q", rtA.TmpRoot, want)
	}
	if want := s.ownerTmpDir(dirB); rtB.TmpRoot != want {
		t.Errorf("owner b TmpRoot = %q, want %q", rtB.TmpRoot, want)
	}
}

// TestRuntimeAtContainerModeHasNoTmpRoot pins that a container-mode owner is
// left with an empty TmpRoot: its builder sees the container's own /tmp, and
// a host path would mean nothing inside it.
func TestRuntimeAtContainerModeHasNoTmpRoot(t *testing.T) {
	env := newContainerTestEnv(t)
	rt := env.srv.runtimeAt(ownerRootOf(t, env))
	if rt.TmpRoot != "" {
		t.Errorf("container TmpRoot = %q, want empty", rt.TmpRoot)
	}
}
