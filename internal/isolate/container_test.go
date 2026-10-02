package isolate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// containerSpecFixture is the container spec the argv tables share: an image,
// the owner's bare repo, and one read-only harness home.
func containerSpecFixture() ContainerSpec {
	return ContainerSpec{
		Image:    "relevo-builder:local",
		RepoRoot: "/srv/repos/ab",
		Homes:    []Mount{{Path: "/home/alice/.claude", ReadOnly: true}},
	}
}

// TestContainerArgv pins the podman render's exact argv order: the fixed run
// flags, then the mounts (round tree, out dir, repo, home, /tmp), the env, the
// bounds, the image and the in-container supervisor, then the builder argv.
func TestContainerArgv(t *testing.T) {
	spec := spawn.ProcSpec{
		Dir:        "/round/tree",
		Argv:       []string{"claude", "-p", "plan"},
		Env:        []string{"A=1"},
		LogPath:    "/binding/log",
		StreamPath: "/binding/r1-runner.jsonl",
		Scope: &spawn.ScopeSpec{
			Unit:      "relevo-round-ab-1",
			CPUWeight: 100,
			MemoryMax: "2G",
			CPUQuota:  "200%",
			TasksMax:  64,
		},
		DenyEnv: []string{"FOO"},
	}
	got := ContainerArgv(spec, containerSpecFixture())

	want := []string{
		"podman", "run", "--rm", "--userns=keep-id",
		"--name", "relevo-round-ab-1",
		"--cidfile", "/binding/r1-runner.jsonl.cid",
		"-v", "/round/tree:/round/tree",
		"-v", "/binding/out:/binding/out",
		"-v", "/srv/repos/ab:/srv/repos/ab",
		"-v", "/home/alice/.claude:/home/alice/.claude:ro",
		"--tmpfs", "/tmp",
		"--env", "A=1",
		"--memory", "2G", "--cpus", "2", "--pids-limit", "64",
		"relevo-builder:local", "/bin/sh", "-c", spawn.ContainerSupervisorScript, "relevo-supervisor", "",
		"claude", "-p", "plan",
	}
	if !reflect.DeepEqual(got.Argv, want) {
		t.Fatalf("Argv = %v,\nwant %v", got.Argv, want)
	}
	if got.Scope != nil {
		t.Errorf("Scope = %+v, want nil: container rounds run under no systemd scope", got.Scope)
	}
	if got.Credential != nil {
		t.Errorf("Credential = %+v, want nil: keep-id runs the container as the invoking user", got.Credential)
	}
	// Every field other than Argv, Scope and Credential passes through.
	if got.Dir != spec.Dir || got.LogPath != spec.LogPath || got.StreamPath != spec.StreamPath {
		t.Errorf("paths = %q/%q/%q, want the spec's unchanged", got.Dir, got.LogPath, got.StreamPath)
	}
	if !reflect.DeepEqual(got.Env, spec.Env) || !reflect.DeepEqual(got.DenyEnv, spec.DenyEnv) {
		t.Errorf("Env/DenyEnv = %v/%v, want the spec's unchanged", got.Env, got.DenyEnv)
	}
}

// TestContainerArgvNoScopeNoBounds pins the bare render: with no scope and no
// env there are no bound flags and no --env, the name falls back to the stream
// base, and the supervisor always carries the empty wanted unit.
func TestContainerArgvNoScopeNoBounds(t *testing.T) {
	spec := spawn.ProcSpec{
		Dir:        "/round/tree",
		Argv:       []string{"true"},
		StreamPath: "/binding/r2-runner.jsonl",
	}
	got := ContainerArgv(spec, containerSpecFixture())

	for _, flag := range []string{"--memory", "--cpus", "--pids-limit", "--env"} {
		if containsArg(got.Argv, flag) {
			t.Errorf("Argv = %v, want no %s", got.Argv, flag)
		}
	}
	if !containsArgPair(got.Argv, "--name", "r2-runner.jsonl") {
		t.Errorf("Argv = %v, want the sanitized stream base as --name", got.Argv)
	}
	if !containsArgPair(got.Argv, "--cidfile", "/binding/r2-runner.jsonl.cid") {
		t.Errorf("Argv = %v, want the cidfile beside the stream", got.Argv)
	}
}

