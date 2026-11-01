package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
)

// stubBinary writes an executable stub named name into dir. A claude stub
// answers the one probe the starter configuration asks (`claude auth
// status` printing its loggedIn JSON), so a test's init sees a probed
// lineup; everything else exits 0. A stub is not a real harness: no harness
// CLI, no network, no daemon.
func stubBinary(t *testing.T, dir, name string) {
	t.Helper()
	body := "#!/bin/sh\nexit 0\n"
	if name == "claude" {
		body = "#!/bin/sh\nif [ \"$1\" = auth ] && [ \"$2\" = status ]; then\n  printf '{\"loggedIn\": true}\\n'\nfi\nexit 0\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
}

// stubLoggedOutClaude writes a claude stub whose status says not logged in,
// for a test of the nothing-logged-in refusal.
func stubLoggedOutClaude(t *testing.T, dir string) {
	t.Helper()
	body := "#!/bin/sh\nprintf '{\"loggedIn\": false}\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(body), 0o755); err != nil {
		t.Fatalf("write logged-out stub: %v", err)
	}
}

// initRoot gives a test its own HOME, XDG_CONFIG_HOME and XDG_STATE_HOME and
// returns them. Both roots are per-test: config lives in the state root's
// database, so a test that writes config touches neither the user's state nor
// TestMain's root.
func initRoot(t *testing.T) (home, configHome string) {
	t.Helper()
	home = t.TempDir()
	configHome = filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	return home, configHome
}

// stubbedProbedRoot gives init a machine where claude (and, when named, any
// further kind) stands on PATH with a probed-able stub.
func stubbedProbedRoot(t *testing.T, names ...string) {
	t.Helper()
	initRoot(t)
	bin := t.TempDir()
	for _, name := range names {
		stubBinary(t, bin, name)
	}
	t.Setenv("PATH", bin)
}

// storedConfig loads the sections a command just wrote back through the
// runtime's own config store.
func storedConfig(t *testing.T) config.Loaded {
	t.Helper()
	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	L, err := rt.Config.Load()
	if err != nil {
		t.Fatalf("Config.Load: %v", err)
	}
	return L
}

// TestInitWritesConfigAndRoles pins what init writes through the probed
// lineup: the candidates the recommendations name, the policy only at
// max_tier, and a builder actor at the yolo tier. There is no roles key on a
// candidate and no model of the writer's choice; actors decide.
func TestInitWritesConfigAndRoles(t *testing.T) {
	home, _ := initRoot(t)

	bin := t.TempDir()
	stubBinary(t, bin, "claude")
	stubBinary(t, bin, "opencode")
	t.Setenv("PATH", bin)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "init"})
	})
	if err != nil {
		t.Fatalf("run init: %v (stderr: %s)", err, stderr)
	}

	L := storedConfig(t)
	// claude's sonnet and opus; opencode adds only models its question
	// picked, and init asks none.
	if L.Candidates.Len() != 2 {
		t.Errorf("stored candidates = %v, want the two probed (sonnet, opus)", L.Candidates.Refs())
	}
	if got := len(L.Policy.OrderFor("builder")); got != 0 {
		t.Errorf("stored order.builder has %d tokens, want none", got)
	}
	if len(L.Policy.Order) != 0 || len(L.Policy.Tier) != 0 || L.Policy.MaxTier != "yolo" {
		t.Errorf("stored policy order/tier/max_tier = %v/%v/%q, want none/none/yolo", L.Policy.Order, L.Policy.Tier, L.Policy.MaxTier)
	}
	if got := L.Candidates.ForRole("builder"); len(got) != 0 {
		t.Errorf("candidates serving builder = %v, want none: actors decide now", got)
	}
	builder, ok := L.Actors["builder"]
	if !ok {
		t.Fatalf("stored actors = %v, want a builder", L.Actors)
	}
	if builder.Agent != "plan-executor" || builder.Tier != "yolo" {
		t.Errorf("builder actor = %+v, want plan-executor at tier yolo", builder)
	}

	for _, path := range []string{
		filepath.Join(home, ".claude", "agents", "plan-executor.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("role file %s: %v", path, err)
		}
	}
	opencodeRole := filepath.Join(home, ".config", "opencode", "agents", "plan-executor.md")
	if _, err := os.Stat(opencodeRole); err != nil {
		t.Errorf("opencode role file: %v; opencode is on PATH, so its agents install", err)
	}

	out := string(stdout) + string(stderr)
	if !strings.Contains(out, "wrote") {
		t.Errorf("output does not contain %q:\n%s", "wrote", out)
	}
	if !strings.Contains(out, "next:") {
		t.Errorf("output does not contain %q:\n%s", "next:", out)
	}
}

