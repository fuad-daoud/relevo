package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// fakeReaper is a no-op SessionDeleter a test can hand ReaperFor.
type fakeReaper struct{}

func (fakeReaper) DeleteSession(context.Context, store.AbandonedSession) error { return nil }

// newUserTestEnv builds a user-mode server over the script runner, with a fake
// LookupUser that resolves any name to the test's own uid and gid (so
// ensureTenantRoots can create the owner root without root). unixUser is the
// user the enrolled client declares; "" leaves it undeclared.
func newUserTestEnv(t *testing.T, unixUser string, cfgOpts ...func(*Config)) *testEnv {
	t.Helper()
	gitClient := git.NewClient("git", 0, 0)
	clientDir, headSHA, _, repoID := newClientRepo(t, gitClient, "test")
	cSet, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	runner := newScriptRunner()
	wrapped, err := isolate.Wrap(runner, isolate.ModeUser)
	if err != nil {
		t.Fatalf("isolate.Wrap: %v", err)
	}
	cfg := Config{
		DB:         testServeDB(t),
		Root:       t.TempDir(),
		Candidates: cSet,
		Runner:     wrapped,
		Git:        gitClient,
		Now:        time.Now,
		Isolation:  isolate.ModeUser,
		LookupUser: func(name string) (isolate.Tenant, error) {
			return isolate.Tenant{User: name, UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), Home: "/home/" + name}, nil
		},
		Audiences: []string{testAudience},
	}
	for _, opt := range cfgOpts {
		opt(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := srv.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), unixUser, time.Now()); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return &testEnv{
		srv:       srv,
		ts:        ts,
		kp:        kp,
		id:        id,
		clientDir: clientDir,
		headSHA:   headSHA,
		repoID:    repoID,
		runner:    runner,
		gitClient: gitClient,
		transport: remote.NewBundleTransport(gitClient, t.TempDir()),
	}
}

// ownerRootOf is the bindings/<hex> path for the env's only client.
func ownerRootOf(t *testing.T, env *testEnv) string {
	t.Helper()
	root, err := env.srv.ownerRoot(env.id)
	if err != nil {
		t.Fatalf("ownerRoot: %v", err)
	}
	return root
}

// specAt copies the runner's i-th recorded spec.
func specAt(t *testing.T, r *scriptRunner, i int) spawn.ProcSpec {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.specs) {
		t.Fatalf("runner recorded %d specs, want at least %d", len(r.specs), i+1)
	}
	return r.specs[i]
}

// TestServedRunnerRunsAsTenant pins the user-mode translation at the runner:
// the owner's Start carries the tenant credential, the tenant's HOME/USER/
// LOGNAME, the inherited home deny list plus HOME/USER/LOGNAME, and no scope.
func TestServedRunnerRunsAsTenant(t *testing.T) {
	env := newUserTestEnv(t, "alice")
	rt := env.srv.runtimeAt(ownerRootOf(t, env))

	spec := spawn.ProcSpec{
		Dir:   ownerRootOf(t, env),
		Argv:  []string{"true"},
		Env:   []string{"RELEVO_RUNNER=api"},
		Scope: &spawn.ScopeSpec{Unit: "relevo-round-alice-1", CPUWeight: 100},
	}
	if _, err := rt.Runner.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := specAt(t, env.runner, 0)
	if got.Credential == nil || got.Credential.UID != uint32(os.Getuid()) || got.Credential.GID != uint32(os.Getgid()) {
		t.Fatalf("Credential = %+v, want uid %d gid %d", got.Credential, os.Getuid(), os.Getgid())
	}
	if got.Scope != nil {
		t.Errorf("Scope = %+v, want nil: user mode runs scopes off", got.Scope)
	}
	for _, want := range []string{"HOME=/home/alice", "USER=alice", "LOGNAME=alice"} {
		found := false
		for _, e := range got.Env {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("Env = %v, want it to contain %s", got.Env, want)
		}
	}
	for _, name := range append(append([]string(nil), isolate.InheritedHomeVars...), "HOME", "USER", "LOGNAME") {
		if !containsString(got.DenyEnv, name) {
			t.Errorf("DenyEnv = %v, want it to deny %s", got.DenyEnv, name)
		}
	}
}

func containsString(s []string, want string) bool {
	for _, e := range s {
		if e == want {
			return true
		}
	}
	return false
}

// TestUserModeOwnerGitRunsAsTenant pins that runtimeAt gives the owner its own
// git client rather than the server-wide one: the owner's client is a
// WithCredential copy, so its children run as the tenant. The credential and
// environment it carries are pinned by internal/git's TestCommandAppliesCredential
// and TestCommandEnvCarriesOneHome, which never spawn.
func TestUserModeOwnerGitRunsAsTenant(t *testing.T) {
	env := newUserTestEnv(t, "alice")
	rt := env.srv.runtimeAt(ownerRootOf(t, env))
	if rt.Git == relevo.Git(env.srv.cfg.Git) {
		t.Fatal("the owner runtime kept the server-wide git client, want a WithCredential copy")
	}
}

