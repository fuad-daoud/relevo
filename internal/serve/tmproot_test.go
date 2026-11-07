package serve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
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
	trackTmpRoot(t, s, dirA)
	trackTmpRoot(t, s, dirB)
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

// TestOwnerTmpDirShortShape pins the short root: /tmp/rv-<8>, a digest of
// the serve root and the owner id. The full composition is pinned by
// relevo's length-bound test; this pins the root half it builds on.
func TestOwnerTmpDirShortShape(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{DB: testServeDB(t), Root: root, Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	owner := filepath.Join(root, "bindings", strings.Repeat("a", 64))
	got := s.ownerTmpDir(owner)
	leaf := strings.TrimPrefix(got, "/tmp/rv-")
	if got != filepath.Join("/tmp", "rv-"+leaf) || len(leaf) != 8 || len(got) != 16 {
		t.Fatalf("ownerTmpDir = %q, want /tmp/rv-<8 hex> (16 bytes)", got)
	}
	for _, c := range leaf {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("ownerTmpDir = %q, want a lower-case hex digest", got)
		}
	}
	if again := s.ownerTmpDir(owner); again != got {
		t.Errorf("ownerTmpDir = %q then %q, want one digest per serve root and owner", got, again)
	}
}

// TestOwnerTmpDirSeparatesRootsAndOwners pins that the digest covers both
// inputs: one owner under two serve roots gets two directories, and two
// owners sharing an 8-prefix under one root get two directories.
func TestOwnerTmpDirSeparatesRootsAndOwners(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	sA, err := New(Config{DB: testServeDB(t), Root: rootA, Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sB, err := New(Config{DB: testServeDB(t), Root: rootB, Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hexA := strings.Repeat("a", 64)
	hexB := strings.Repeat("a", 8) + strings.Repeat("b", 56)
	byA := sA.ownerTmpDir(filepath.Join(rootA, "bindings", hexA))
	if byB := sB.ownerTmpDir(filepath.Join(rootB, "bindings", hexA)); byA == byB {
		t.Errorf("one owner under two roots shares %q, want one directory per serve root", byA)
	}
	if byB := sA.ownerTmpDir(filepath.Join(rootA, "bindings", hexB)); byA == byB {
		t.Errorf("two owners sharing an 8-prefix share %q, want one directory per owner", byA)
	}
}

// TestOwnerTmpTargetOnDiskShape pins the link's target: <root>/tmp/t/<8>,
// the on-disk root the bytes live in, so the disk preflight measures the
// serve filesystem while the handed-out link stays short.
func TestOwnerTmpTargetOnDiskShape(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{DB: testServeDB(t), Root: root, Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ownerHex := strings.Repeat("a", 64)
	ownerRoot := filepath.Join(root, "bindings", ownerHex)
	got := s.ownerTmpTarget(ownerRoot)
	if want := filepath.Join(root, "tmp", "t", ownerHex[:8]); got != want {
		t.Errorf("ownerTmpTarget = %q, want %q", got, want)
	}
}

// TestOwnerTmpDirLengthBound pins the move's goal: the new root plus a
// checks/<8> leaf plus Chromium's 45-byte socket suffix stays under 108
// bytes, even under a long serve root that dwarfs the digest itself.
func TestOwnerTmpDirLengthBound(t *testing.T) {
	root := filepath.Join(t.TempDir(), "serve-root-"+strings.Repeat("r", 60))
	s, err := New(Config{DB: testServeDB(t), Root: root, Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tmpRoot := s.ownerTmpDir(filepath.Join(root, "bindings", strings.Repeat("a", 64)))
	dir := filepath.Join(tmpRoot, "checks", strings.Repeat("b", 8))
	const chromiumSuffix = "/org.chromium.Chromium.XXXXXX/SingletonSocket"
	if len(chromiumSuffix) != 45 {
		t.Fatalf("chromium suffix = %d bytes, want the 45 this bound assumes", len(chromiumSuffix))
	}
	total := len(dir) + len(chromiumSuffix)
	t.Logf("tmp root %q (%d bytes) + checks leaf = %d, +45 suffix = %d", tmpRoot, len(tmpRoot), len(dir), total)
	if total >= 108 {
		t.Errorf("tmp dir %q (%d bytes) plus socket suffix reaches the sun_path limit", dir, len(dir))
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
	trackTmpRoot(t, s, owner)
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

// TestRuntimeAtReplacesOwnedSymlink pins that mode none re-checks the tmp
// link on every resolution: a link the serve uid owns that points elsewhere
// is removed and remade pointing at the target, and the next start works.
// TmpRoot stays set at the link path.
func TestRuntimeAtReplacesOwnedSymlink(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{DB: testServeDB(t), Root: root, Runner: newScriptRunner(), Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ownerRoot := filepath.Join(root, "bindings", strings.Repeat("a", 64))
	tmpRoot := s.ownerTmpDir(ownerRoot)
	target := s.ownerTmpTarget(ownerRoot)
	t.Cleanup(func() { _ = os.RemoveAll(tmpRoot) })
	if rt := s.runtimeAt(ownerRoot); rt.TmpRoot != tmpRoot {
		t.Fatalf("first resolve TmpRoot = %q, want %q", rt.TmpRoot, tmpRoot)
	}
	if err := os.Remove(tmpRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), tmpRoot); err != nil {
		t.Fatal(err)
	}
	rt := s.runtimeAt(ownerRoot)
	if rt.TmpRoot != tmpRoot {
		t.Errorf("replaced TmpRoot = %q, want it kept at %q", rt.TmpRoot, tmpRoot)
	}
	if got, err := os.Readlink(tmpRoot); err != nil || got != target {
		t.Fatalf("link = %q, %v; want it remade pointing at %s", got, err, target)
	}
	if _, err := rt.Runner.Start(context.Background(), spawn.ProcSpec{Dir: ownerRoot, Argv: []string{"true"}}); err != nil {
		t.Fatalf("Start after replace: %v", err)
	}
}

// TestRuntimeAtRefusesPlantedFile pins the non-directory half of the same
// check: a regular file at the tmp root path is refused, never treated as a
// directory.
func TestRuntimeAtRefusesPlantedFile(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{DB: testServeDB(t), Root: root, Runner: newScriptRunner(), Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ownerRoot := filepath.Join(root, "bindings", strings.Repeat("b", 64))
	tmpRoot := s.ownerTmpDir(ownerRoot)
	t.Cleanup(func() { _ = os.RemoveAll(tmpRoot) })
	if rt := s.runtimeAt(ownerRoot); rt.TmpRoot != tmpRoot {
		t.Fatalf("first resolve TmpRoot = %q, want %q", rt.TmpRoot, tmpRoot)
	}
	if err := os.RemoveAll(tmpRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpRoot, []byte("planted"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := s.runtimeAt(ownerRoot)
	_, err = rt.Runner.Start(context.Background(), spawn.ProcSpec{Dir: ownerRoot, Argv: []string{"true"}})
	if !errors.Is(err, spawn.ErrBoundarySetup) {
		t.Fatalf("Start = %v, want an error wrapping spawn.ErrBoundarySetup", err)
	}
	if !strings.Contains(err.Error(), tmpRoot) {
		t.Errorf("halt error = %q, want it to name the planted path", err)
	}
}

// TestRuntimeAtRecreatesMissingTmpRoot pins that a link /tmp aged away comes
// back on the next resolution pointing at the surviving target, with starts
// working again. TmpRoot stays set at the link path.
func TestRuntimeAtRecreatesMissingTmpRoot(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{DB: testServeDB(t), Root: root, Runner: newScriptRunner(), Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ownerRoot := filepath.Join(root, "bindings", strings.Repeat("c", 64))
	tmpRoot := s.ownerTmpDir(ownerRoot)
	target := s.ownerTmpTarget(ownerRoot)
	t.Cleanup(func() { _ = os.RemoveAll(tmpRoot) })
	_ = s.runtimeAt(ownerRoot)
	if err := os.Remove(tmpRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); err != nil {
		t.Fatalf("removing the link took the target %s: %v", target, err)
	}
	rt := s.runtimeAt(ownerRoot)
	if rt.TmpRoot != tmpRoot {
		t.Errorf("recreated TmpRoot = %q, want it kept at %q", rt.TmpRoot, tmpRoot)
	}
	if got, err := os.Readlink(tmpRoot); err != nil || got != target {
		t.Fatalf("link = %q, %v; want it recreated pointing at %s", got, err, target)
	}
	if _, err := rt.Runner.Start(context.Background(), spawn.ProcSpec{Dir: ownerRoot, Argv: []string{"true"}}); err != nil {
		t.Fatalf("Start after recreate: %v", err)
	}
}

// TestEnsureTenantRootsReplacesOwnedSymlink pins the user-mode half of the
// per-resolution link check: a link the tenant owns that points elsewhere is
// removed and remade pointing at the target, and the next start works.
func TestEnsureTenantRootsReplacesOwnedSymlink(t *testing.T) {
	env := newUserTestEnv(t, "alice")
	ownerRoot := ownerRootOf(t, env)
	tmpRoot := env.srv.ownerTmpDir(ownerRoot)
	target := env.srv.ownerTmpTarget(ownerRoot)
	t.Cleanup(func() { _ = os.RemoveAll(tmpRoot) })
	_ = env.srv.runtimeAt(ownerRoot)
	if err := os.Remove(tmpRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), tmpRoot); err != nil {
		t.Fatal(err)
	}
	rt := env.srv.runtimeAt(ownerRoot)
	if rt.TmpRoot != tmpRoot {
		t.Errorf("replaced TmpRoot = %q, want it kept at %q", rt.TmpRoot, tmpRoot)
	}
	if got, err := os.Readlink(tmpRoot); err != nil || got != target {
		t.Fatalf("link = %q, %v; want it remade pointing at %s", got, err, target)
	}
	if _, err := rt.Runner.Start(context.Background(), spawn.ProcSpec{Dir: ownerRoot, Argv: []string{"true"}}); err != nil {
		t.Fatalf("Start after replace: %v", err)
	}
}

// TestEnsureTenantRootsRefusesPlantedTargetFile pins that a regular file where
// the owner's on-disk tmp target belongs is refused and left alone: the
// target is Lstat'ed and never followed, so the plant cannot divert the link
// onto it.
func TestEnsureTenantRootsRefusesPlantedTargetFile(t *testing.T) {
	env := newUserTestEnv(t, "alice")
	ownerRoot := ownerRootOf(t, env)
	tmpRoot := env.srv.ownerTmpDir(ownerRoot)
	target := env.srv.ownerTmpTarget(ownerRoot)
	t.Cleanup(func() { _ = os.RemoveAll(tmpRoot) })
	_ = env.srv.runtimeAt(ownerRoot)
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("planted"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(target) })
	rt := env.srv.runtimeAt(ownerRoot)
	if rt.TmpRoot != tmpRoot {
		t.Errorf("refused TmpRoot = %q, want it kept at %q", rt.TmpRoot, tmpRoot)
	}
	_, err := rt.Runner.Start(context.Background(), spawn.ProcSpec{Dir: ownerRoot, Argv: []string{"true"}})
	if !errors.Is(err, spawn.ErrBoundarySetup) {
		t.Fatalf("Start = %v, want an error wrapping spawn.ErrBoundarySetup", err)
	}
	if !strings.Contains(err.Error(), target) {
		t.Errorf("halt error = %q, want it to name the planted target", err)
	}
	if fi, serr := os.Lstat(target); serr != nil || !fi.Mode().IsRegular() {
		t.Errorf("resolve took the planted target file: %v, %v", fi, serr)
	}
}

// TestEnsureTenantRootsRecreatesMissingTmpRoot pins the user-mode half of
// the re-create rule: an aged-away link comes back pointing at the surviving
// target, and the next start works.
func TestEnsureTenantRootsRecreatesMissingTmpRoot(t *testing.T) {
	env := newUserTestEnv(t, "alice")
	ownerRoot := ownerRootOf(t, env)
	tmpRoot := env.srv.ownerTmpDir(ownerRoot)
	target := env.srv.ownerTmpTarget(ownerRoot)
	t.Cleanup(func() { _ = os.RemoveAll(tmpRoot) })
	_ = env.srv.runtimeAt(ownerRoot)
	if err := os.Remove(tmpRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); err != nil {
		t.Fatalf("removing the link took the target %s: %v", target, err)
	}
	rt := env.srv.runtimeAt(ownerRoot)
	if rt.TmpRoot != tmpRoot {
		t.Errorf("recreated TmpRoot = %q, want it kept at %q", rt.TmpRoot, tmpRoot)
	}
	if got, err := os.Readlink(tmpRoot); err != nil || got != target {
		t.Fatalf("link = %q, %v; want it recreated pointing at %s", got, err, target)
	}
	if _, err := rt.Runner.Start(context.Background(), spawn.ProcSpec{Dir: ownerRoot, Argv: []string{"true"}}); err != nil {
		t.Fatalf("Start after recreate: %v", err)
	}
}
