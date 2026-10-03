package serve

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

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
// TMPDIR and GOTMPDIR pointing at its own <root>/tmp/<hex>: the daemon's state
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

	want := filepath.Join(env.srv.cfg.Root, "tmp", filepath.Base(ownerRoot))
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
	want := filepath.Join(env.srv.cfg.Root, "tmp", filepath.Base(ownerRoot))
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
		if hasEnvName(spec.Env, name) {
			t.Errorf("owner spec Env = %v, want no %s entry: the user's value is inherited", spec.Env, name)
		}
	}
	if v := os.Getenv("TMPDIR"); v != user {
		t.Fatalf("TMPDIR = %q, want the user's %q", v, user)
	}
}

// TestTmpDirRunnerIsTransparentToScopes pins that the decorator keeps every
// optional half of a Runner, so wrapping it cannot hide the base's scope
// support from a caller that type-asserts for it.
func TestTmpDirRunnerIsTransparentToScopes(t *testing.T) {
	base := newScriptRunner()
	wrapped := withTmpDir(base, t.TempDir())
	if _, ok := wrapped.(spawn.ScopeProber); !ok {
		t.Error("wrapper is not a spawn.ScopeProber")
	}
	if _, ok := wrapped.(spawn.ScopeStopper); !ok {
		t.Error("wrapper is not a spawn.ScopeStopper")
	}
	if _, ok := wrapped.(spawn.ScopeResultProber); !ok {
		t.Error("wrapper is not a spawn.ScopeResultProber")
	}
}
