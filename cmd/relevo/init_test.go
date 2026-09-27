package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
)

// stubBinary writes an executable stub named name into dir.
func stubBinary(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
}

// initRoot gives a test its own HOME, XDG_CONFIG_HOME and XDG_STATE_HOME and
// returns them. Both roots are per-test: config now lives in the state root's
// database, so the #235 rule applies to a test that writes config (#235).
func initRoot(t *testing.T) (home, configHome string) {
	t.Helper()
	home = t.TempDir()
	configHome = filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	return home, configHome
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
	if L.Candidates.Len() != 3 {
		t.Errorf("stored candidates = %v, want the one builder and two planner candidates", L.Candidates.Refs())
	}
	// R5: the candidates carry no roles/tier, the policy only max_tier, and a
	// builder actor over the candidates' names is what says who serves what.
	if got := len(L.Policy.OrderFor("builder")); got != 0 {
		t.Errorf("stored order.builder has %d tokens, want none", got)
	}
	if len(L.Policy.Order) != 0 || len(L.Policy.Tier) != 0 {
		t.Errorf("stored policy order/tier = %v/%v, want none", L.Policy.Order, L.Policy.Tier)
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
	if len(builder.Candidates) != 1 {
		t.Errorf("builder candidates = %v, want the one non-claude plan name", builder.Candidates)
	}

	for _, path := range []string{
		filepath.Join(home, ".claude", "agents", "plan-executor.md"),
		filepath.Join(home, ".config", "opencode", "agents", "plan-executor.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("role file %s: %v", path, err)
		}
	}

	out := string(stdout) + string(stderr)
	if !strings.Contains(out, "wrote") {
		t.Errorf("output does not contain %q:\n%s", "wrote", out)
	}
	if !strings.Contains(out, "next:") {
		t.Errorf("output does not contain %q:\n%s", "next:", out)
	}
}

// TestInitReportsActors pins A2 round 3 S2.2: config init reports the actor it
// seeded and the names Plan derived, not the old policy order.
func TestInitReportsActors(t *testing.T) {
	initRoot(t)

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
	var parts []string
	for _, name := range []string{"builder", "planner", "lite-planner"} {
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
	if want := "wrote candidates (3: glm-5.3-flash, opus, deepseek-v4.1-flash)"; !strings.Contains(out, want) {
		t.Errorf("output does not contain %q:\n%s", want, out)
	}
}

// TestInitClaudeOnlyNotesMissingBuilder pins the claude-only case: the builder
// is written with no candidates and init says how to add one.
func TestInitClaudeOnlyNotesMissingBuilder(t *testing.T) {
	initRoot(t)

	bin := t.TempDir()
	stubBinary(t, bin, "claude")
	t.Setenv("PATH", bin)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "init"})
	})
	if err != nil {
		t.Fatalf("run init: %v (stderr: %s)", err, stderr)
	}

	out := string(stdout) + string(stderr)
	if !strings.Contains(out, "builder: none") {
		t.Errorf("output does not contain %q:\n%s", "builder: none", out)
	}
	if want := `note: no builder candidate (claude only plans); add one from a builder harness (agy, codex, opencode): relevo config set actors.builder.candidates '["<name>"]'`; !strings.Contains(out, want) {
		t.Errorf("output does not contain %q:\n%s", want, out)
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