// TestInitReportsActors pins what init reports: the actors it seeded in
// chain order with the names the probe's recommendations derived.
func TestInitReportsActors(t *testing.T) {
	stubbedProbedRoot(t, "claude", "opencode")

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "init"})
	})
	if err != nil {
		t.Fatalf("run init: %v (stderr: %s)", err, stderr)
	}

	L := storedConfig(t)
	var parts []string
	for _, name := range []string{"builder", "librarian", "planner", "reviewer", "security", "lite-planner"} {
		a, ok := L.Actors[name]
		if !ok {
			t.Fatalf("stored actors = %v, want a %s", L.Actors, name)
		}
		names := make([]string, 0, len(a.Candidates))
		for _, e := range a.Candidates {
			names = append(names, e.Candidate)
		}
		parts = append(parts, name+": "+strings.Join(names, ", "))
	}
	want := "wrote actors (" + strings.Join(parts, "; ") + ")"
	out := string(stdout) + string(stderr)
	if !strings.Contains(out, want) {
		t.Errorf("output does not contain %q:\n%s", want, out)
	}
	if want := "wrote candidates (2: sonnet, opus)"; !strings.Contains(out, want) {
		t.Errorf("output does not contain %q:\n%s", want, out)
	}
}

// TestInitPrintsTierLine pins init's tier notice: the seeded tier, plainly
// named, with the way to restrict it.
func TestInitPrintsTierLine(t *testing.T) {
	stubbedProbedRoot(t, "claude")

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "init"})
	})
	if err != nil {
		t.Fatalf("run init: %v (stderr: %s)", err, stderr)
	}
	want := "tier: yolo (builders run commands without asking) -- relevo config set policy.max_tier edit to restrict"
	out := string(stdout) + string(stderr)
	if !strings.Contains(out, want) {
		t.Errorf("output does not contain the tier line %q:\n%s", want, out)
	}
}

// TestInitNothingLoggedInIsNotAvailable pins the nothing-logged-in refusal:
// config init writes nothing, names every reachable login, and points back
// at the wizard.
func TestInitNothingLoggedInIsNotAvailable(t *testing.T) {
	initRoot(t)

	bin := t.TempDir()
	stubLoggedOutClaude(t, bin)
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write codex stub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bin, "agy"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write agy stub: %v", err)
	}
	t.Setenv("PATH", bin)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"config", "init"})
	})
	if err == nil {
		t.Fatal("init with nothing logged in: want error, got nil")
	}
	_ = requireCLIError(t, err, codeNotAvailable, "relevo setup")
	if !strings.Contains(err.Error(), "not_available") {
		t.Fatalf("init error = %v, want the not_available code", err)
	}
	for _, login := range []string{"claude auth login", "codex login", "run agy once to sign in"} {
		if !strings.Contains(err.Error(), login) {
			t.Errorf("refusal %q does not name %q", err, login)
		}
	}
	if !strings.Contains(err.Error(), "log in with one of:") {
		t.Errorf("refusal %q does not name the way in", err)
	}
	L := storedConfig(t)
	if L.Candidates.Len() != 0 {
		t.Errorf("candidates written despite the refusal: %v", L.Candidates.Refs())
	}
}

func TestInitRefusesOverwriteWithoutForce(t *testing.T) {
	_, _ = initRoot(t)

	bin := t.TempDir()
	stubBinary(t, bin, "claude")
	t.Setenv("PATH", bin)

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "init"})
	}); err != nil {
		t.Fatalf("first init: %v (stderr: %s)", err, stderr)
	}

	_, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "init"})
	})
	if err == nil {
		t.Fatal("second init without --force: want error, got nil")
	}
	if !strings.Contains(err.Error(), "--force") && !strings.Contains(string(stderr), "--force") {
		t.Fatalf("second init error %v does not mention --force (stderr: %s)", err, stderr)
	}

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "init", "--force"})
	}); err != nil {
		t.Fatalf("init --force: %v (stderr: %s)", err, stderr)
	}

	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	ok, err := rt.Config.Has(config.Candidates)
	if err != nil {
		t.Fatalf("Has(candidates): %v", err)
	}
	if !ok {
		t.Error("candidates section missing after init --force")
	}
}

func TestInitNoBinariesExits1(t *testing.T) {
	initRoot(t)

	t.Setenv("PATH", t.TempDir())

	_, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "init"})
	})
	if err == nil {
		t.Fatal("init with no binaries on PATH: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no harness binaries") {
		t.Fatalf("init error %v does not mention %q (stderr: %s)", err, "no harness binaries", stderr)
	}
	if strings.Contains(string(stderr), "not_available") {
		t.Errorf("no-binaries refusal carries not_available:\n%s", stderr)
	}
}

func TestInitNoAgentsSkipsInstall(t *testing.T) {
	home, _ := initRoot(t)

	bin := t.TempDir()
	stubBinary(t, bin, "claude")
	t.Setenv("PATH", bin)

	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "init", "--no-agents"})
	}); err != nil {
		t.Fatalf("init --no-agents: %v (stderr: %s)", err, stderr)
	}

	if _, err := os.Stat(filepath.Join(home, ".claude", "agents", "plan-executor.md")); err == nil {
		t.Fatal("plan-executor.md written despite --no-agents")
	}
}