// TestContainerArgvNameFallback pins the name rule: a scope unit wins when one
// is present, and characters outside the runtime's alphabet are replaced.
func TestContainerArgvNameFallback(t *testing.T) {
	c := ContainerSpec{Image: "img"}
	t.Run("scope unit", func(t *testing.T) {
		spec := spawn.ProcSpec{
			Argv:       []string{"true"},
			StreamPath: "/b/stream",
			Scope:      &spawn.ScopeSpec{Unit: "relevo-round-ab/api:1", CPUWeight: 100},
		}
		got := ContainerArgv(spec, c)
		if !containsArgPair(got.Argv, "--name", "relevo-round-ab-api-1") {
			t.Errorf("Argv = %v, want the sanitized scope unit as --name", got.Argv)
		}
	})
	t.Run("empty name falls back", func(t *testing.T) {
		spec := spawn.ProcSpec{Argv: []string{"true"}, StreamPath: "/b/..."}
		got := ContainerArgv(spec, c)
		if !containsArgPair(got.Argv, "--name", "relevo-round") {
			t.Errorf("Argv = %v, want the fixed fallback name", got.Argv)
		}
	})
}

// TestBounds pins the scope-to-flag translation: memory and pids from their
// fields, cpus from the quota as a decimal, and Slice/CPUWeight/AllowedCPUs
// dropped because podman has no equivalent.
func TestBounds(t *testing.T) {
	cases := []struct {
		name  string
		scope *spawn.ScopeSpec
		want  []string
	}{
		{"nil scope", nil, nil},
		{"empty scope", &spawn.ScopeSpec{}, nil},
		{"memory only", &spawn.ScopeSpec{MemoryMax: "2G"}, []string{"--memory", "2G"}},
		{"quota whole cores", &spawn.ScopeSpec{CPUQuota: "200%"}, []string{"--cpus", "2"}},
		{"quota fractional", &spawn.ScopeSpec{CPUQuota: "150%"}, []string{"--cpus", "1.5"}},
		{"quota below one", &spawn.ScopeSpec{CPUQuota: "50%"}, []string{"--cpus", "0.5"}},
		{"pids only", &spawn.ScopeSpec{TasksMax: 64}, []string{"--pids-limit", "64"}},
		{
			"all three",
			&spawn.ScopeSpec{MemoryMax: "1G", CPUQuota: "300%", TasksMax: 8},
			[]string{"--memory", "1G", "--cpus", "3", "--pids-limit", "8"},
		},
		{
			"dropped fields",
			&spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 100, AllowedCPUs: "0-1"},
			nil,
		},
		{"malformed quota", &spawn.ScopeSpec{CPUQuota: "abc"}, nil},
		{"non-positive quota", &spawn.ScopeSpec{CPUQuota: "0%"}, nil},
		{"no percent sign", &spawn.ScopeSpec{CPUQuota: "2"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Bounds(tc.scope)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Bounds(%+v) = %v, want %v", tc.scope, got, tc.want)
			}
		})
	}
	if got := Bounds(&spawn.ScopeSpec{CPUWeight: 250}); containsArg(got, "--cpus") {
		t.Errorf("Bounds emitted --cpus from CPUWeight: %v", got)
	}
}