// TestUserModeReaperRunsAsTenant pins that runtimeAt builds the owner's session
// reaper from ReaperFor(t), so a user-mode delete runs as the tenant.
func TestUserModeReaperRunsAsTenant(t *testing.T) {
	var seen []isolate.Tenant
	env := newUserTestEnv(t, "alice", func(c *Config) {
		c.ReaperFor = func(t isolate.Tenant) relevo.SessionDeleter {
			seen = append(seen, t)
			return fakeReaper{}
		}
	})
	rt := env.srv.runtimeAt(ownerRootOf(t, env))
	if len(seen) != 1 || seen[0].User != "alice" {
		t.Fatalf("ReaperFor saw %+v, want one tenant alice", seen)
	}
	if rt.SessionReaper == nil {
		t.Error("owner runtime has no session reaper, want the tenant one")
	}
}

// TestEnsureTenantRootsRefusesSymlink pins that a symlink where a tenant
// directory belongs is refused, never followed.
func TestEnsureTenantRootsRefusesSymlink(t *testing.T) {
	env := newUserTestEnv(t, "alice")
	root := env.srv.cfg.Root
	dir, ok := env.id.Dir()
	if !ok {
		t.Fatal("malformed test client id")
	}
	if err := os.MkdirAll(filepath.Join(root, "repos"), 0o711); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "repos", dir)); err != nil {
		t.Fatal(err)
	}
	tenant := isolate.Tenant{User: "alice", UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), Home: "/home/alice"}
	if err := env.srv.ensureTenantRoots(env.id, tenant); err == nil {
		t.Fatal("ensureTenantRoots followed a symlinked repos/<hex>")
	}
}

// TestUserModeRoundWithoutUnixUserHaltsNeedsYou pins that an owner with no
// declared unix_user makes the owner's runner refuse every spawn with
// spawn.ErrBoundarySetup carrying the enroll fix, which the round start path
// turns into a NEEDS YOU halt (step 3).
func TestUserModeRoundWithoutUnixUserHaltsNeedsYou(t *testing.T) {
	env := newUserTestEnv(t, "")
	rt := env.srv.runtimeAt(ownerRootOf(t, env))
	_, err := rt.Runner.Start(context.Background(), spawn.ProcSpec{Dir: ownerRootOf(t, env), Argv: []string{"true"}})
	if !errors.Is(err, spawn.ErrBoundarySetup) {
		t.Fatalf("Start = %v, want an error wrapping spawn.ErrBoundarySetup", err)
	}
	if !strings.Contains(err.Error(), "enroll") && !strings.Contains(err.Error(), "useradd") {
		t.Errorf("halt error = %q, want it to name the enrollment fix", err)
	}
}

// TestUserModeCreateRefusedWithoutUnixUser pins that a user-mode create with no
// declared unix_user is refused 422, before any side effect.
func TestUserModeCreateRefusedWithoutUnixUser(t *testing.T) {
	env := newUserTestEnv(t, "")
	body, _ := json.Marshal(remote.CreateBindingRequest{
		Name: "api", RepoID: env.repoID, BaseCommit: env.headSHA, Role: "builder",
	})
	resp, out := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", body, "application/json")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("create status = %d, want 422; body: %s", resp.StatusCode, string(out))
	}
	if !strings.Contains(string(out), "enroll") {
		t.Errorf("create body = %s, want it to name the enroll fix", string(out))
	}
}

// TestUserModeRoundWithForeignOwnerRootHaltsNeedsYou pins that an owner root
// with the wrong mode makes the owner's runner refuse every spawn with
// spawn.ErrBoundarySetup and the exact chmod line, which the round start path
// turns into a NEEDS YOU halt (step 3). It resolves once to create the root,
// then makes it foreign, then resolves again.
func TestUserModeRoundWithForeignOwnerRootHaltsNeedsYou(t *testing.T) {
	env := newUserTestEnv(t, "alice")
	ownerRoot := ownerRootOf(t, env)
	_ = env.srv.runtimeAt(ownerRoot) // creates the root in the correct layout

	if err := os.Chmod(ownerRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	rt := env.srv.runtimeAt(ownerRoot)
	_, err := rt.Runner.Start(context.Background(), spawn.ProcSpec{Dir: ownerRoot, Argv: []string{"true"}})
	if !errors.Is(err, spawn.ErrBoundarySetup) {
		t.Fatalf("Start = %v, want an error wrapping spawn.ErrBoundarySetup", err)
	}
	if !strings.Contains(err.Error(), "0710") && !strings.Contains(err.Error(), "chmod") {
		t.Errorf("halt error = %q, want it to name the chmod fix", err)
	}
}

// TestUserModeNonBoundaryRunnerFailsClosed pins the guard in applyTenant: a
// user-mode server whose runner is not an isolate.Boundary wraps it so every
// Start refuses with spawn.ErrBoundarySetup, instead of falling back to the
// serve uid. Production wraps the runner in resolveIsolation, so this is a
// wiring fault the server must refuse rather than run through.
func TestUserModeNonBoundaryRunnerFailsClosed(t *testing.T) {
	env := newUserTestEnv(t, "alice", func(c *Config) { c.Runner = newScriptRunner() })
	rt := env.srv.runtimeAt(ownerRootOf(t, env))
	_, err := rt.Runner.Start(context.Background(), spawn.ProcSpec{Dir: ownerRootOf(t, env), Argv: []string{"true"}})
	if !errors.Is(err, spawn.ErrBoundarySetup) {
		t.Fatalf("Start = %v, want an error wrapping spawn.ErrBoundarySetup", err)
	}
	if !strings.Contains(err.Error(), "boundary") {
		t.Errorf("halt error = %q, want it to name the missing tenant boundary", err)
	}
}
