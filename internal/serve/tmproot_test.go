package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// TestTickPacesTmpSweepAcrossTicks pins that serve's sweep keeps its pace even
// though every tick builds a fresh Daemon per owner: a leftover planted inside
// the interval survives the next tick and goes once the interval has passed.
func TestTickPacesTmpSweepAcrossTicks(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	s, err := New(Config{DB: testServeDB(t), Root: root, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	owner := filepath.Join(root, "bindings", strings.Repeat("a", 64))
	if err := os.MkdirAll(owner, 0o700); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(s.ownerTmpDir(owner), "rounds", "ghost-001")
	plant := func() {
		t.Helper()
		if err := os.MkdirAll(leftover, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tick := func() {
		t.Helper()
		if err := s.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}

	plant()
	tick()
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("first tick left %s (err %v), want it swept", leftover, err)
	}

	plant()
	now = now.Add(time.Minute)
	tick()
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("a tick inside the interval removed %s: %v", leftover, err)
	}

	now = now.Add(10 * time.Minute)
	tick()
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("a tick past the interval left %s (err %v), want it swept", leftover, err)
	}
}
