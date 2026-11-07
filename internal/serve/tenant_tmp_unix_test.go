//go:build unix

package serve

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// requireTenantBoundary skips when the user-mode tenant boundary cannot be
// established here. ensureTenantRoots chowns the owner root to root:<gid> and
// then verifies the group: without chown privilege the chown is tolerated, and
// when the temporary parent already carries another group (macOS CI) the group
// check fails before the decorator under test runs. The decorator does not
// depend on the boundary itself, so skipping keeps the pin where the fixture
// can exist instead of failing where it cannot.
func requireTenantBoundary(t *testing.T) {
	t.Helper()
	gid := os.Getgid()
	dir := t.TempDir()
	if err := os.Lchown(dir, 0, gid); err == nil {
		return
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Gid) != gid {
		t.Skipf("tenant boundary needs chown privilege or a group-owned TMPDIR")
	}
}

// TestUserModeOwnerTmpDir pins that a user-mode owner's children start with
// TMPDIR and GOTMPDIR pointing at its own short tmp root: the daemon's state
// tmp dir is root-owned, so a tenant cannot write there. The variables are
// cleared in this process first, because an inherited value is the user's own
// choice and must survive.
func TestUserModeOwnerTmpDir(t *testing.T) {
	requireTenantBoundary(t)
	t.Setenv("TMPDIR", "")
	t.Setenv("GOTMPDIR", "")
	env := newUserTestEnv(t, "alice")
	ownerRoot := ownerRootOf(t, env)
	rt := env.srv.runtimeAt(ownerRoot)

	want := env.srv.ownerTmpDir(ownerRoot)
	if _, err := rt.Runner.Start(context.Background(), spawn.ProcSpec{
		Dir: ownerRoot, Argv: []string{"true"},
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	spec := specAt(t, env.runner, len(env.runner.specs)-1)
	for _, name := range proc.TmpDirEnv {
		if entry := name + "=" + want; !slices.Contains(spec.Env, entry) {
			t.Errorf("owner spec Env = %v, want %q", spec.Env, entry)
		}
	}
}

// TestUserModeOwnerTmpDirSpecWins pins the override rule's first arm: a spec
// that already names a variable keeps its own value.
func TestUserModeOwnerTmpDirSpecWins(t *testing.T) {
	requireTenantBoundary(t)
	t.Setenv("TMPDIR", "")
	t.Setenv("GOTMPDIR", "")
	env := newUserTestEnv(t, "alice")
	ownerRoot := ownerRootOf(t, env)
	rt := env.srv.runtimeAt(ownerRoot)

	if _, err := rt.Runner.Start(context.Background(), spawn.ProcSpec{
		Dir: ownerRoot, Argv: []string{"true"}, Env: []string{"TMPDIR=/spec/tmp"},
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	spec := specAt(t, env.runner, len(env.runner.specs)-1)
	if !slices.Contains(spec.Env, "TMPDIR=/spec/tmp") {
		t.Errorf("owner spec Env = %v, want the spec's own TMPDIR kept", spec.Env)
	}
	want := env.srv.ownerTmpDir(ownerRoot)
	if !slices.Contains(spec.Env, "GOTMPDIR="+want) {
		t.Errorf("owner spec Env = %v, want GOTMPDIR defaulted to %q", spec.Env, want)
	}
	if slices.Contains(spec.Env, "TMPDIR="+want) {
		t.Errorf("the owner's directory overrode the spec's TMPDIR: %v", spec.Env)
	}
}

// TestTmpDirRunnerKeepsTheInheritedTmpDir pins the rule's second arm on the
// user-mode path: a value the user set reaches the owner unchanged, so an
// operator who points TMPDIR somewhere himself keeps it.
func TestTmpDirRunnerKeepsTheInheritedTmpDir(t *testing.T) {
	requireTenantBoundary(t)
	user := t.TempDir()
	t.Setenv("TMPDIR", user)
	t.Setenv("GOTMPDIR", user)
	env := newUserTestEnv(t, "alice")
	ownerRoot := ownerRootOf(t, env)
	rt := env.srv.runtimeAt(ownerRoot)

	if _, err := rt.Runner.Start(context.Background(), spawn.ProcSpec{
		Dir: ownerRoot, Argv: []string{"true"},
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The value reaches the child through the inherited environment rather
	// than the spec, so what this pins is that the decorator adds nothing: the
	// base runner's own rule then passes the user's value through.
	spec := specAt(t, env.runner, len(env.runner.specs)-1)
	for _, name := range proc.TmpDirEnv {
		if proc.HasEnvName(spec.Env, name) {
			t.Errorf("owner spec Env = %v, want no %s entry: the user's value is inherited", spec.Env, name)
		}
	}
	if v := os.Getenv("TMPDIR"); v != user {
		t.Fatalf("TMPDIR = %q, want the user's %q", v, user)
	}
}

// TestEnsureTmpLinkRefusesForeignOwnedLink pins the link's ownership check
// through a direct call: a link another uid owns is refused untouched, even
// when it already points at the target. The fake euid stands in for a tenant
// the link does not belong to, so the test needs no root.
func TestEnsureTmpLinkRefusesForeignOwnedLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	const foreign = 4294967294
	if err := ensureTmpLink(link, target, foreign); err == nil {
		t.Fatal("ensureTmpLink accepted a foreign-owned link")
	} else if !strings.Contains(err.Error(), link) {
		t.Errorf("refusal = %q, want it to name the planted link", err)
	}
	if got, err := os.Readlink(link); err != nil || got != target {
		t.Errorf("the refused link now points at %q (%v), want it left alone", got, err)
	}
}

// TestUserModeTmpLinkBelongsToServe pins whose link it is in user mode: serve
// makes it, so it belongs to serve's euid, never the tenant's. The tenant here
// has a uid nothing on disk owns, so a check against the tenant would refuse
// the link serve itself made on the first resolution.
func TestUserModeTmpLinkBelongsToServe(t *testing.T) {
	env := newUserTestEnv(t, "alice")
	ownerRoot := ownerRootOf(t, env)
	link := env.srv.ownerTmpDir(ownerRoot)
	t.Cleanup(func() { _ = os.RemoveAll(link) })
	tenant := isolate.Tenant{User: "alice", UID: 4294967294, GID: uint32(os.Getgid()), Home: "/home/alice"}
	for i := range 2 {
		if err := env.srv.ensureTenantRoots(env.id, tenant); err != nil {
			t.Fatalf("resolution %d: %v", i+1, err)
		}
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if ouid, _, ok := db.StatOwner(fi); ok && ouid != uint32(os.Geteuid()) {
		t.Errorf("link owner = %d, want serve's euid %d", ouid, os.Geteuid())
	}
	if got, err := os.Readlink(link); err != nil || got != env.srv.ownerTmpTarget(ownerRoot) {
		t.Errorf("link = %q, %v; want it pointing at the target", got, err)
	}
}

// TestTmpDirRunnerPassesSpecTmpDirThrough pins that a spec carrying its own
// temp directory reaches the base with Env unchanged: the base applies the
// spec's directory itself, so the owner's default must not be appended beside
// it.
func TestTmpDirRunnerPassesSpecTmpDirThrough(t *testing.T) {
	requireTenantBoundary(t)
	t.Setenv("TMPDIR", "")
	t.Setenv("GOTMPDIR", "")
	env := newUserTestEnv(t, "alice")
	ownerRoot := ownerRootOf(t, env)
	rt := env.srv.runtimeAt(ownerRoot)

	if _, err := rt.Runner.Start(context.Background(), spawn.ProcSpec{
		Dir: ownerRoot, Argv: []string{"true"}, TmpDir: "/proc/tmp",
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	spec := specAt(t, env.runner, len(env.runner.specs)-1)
	for _, name := range proc.TmpDirEnv {
		if proc.HasEnvName(spec.Env, name) {
			t.Errorf("owner spec Env = %v, want no %s entry: the base applies the spec's own directory", spec.Env, name)
		}
	}
	if spec.TmpDir != "/proc/tmp" {
		t.Errorf("owner spec TmpDir = %q, want the spec's own directory forwarded untouched", spec.TmpDir)
	}
}