// containerBoundary builds a container-mode boundary over base whose runtime
// seams are injected: lookErr and imageErr force the two prerequisites missing
// (nil means present), and rm records the ids Kill removes.
func containerBoundary(t *testing.T, base spawn.Runner, lookErr, imageErr error) (Boundary, *[]string) {
	t.Helper()
	removed := &[]string{}
	b, err := Wrap(base, ModeContainer,
		LookPath(func(string) (string, error) { return "/usr/bin/podman", lookErr }),
		PodmanImageExists(func(context.Context, string) error { return imageErr }),
		PodmanRm(func(_ context.Context, cid string) error {
			*removed = append(*removed, cid)
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Wrap(ModeContainer) error = %v, want nil", err)
	}
	return b, removed
}

// TestContainerStartRefusesMissingPodman pins the readiness probe: with podman
// absent, Start refuses with a boundary-setup error naming podman and never
// reaches the base.
func TestContainerStartRefusesMissingPodman(t *testing.T) {
	base := &stubRunner{}
	b, _ := containerBoundary(t, base, errors.New("exec: podman: not found"), nil)
	bound := b.ForContainer(containerSpecFixture())

	_, err := bound.Start(context.Background(), spawn.ProcSpec{Dir: "/round", Argv: []string{"true"}})
	if !errors.Is(err, spawn.ErrBoundarySetup) {
		t.Fatalf("Start = %v, want it to wrap spawn.ErrBoundarySetup", err)
	}
	if !strings.Contains(err.Error(), "podman") {
		t.Errorf("Start error = %q, want it to name podman", err)
	}
	if base.started != 0 {
		t.Errorf("Start reached the base %d times, want 0", base.started)
	}
}

// TestContainerStartRefusesMissingImage pins the image probe: with podman
// present but the image absent, Start refuses with a boundary-setup error
// naming the image and the Containerfile fix, never reaching the base.
func TestContainerStartRefusesMissingImage(t *testing.T) {
	base := &stubRunner{}
	b, _ := containerBoundary(t, base, nil, errors.New("no such image"))
	bound := b.ForContainer(containerSpecFixture())

	_, err := bound.Start(context.Background(), spawn.ProcSpec{Dir: "/round", Argv: []string{"true"}})
	if !errors.Is(err, spawn.ErrBoundarySetup) {
		t.Fatalf("Start = %v, want it to wrap spawn.ErrBoundarySetup", err)
	}
	if !strings.Contains(err.Error(), "relevo-builder:local") || !strings.Contains(err.Error(), "Containerfile") {
		t.Errorf("Start error = %q, want it to name the image and the Containerfile", err)
	}
	if base.started != 0 {
		t.Errorf("Start reached the base %d times, want 0", base.started)
	}
}

// TestContainerStartRunsPodman pins the ready path: with both prerequisites
// present, Start hands the base the ContainerArgv render.
func TestContainerStartRunsPodman(t *testing.T) {
	base := &stubRunner{}
	b, _ := containerBoundary(t, base, nil, nil)
	bound := b.ForContainer(containerSpecFixture())

	spec := spawn.ProcSpec{Dir: "/round", Argv: []string{"true"}, StreamPath: "/b/stream"}
	if _, err := bound.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	if base.started != 1 {
		t.Fatalf("base started %d, want 1", base.started)
	}
}

// TestContainerBoundaryRefusesUnbound pins that a container boundary with no
// spec bound refuses every Start like an unbound tenant, never reaching the
// base.
func TestContainerBoundaryRefusesUnbound(t *testing.T) {
	base := &stubRunner{}
	b, _ := containerBoundary(t, base, nil, nil)

	_, err := b.Start(context.Background(), spawn.ProcSpec{Dir: "/round", Argv: []string{"true"}})
	if !errors.Is(err, spawn.ErrBoundarySetup) {
		t.Fatalf("Start = %v, want it to wrap spawn.ErrBoundarySetup", err)
	}
	if base.started != 0 {
		t.Errorf("an unbound container boundary reached the base %d times, want 0", base.started)
	}
}

// TestContainerKillRemovesContainerFromCidfile pins the kill path: the cidfile
// names the container, the runtime removes it, the cidfile is swept, and the
// base Kill still runs.
func TestContainerKillRemovesContainerFromCidfile(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "r1-runner.jsonl")
	cidPath := stream + ".cid"
	if err := os.WriteFile(cidPath, []byte("abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := &stubRunner{alive: true, killErr: errors.New("base-kill")}
	b, removed := containerBoundary(t, base, nil, nil)
	bound := b.ForContainer(containerSpecFixture())

	err := bound.Kill(context.Background(), spawn.ProcHandle{PID: 7}, stream)
	if !reflect.DeepEqual(*removed, []string{"abc123"}) {
		t.Errorf("podman rm saw %v, want [abc123]", *removed)
	}
	if _, statErr := os.Stat(cidPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("cidfile still present after Kill (stat err %v)", statErr)
	}
	if err == nil || err.Error() != "base-kill" {
		t.Errorf("Kill = %v, want the base's error: the base Kill must still run", err)
	}
}

// TestContainerKillToleratesStaleCidfile pins the stale and missing cases: a
// cidfile whose runtime removal fails, and no cidfile at all, are both not
// errors and never block the base Kill.
func TestContainerKillToleratesStaleCidfile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	t.Run("stale cidfile", func(t *testing.T) {
		stream := filepath.Join(dir, "stale.jsonl")
		if err := os.WriteFile(stream+".cid", []byte("gone"), 0o644); err != nil {
			t.Fatal(err)
		}
		base := &stubRunner{alive: true, killErr: errors.New("base-kill")}
		b, err := Wrap(base, ModeContainer,
			LookPath(func(string) (string, error) { return "/usr/bin/podman", nil }),
			PodmanImageExists(func(context.Context, string) error { return nil }),
			PodmanRm(func(context.Context, string) error { return errors.New("no such container") }),
		)
		if err != nil {
			t.Fatal(err)
		}
		if got := b.ForContainer(containerSpecFixture()).Kill(ctx, spawn.ProcHandle{PID: 7}, stream); got == nil || got.Error() != "base-kill" {
			t.Errorf("Kill = %v, want the base's error despite the stale cidfile", got)
		}
		if _, statErr := os.Stat(stream + ".cid"); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("stale cidfile not swept")
		}
	})

	t.Run("missing cidfile", func(t *testing.T) {
		stream := filepath.Join(dir, "missing.jsonl")
		base := &stubRunner{alive: true, killErr: errors.New("base-kill")}
		b, _ := containerBoundary(t, base, nil, nil)
		if got := b.ForContainer(containerSpecFixture()).Kill(ctx, spawn.ProcHandle{PID: 7}, stream); got == nil || got.Error() != "base-kill" {
			t.Errorf("Kill with no cidfile = %v, want the base's error", got)
		}
	})
}

// TestContainerRusageReportsUnavailable pins that a bound container boundary
// reports no rusage rather than a faked measurement, and that a none boundary
// still delegates.
func TestContainerRusageReportsUnavailable(t *testing.T) {
	ctx := context.Background()
	base := &stubRunner{rusage: spawn.ProcRusage{CPUMS: 9, PeakMemBytes: 10}, rusageOK: true}
	b, _ := containerBoundary(t, base, nil, nil)

	if ru, ok := b.ForContainer(containerSpecFixture()).Rusage(ctx, spawn.ProcHandle{}, "stream"); ok || ru != (spawn.ProcRusage{}) {
		t.Errorf("container Rusage = (%+v, %v), want (zero, false)", ru, ok)
	}

	none, err := Wrap(base, ModeNone)
	if err != nil {
		t.Fatal(err)
	}
	if ru, ok := none.Rusage(ctx, spawn.ProcHandle{}, "stream"); !ok || ru.CPUMS != 9 {
		t.Errorf("none Rusage = (%+v, %v), want the base's rusage", ru, ok)
	}
}

// containsArg reports whether args holds want as an element.
func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// containsArgPair reports whether args holds flag immediately followed by value.
func containsArgPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
