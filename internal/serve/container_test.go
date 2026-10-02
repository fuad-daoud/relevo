package serve

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// readyContainerBoundary wraps runner in a container boundary whose runtime
// seams all succeed, so a served Start reaches the render without podman.
func readyContainerBoundary(t *testing.T, runner spawn.Runner) isolate.Boundary {
	t.Helper()
	b, err := isolate.Wrap(runner, isolate.ModeContainer,
		isolate.LookPath(func(string) (string, error) { return "/usr/bin/podman", nil }),
		isolate.PodmanImageExists(func(context.Context, string) error { return nil }),
		isolate.PodmanRm(func(context.Context, string) error { return nil }),
	)
	if err != nil {
		t.Fatalf("isolate.Wrap(ModeContainer): %v", err)
	}
	return b
}

// newContainerTestEnv builds a container-mode server over the script runner,
// with one enrolled client so an owner root resolves.
func newContainerTestEnv(t *testing.T, cfgOpts ...func(*Config)) *testEnv {
	t.Helper()
	gitClient := git.NewClient("git", 0, 0)
	clientDir, headSHA, _, repoID := newClientRepo(t, gitClient, "test")
	cSet, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	runner := newScriptRunner()
	cfg := Config{
		DB:             testServeDB(t),
		Root:           t.TempDir(),
		Candidates:     cSet,
		Runner:         readyContainerBoundary(t, runner),
		Git:            gitClient,
		Now:            time.Now,
		Isolation:      isolate.ModeContainer,
		IsolationImage: "relevo-builder:local",
		Audiences:      []string{testAudience},
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
	if _, err := srv.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), "", time.Now()); err != nil {
		t.Fatal(err)
	}
	return &testEnv{
		srv:       srv,
		kp:        kp,
		id:        id,
		clientDir: clientDir,
		headSHA:   headSHA,
		repoID:    repoID,
		runner:    runner,
		gitClient: gitClient,
	}
}

// TestServedRunnerRunsInContainer pins the container translation at the runner
// for the three shared spawn shapes -- round, gate and consult: each Start
// reaches the base as a podman command with the round tree, out/ and the
// owner's repo mounted, no scope, no credential, and no rusage.
func TestServedRunnerRunsInContainer(t *testing.T) {
	env := newContainerTestEnv(t)
	root := ownerRootOf(t, env)
	rt := env.srv.runtimeAt(root)
	dir, ok := env.id.Dir()
	if !ok {
		t.Fatal("malformed test client id")
	}
	repoMount := filepath.Join(env.srv.cfg.Root, "repos", dir) + ":" + filepath.Join(env.srv.cfg.Root, "repos", dir)

	shapes := []struct {
		name string
		argv []string
	}{
		{"round", []string{"claude", "-p", "plan"}},
		{"gate", []string{"sh", "-c", "relevo gate"}},
		{"consult", []string{"codex", "exec", "ask"}},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			spec := spawn.ProcSpec{
				Dir:        root,
				Argv:       shape.argv,
				Env:        []string{"RELEVO_RUNNER=api"},
				StreamPath: filepath.Join(root, shape.name+"-runner.jsonl"),
				Scope:      &spawn.ScopeSpec{Unit: "relevo-round-alice-1", CPUWeight: 100},
			}
			if _, err := rt.Runner.Start(context.Background(), spec); err != nil {
				t.Fatalf("Start: %v", err)
			}
		})
	}

	for i := range shapes {
		got := specAt(t, env.runner, i)
		if len(got.Argv) == 0 || got.Argv[0] != "podman" {
			t.Fatalf("%s Argv = %v, want argv[0] podman", shapes[i].name, got.Argv)
		}
		if got.Scope != nil {
			t.Errorf("%s Scope = %+v, want nil", shapes[i].name, got.Scope)
		}
		if got.Credential != nil {
			t.Errorf("%s Credential = %+v, want nil", shapes[i].name, got.Credential)
		}
		for _, mount := range []string{root + ":" + root, repoMount} {
			if !containsString(got.Argv, mount) {
				t.Errorf("%s Argv = %v, want volume %q", shapes[i].name, got.Argv, mount)
			}
		}
	}
	if _, ok := rt.Runner.Rusage(context.Background(), spawn.ProcHandle{PID: 1}, filepath.Join(root, "round-runner.jsonl")); ok {
		t.Errorf("container Rusage ok = true, want false")
	}
}

// TestContainerModeRefusesWithoutBoundary pins the guard in applyContainer: a
// container-mode server whose runner is not an isolate.Boundary wraps it so
// every Start refuses with spawn.ErrBoundarySetup, instead of falling back to
// the serve uid.
func TestContainerModeRefusesWithoutBoundary(t *testing.T) {
	env := newContainerTestEnv(t, func(c *Config) { c.Runner = newScriptRunner() })
	root := ownerRootOf(t, env)
	rt := env.srv.runtimeAt(root)

	_, err := rt.Runner.Start(context.Background(), spawn.ProcSpec{Dir: root, Argv: []string{"true"}})
	if !errors.Is(err, spawn.ErrBoundarySetup) {
		t.Fatalf("Start = %v, want it to wrap spawn.ErrBoundarySetup", err)
	}
	if !strings.Contains(err.Error(), "boundary") {
		t.Errorf("halt error = %q, want it to name the missing container boundary", err)
	}
}
