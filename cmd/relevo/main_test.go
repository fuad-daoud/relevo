package main

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// TestMain points the whole package at a fresh temp root: HOME,
// XDG_CONFIG_HOME, XDG_STATE_HOME and XDG_DATA_HOME all move here, and the
// harness-identity and override variables are unset, so no test in cmd/relevo
// reads the user's real config, state or data, nor sees the calling harness
// (#235, #463). Tests that t.Setenv the same variables keep working: t.Setenv
// restores to these values.
//
// It also clears the mastermind identity a harness injects into the shell that
// runs the tests (a Claude Code session, an agy conversation, an OpenCode shell
// marked by the relevo plugin's server hook), so no test resolves the mastermind
// of whoever happens to run `go test`. isolateTestEnv holds the rule; a test
// that needs one of these variables sets it itself.
func TestMain(m *testing.M) {
	// The one-opener child answers before any isolation: it must keep the
	// parent's state root so it dials the parent's in-process owner.
	if os.Getenv(cmdTestHelperEnv) != "" {
		os.Exit(runCmdTestHelper())
	}
	// The daemon-runtime child keeps the parent's state root for the same
	// reason: it opens the database the parent points it at.
	if os.Getenv(cmdTestDaemonHelperEnv) != "" {
		os.Exit(runCmdTestDaemonHelper())
	}

	root, err := os.MkdirTemp("", "relevo-cmd-test-")
	if err != nil {
		panic(err)
	}
	isolateTestEnv(root)
	// The production route stays off under `go test`: the tests that call run()
	// keep opening the machine database directly, and a test that wants the
	// switch installs it itself.
	dbRouteFromArgs = false
	installDBRoute(routeNone, verbDialBudget, 0)
	cleanup, err := dbtest.Install()
	if err != nil {
		panic(err)
	}
	code := m.Run()
	cleanup()
	os.RemoveAll(root)
	os.Exit(code)
}

// isolateTestEnv makes the package start from CI's environment whatever
// harness runs `go test` (#463). It points HOME, XDG_CONFIG_HOME,
// XDG_STATE_HOME and XDG_DATA_HOME at root, a temp directory, so no test reads
// the user's real config, state or data (#235). It then unsets the
// harness-identity and override variables, so a test never detects the calling
// harness or picks up the caller's overrides; it unsets a variable when its
// name is CLAUDECODE or TYPESAFE_API_KEY, or starts with CLAUDE_, RELEVO_ or
// ANTIGRAVITY_. It never fails: it ignores os.Setenv and os.Unsetenv errors,
// as TestMain did before.
func isolateTestEnv(root string) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if matchesUnsetRule(name) {
			os.Unsetenv(name)
		}
	}
	os.Setenv("HOME", root)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	os.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	os.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
}

// matchesUnsetRule reports whether isolateTestEnv unsets the named variable.
func matchesUnsetRule(name string) bool {
	return name == "CLAUDECODE" ||
		name == "TYPESAFE_API_KEY" ||
		strings.HasPrefix(name, "CLAUDE_") ||
		strings.HasPrefix(name, "RELEVO_") ||
		strings.HasPrefix(name, "ANTIGRAVITY_")
}

// TestIsolateTestEnv pins isolateTestEnv: it must not see the caller's harness
// or overrides, but must leave near-miss names and the build's own variables
// alone. It is a pure environment test; it reaches no harness and no network.
func TestIsolateTestEnv(t *testing.T) {
	// t.Setenv restores the value from before its call, so every variable
	// isolateTestEnv changes must be t.Setenv'd first.
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(name, "/polluted")
	}
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_ENV_FILE", "/polluted/env")
	t.Setenv("CLAUDE_PID", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "x")
	t.Setenv("RELEVO_HARNESS", "opencode")
	t.Setenv("RELEVO_MASTERMIND", "p")
	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "x")
	t.Setenv("TYPESAFE_API_KEY", "k")
	t.Setenv("CLAUDEX_KEEP", "1") // near-miss: no underscore after CLAUDE
	t.Setenv("RELEVO", "1")       // near-miss: no trailing underscore

	root := t.TempDir()
	isolateTestEnv(root)

	for name, want := range map[string]string{
		"HOME":            root,
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"XDG_STATE_HOME":  filepath.Join(root, "state"),
		"XDG_DATA_HOME":   filepath.Join(root, "data"),
	} {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	for _, name := range []string{
		"CLAUDECODE", "CLAUDE_ENV_FILE", "CLAUDE_PID", "CLAUDE_CODE_SESSION_ID",
		"RELEVO_HARNESS", "RELEVO_MASTERMIND", "RELEVO_PLANNER",
		"ANTIGRAVITY_CONVERSATION_ID", "TYPESAFE_API_KEY",
	} {
		if _, ok := os.LookupEnv(name); ok {
			t.Errorf("%s must be unset by isolateTestEnv", name)
		}
	}

	for _, name := range []string{"CLAUDEX_KEEP", "RELEVO"} {
		if _, ok := os.LookupEnv(name); !ok {
			t.Errorf("%s must survive isolateTestEnv", name)
		}
	}

	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if matchesUnsetRule(name) {
			t.Errorf("%s matches the unset rule but is still present", name)
		}
	}
}

// TestHelpListsServeVerbs pins that the top-level usage names the server
// administration verbs and the gate verb that replaced `serve gates`,
// `serve available` and `serve unavailable` (§4.3). `relevo help` only prints
// a constant string, so this reaches no harness and touches no state.
func TestHelpListsServeVerbs(t *testing.T) {
	stdout, _, runErr := captureOutput(t, func() error {
		return run([]string{"help"})
	})
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	for _, want := range []string{"serve init", "gate --serve"} {
		if !strings.Contains(string(stdout), want) {
			t.Errorf("expected the top-level usage to mention %q, got %q", want, string(stdout))
		}
	}
}

// TestUpdateHelp pins `relevo update`'s discovery: the top-level usage names
// it, and `update -h` prints its own usage line and returns errHelpShown,
// which main turns into exit 0. It reaches no network and needs no harness.
func TestUpdateHelp(t *testing.T) {
	for _, want := range []string{"update", "--release"} {
		if !strings.Contains(usage, want) {
			t.Errorf("the usage constant must mention %q", want)
		}
	}

	_, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"update", "-h"})
	})
	if !errors.Is(runErr, errHelpShown) {
		t.Fatalf("run(update -h) = %v, want errHelpShown", runErr)
	}
	const line = "usage: relevo update [--check] [--to vX.Y.Z] [--release]"
	if !strings.Contains(string(stderr), line) {
		t.Errorf("update -h stderr = %q, want it to contain %q", string(stderr), line)
	}
}

// TestBindResumeWithoutNameIsRejected covers a flag shape that reads fine and
// silently does the wrong thing: --resume is a bool and the name comes from
// --name, so `relevo bind --resume webshop` drops the positional and the binding
// lookup then fails on the empty name ("relevo: : binding not found").
//
// The check runs before any runtime is built, so this test touches neither
// the state directory nor a harness.
func TestBindResumeWithoutNameIsRejected(t *testing.T) {
	err := run([]string{"bind", "--resume", "webshop"})
	if err == nil {
		t.Fatal("relevo bind --resume with a positional name must be rejected")
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("error must point at --name, got %q", err)
	}
}

// TestSendDryRunRequiresFile pins that `relevo send --dry-run` without a plan
// file is refused before a runtime is built, so a CI runner with no harness
// still fails on the missing flag rather than on the environment.
func TestSendDryRunRequiresFile(t *testing.T) {
	err := run([]string{"send", "--dry-run", "--name", "x"})
	if err == nil {
		t.Fatal("relevo send --dry-run without --file must be rejected")
	}
	if !strings.Contains(err.Error(), "--file") {
		t.Errorf("error must point at --file, got %q", err)
	}
}

// TestAskIsGone pins the removed verb's stub: `relevo ask` returns the
// usage-coded refusal naming the replacement workflow, and exits 2 through the
// code bridge. It dispatches through run -- parsing only, so nothing is
// spawned and no runtime is built.
func TestAskIsGone(t *testing.T) {
	initRoot(t)

	err := run([]string{"ask", "--actor", "reviewer", "--file", "q.md"})
	ce := requireCLIError(t, err, codeUsage, "relevo bind --actor reviewer")
	want := "relevo ask is gone: bind a reader actor (relevo bind --actor reviewer) and send it a plan"
	if !strings.Contains(ce.message, want) {
		t.Errorf("message = %q, want it to contain %q", ce.message, want)
	}
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("relevo ask: run = %v, want exit code 2", err)
	}
}

// TestSendRegateNegativeIsRejected pins #132 part 2's flag validation. The
// flag's -1 default means "not given", so a negative value the human typed is
// a bad value, not an omission: it exits 2, and the check runs before any
// runtime is built, so this touches neither the state directory nor a harness.
func TestSendRegateNegativeIsRejected(t *testing.T) {
	stdout, _, runErr := captureOutput(t, func() error {
		return run([]string{"send", "--regate", "-1", "--file", "plan.md"})
	})

	ce := requireCLIError(t, runErr, codeUsage, "relevo help")
	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if len(stdout) != 0 {
		t.Errorf("expected nothing on stdout, got %q", string(stdout))
	}
	if !strings.Contains(ce.message, "--regate") {
		t.Errorf("expected the error to name --regate, got %q", ce.message)
	}
}

// TestSendVerifyAndNoVerifyAreExclusive pins #144's flag pair: like add's
// --branch/--cwd, the refusal happens in validation, before newRuntime, so it
// reaches neither the state directory nor a harness.
func TestSendVerifyAndNoVerifyAreExclusive(t *testing.T) {
	_, _, runErr := captureOutput(t, func() error {
		return run([]string{"send", "--verify", "--no-verify", "--file", "plan.md"})
	})

	ce := requireCLIError(t, runErr, codeRefused, "")
	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", runErr)
	}
	if !strings.Contains(ce.message, "--verify") || !strings.Contains(ce.message, "--no-verify") {
		t.Errorf("expected the error to name both flags, got %q", ce.message)
	}
}

func TestStopRefusesToGuessTheBinding(t *testing.T) {
	err := run([]string{"stop"})
	if err == nil {
		t.Fatal("relevo stop with no binding must be refused")
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("error must point at --name, got %q", err)
	}
}

// TestStopGraceMustBePositive pins #138's flag validation: --grace <= 0 is a
// bad value, not an omission, so it exits 2. The check runs before any runtime
// is built, so this touches neither the state directory nor a harness.
func TestExplicitBindingNeverGuesses(t *testing.T) {
	cases := []struct {
		name       string
		flag       string
		positional []string
		want       string
		ok         bool
	}{
		{"flag only", "ai", nil, "ai", true},
		{"positional only", "", []string{"ai"}, "ai", true},
		{"bare invocation is refused", "", nil, "", false},
		{"both at once is refused", "ai", []string{"ai"}, "", false},
		{"two positionals refused", "", []string{"ai", "webshop"}, "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := explicitBinding(c.flag, c.positional)
			if ok != c.ok || got != c.want {
				t.Errorf("explicitBinding(%q, %v) = (%q, %v), want (%q, %v)",
					c.flag, c.positional, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestDiffHelp(t *testing.T) {
	err := run([]string{"show", "-h"})
	if !errors.Is(err, errHelpShown) {
		t.Fatalf("got %v, want errHelpShown", err)
	}
}

func TestDiffCommand(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))

	repoDir := t.TempDir()
	// Init git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "file.txt")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	// -c commit.gpgsign=false so a developer with signing enabled globally
	// does not have this fixture commit reach gpg; see internal/git's runGit.
	cmd = exec.Command("git", "-c", "user.name=T", "-c", "user.email=t@e",
		"-c", "commit.gpgsign=false", "commit", "-m", "init")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	patchContent := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1,2 @@\n hello\n+world\n"

	s := store.New(filepath.Join(tempHome, ".local", "state", "relevo"))
	b := store.Binding{
		Name:  "webshop",
		CWD:   repoDir,
		Round: 2, // Round 1 completed
		State: store.StateActive,
	}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}

	// Author round 1's diff patch as a round_file row.
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile("webshop", 1, s.DiffPath("webshop", 1), []byte(patchContent))
	}); err != nil {
		t.Fatal(err)
	}

	// Add log entries for round 1
	if err := s.AppendLog("webshop", store.LogEntry{
		Round:     1,
		Direction: store.DirToMasterMind,
		Kind:      store.KindDiff,
		Note:      "1 file, +1 -0",
		Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}

	// Test default round output (round 1)
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := run([]string{"show", "webshop", "--diff"})

	w.Close()
	os.Stdout = origStdout
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("run diff: %v", runErr)
	}
	if string(out) != patchContent {
		t.Fatalf("got stdout %q, want %q", string(out), patchContent)
	}

	// Verify patch reaches stdout byte-for-byte by piping into git apply --check
	applyCmd := exec.Command("git", "apply", "--check")
	applyCmd.Dir = repoDir
	applyCmd.Stdin = strings.NewReader(string(out))
	if applyOut, err := applyCmd.CombinedOutput(); err != nil {
		t.Fatalf("git apply --check failed: %v\nOutput: %s", err, string(applyOut))
	}

	// #143: a successful `diff` stamps the binding's viewed mark.
	// Store-only -- reaches no harness.
	if _, ok := s.ViewedAt("webshop"); !ok {
		t.Fatal("diff must stamp .viewed on a successful print")
	}

	// Test --stat flag
	r2, w2, _ := os.Pipe()
	os.Stdout = w2
	runErr = run([]string{"show", "webshop", "--diff", "--stat"})
	w2.Close()
	os.Stdout = origStdout
	outStat, _ := io.ReadAll(r2)
	if runErr != nil {
		t.Fatalf("run diff --stat: %v", runErr)
	}
	if strings.TrimSpace(string(outStat)) != "1 file, +1 -0" {
		t.Fatalf("got stat %q, want %q", strings.TrimSpace(string(outStat)), "1 file, +1 -0")
	}

	// Test unknown round errors naming binding and round
	errUnknown := run([]string{"show", "webshop", "--diff", "--round", "99"})
	if errUnknown == nil {
		t.Fatal("expected error for unknown round")
	}
	if !strings.Contains(errUnknown.Error(), "99") || !strings.Contains(errUnknown.Error(), "webshop") {
		t.Fatalf("error %q must name round and binding", errUnknown.Error())
	}

	// Test binding with no completed round yet
	b.Round = 1
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	errNoCompleted := run([]string{"show", "webshop", "--diff"})
	if errNoCompleted == nil {
		t.Fatal("expected error for binding with no completed round")
	}
	if !strings.Contains(errNoCompleted.Error(), "no completed round yet") || !strings.Contains(errNoCompleted.Error(), "webshop") {
		t.Fatalf("error %q must name binding and explain no completed round", errNoCompleted.Error())
	}
}

// TestDiffAnchorsCommand pins `relevo show --diff --anchors`: it store-only seeds a
// binding and a round 1 diff the way TestDiffCommand does, so it reaches no
// a harness, and asserts the printed patch carries the path:line gutter
// internal/patch's Annotate produces.
func TestDiffAnchorsCommand(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))

	patchContent := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1,2 @@\n hello\n+world\n"

	s := store.New(filepath.Join(tempHome, ".local", "state", "relevo"))
	b := store.Binding{
		Name:  "webshop",
		CWD:   "/repo",
		Round: 2, // Round 1 completed
		State: store.StateActive,
	}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile("webshop", 1, s.DiffPath("webshop", 1), []byte(patchContent))
	}); err != nil {
		t.Fatal(err)
	}

	stdout, _, runErr := captureOutput(t, func() error {
		return run([]string{"show", "webshop", "--diff", "--anchors"})
	})
	if runErr != nil {
		t.Fatalf("run diff --anchors: %v", runErr)
	}

	want := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\nfile.txt:1  @@ -1 +1,2 @@\nfile.txt:1  hello\nfile.txt:2 +world\n"
	if string(stdout) != want {
		t.Fatalf("got stdout %q, want %q", string(stdout), want)
	}
}

func TestDiffDriftCommand(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))

	repoDir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	driftPatchRound2 := "diff --git a/drift.txt b/drift.txt\n--- a/drift.txt\n+++ b/drift.txt\n@@ -1 +1,2 @@\n drift\n+round2\n"
	driftPatchRound1 := "diff --git a/drift.txt b/drift.txt\n--- a/drift.txt\n+++ b/drift.txt\n@@ -1 +1,2 @@\n drift\n+round1\n"

	s := store.New(filepath.Join(tempHome, ".local", "state", "relevo"))
	b := store.Binding{
		Name:  "webshop",
		CWD:   repoDir,
		Round: 2,
		State: store.StateActive,
	}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}

	// Author round 2's drift patch as a round_file row.
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile("webshop", 2, s.DriftPath("webshop", 2), []byte(driftPatchRound2))
	}); err != nil {
		t.Fatal(err)
	}
	// Author round 1's drift patch as a round_file row.
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile("webshop", 1, s.DriftPath("webshop", 1), []byte(driftPatchRound1))
	}); err != nil {
		t.Fatal(err)
	}

	// Add KindDrift log entry for round 2
	if err := s.AppendLog("webshop", store.LogEntry{
		Round:     2,
		Direction: store.DirToMasterMind,
		Kind:      store.KindDrift,
		Note:      "1 file, +5 -1",
		Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}

	// 1. --drift defaults to the current round (round 2)
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := run([]string{"show", "webshop", "--drift"})

	w.Close()
	os.Stdout = origStdout
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("run diff --drift: %v", runErr)
	}
	if string(out) != driftPatchRound2 {
		t.Fatalf("got stdout %q, want round 2 drift %q", string(out), driftPatchRound2)
	}

	// 2. --round overrides (requests round 1)
	rRound, wRound, _ := os.Pipe()
	os.Stdout = wRound
	runErr = run([]string{"show", "webshop", "--drift", "--round", "1"})
	wRound.Close()
	os.Stdout = origStdout
	outRound, _ := io.ReadAll(rRound)
	if runErr != nil {
		t.Fatalf("run diff --drift --round 1: %v", runErr)
	}
	if string(outRound) != driftPatchRound1 {
		t.Fatalf("got stdout %q, want round 1 drift %q", string(outRound), driftPatchRound1)
	}

	// 3. --stat prints the KindDrift Note
	rStat, wStat, _ := os.Pipe()
	os.Stdout = wStat
	runErr = run([]string{"show", "webshop", "--drift", "--stat"})
	wStat.Close()
	os.Stdout = origStdout
	outStat, _ := io.ReadAll(rStat)
	if runErr != nil {
		t.Fatalf("run diff --drift --stat: %v", runErr)
	}
	if strings.TrimSpace(string(outStat)) != "1 file, +5 -1" {
		t.Fatalf("got stat %q, want %q", strings.TrimSpace(string(outStat)), "1 file, +5 -1")
	}

	// 4. a round with no drift errors mentioning --drift
	errNoDrift := run([]string{"show", "webshop", "--drift", "--round", "99"})
	if errNoDrift == nil {
		t.Fatal("expected error for round with no drift")
	}
	if !strings.Contains(errNoDrift.Error(), "--drift") || !strings.Contains(errNoDrift.Error(), "99") || !strings.Contains(errNoDrift.Error(), "webshop") {
		t.Fatalf("error %q must name --drift, round 99, and webshop", errNoDrift.Error())
	}

	errNoDriftStat := run([]string{"show", "webshop", "--drift", "--stat", "--round", "99"})
	if errNoDriftStat == nil {
		t.Fatal("expected error for --stat on round with no drift")
	}
	if !strings.Contains(errNoDriftStat.Error(), "--drift") || !strings.Contains(errNoDriftStat.Error(), "99") || !strings.Contains(errNoDriftStat.Error(), "webshop") {
		t.Fatalf("error %q must name --drift, round 99, and webshop", errNoDriftStat.Error())
	}
}

// TestCandidatesConfigHome pins #42: relevo config is composed through
// userConfigRoot(), so XDG_CONFIG_HOME decides where candidates.json is read.
func TestCandidatesConfigHome(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	relevoDir := filepath.Join(configHome, "relevo")
	if err := os.MkdirAll(relevoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `[{"harness":"claude","provider":"test","model":"m","roles":["builder"]}]`
	if err := os.WriteFile(filepath.Join(relevoDir, "candidates.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	if _, err := rt.Candidates.Lookup(candidate.Ref{Harness: "claude", Provider: "test", Model: "m"}); err != nil {
		t.Fatalf("rt.Candidates.Lookup: %v", err)
	}
}

// TestDiffHonoursConfigHome pins #235: cmdDiff's newRuntime() reads
// candidates.json through userConfigRoot(), so XDG_CONFIG_HOME decides which
// file it reads. A candidates file the branch rejects must make `relevo show --diff`
// fail with that rejection, exactly the way the two diff tests used to fail
// when they picked the file up from the real home. (An unknown harness no
// longer rejects the load: #372 skips it with a warning, so this uses a bad
// tree instead.)
func TestDiffHonoursConfigHome(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	relevoDir := filepath.Join(configHome, "relevo")
	if err := os.MkdirAll(relevoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `[{"harness":"claude","provider":"p","model":"m","roles":["builder"],"tree":"sideways"}]`
	if err := os.WriteFile(filepath.Join(relevoDir, "candidates.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"show", "anything", "--diff"})
	if err == nil {
		t.Fatal("diff must fail when XDG_CONFIG_HOME names an invalid candidate")
	}
	if !strings.Contains(err.Error(), `tree must be "binding" or "none"`) {
		t.Fatalf("diff must read candidates from XDG_CONFIG_HOME; got %v", err)
	}
}

// TestDaemonCheckLeavesNoDB pins #372 §4.5: the plugin's `relevo daemon --check`
// probe runs before the daemon opens (and migrates) the database, so it leaves
// no relevo.db in a fresh state root. cmdDaemon returns exitCodeErr{1} instead
// of calling os.Exit, which main maps to the same silent exit status.
func TestDaemonCheckLeavesNoDB(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	err := cmdDaemon([]string{"--check"})
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 1 {
		t.Fatalf("cmdDaemon --check error = %v, want exitCodeErr{code: 1} with no daemon running", err)
	}

	dbPath := filepath.Join(root, "state", "relevo", "relevo.db")
	if _, serr := os.Stat(dbPath); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("relevo.db exists after --check: stat error = %v, want not-exist", serr)
	}
}

// TestDaemonSecondStartLeavesNoDB pins the lock-first start (#701): a daemon
// that loses AcquireDaemonLock exits before newRuntime, so it mints no
// installation.json and opens no relevo.db (nor its -wal/-shm), leaving only
// the .daemon.lock its own acquisition touched. The test holds the lock first,
// then calls cmdDaemon directly -- never run, which would need a harness and
// the network.
func TestDaemonSecondStartLeavesNoDB(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	stateRoot := filepath.Join(root, "state", "relevo")
	lock, err := store.New(stateRoot).AcquireDaemonLock()
	if err != nil {
		t.Fatalf("AcquireDaemonLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })

	err = cmdDaemon(nil)
	if !errors.Is(err, store.ErrDaemonRunning) {
		t.Fatalf("cmdDaemon error = %v, want store.ErrDaemonRunning", err)
	}

	for _, name := range []string{"relevo.db", "relevo.db-wal", "relevo.db-shm", "installation.json"} {
		p := filepath.Join(stateRoot, name)
		if _, serr := os.Stat(p); !errors.Is(serr, os.ErrNotExist) {
			t.Errorf("%s exists after a losing start: stat error = %v, want not-exist", p, serr)
		}
	}
}

// TestDefaultServeRoot pins the root `relevo gate --clear` reads the serve
// pointer from (#372 §4.6): the serve root under the state root, not the
// client root's daemon.json.
func TestDefaultServeRoot(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	got, err := defaultServeRoot()
	if err != nil {
		t.Fatalf("defaultServeRoot: %v", err)
	}
	want := filepath.Join(stateHome, "relevo", "serve")
	if got != want {
		t.Errorf("defaultServeRoot() = %q, want %q", got, want)
	}
}

func TestResolveHooksConfig(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))

	d, err := db.Open(filepath.Join(tempHome, "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	log := hooks.NewKVLog(db.TxKV{DB: d})

	hooksMap := map[string][][]string{"state_changed": {{"/bin/true"}}}
	cfg, err := resolveHooksConfig(hooksMap, log)
	if err != nil {
		t.Fatalf("resolveHooksConfig: %v", err)
	}
	if cfg.Log != hooks.RunLog(log) {
		t.Fatalf("got Log %v, want the run log passed in", cfg.Log)
	}
	if !reflect.DeepEqual(cfg.Hooks, hooksMap) {
		t.Fatalf("got Hooks %#v, want %#v", cfg.Hooks, hooksMap)
	}
}

// --assume-dead must be defined on the bind flag set. If it were not, parsing
// would fail with "flag provided but not defined" and never reach the --name
// check -- which runs before any runtime is built, so this test touches
// neither the state directory nor a harness.
func TestAddHelp(t *testing.T) {
	err := run([]string{"bind", "--worktree", "-h"})
	if !errors.Is(err, errHelpShown) {
		t.Fatalf("got %v, want errHelpShown", err)
	}
}

func TestAddValidation(t *testing.T) {
	// Missing --name
	err := run([]string{"bind", "--worktree", "--no-feature", "--candidate", "claude/test/m"})
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("expected an error about --name, got %v", err)
	}
}

// TestAddBranchWithCwdIsRefusedBeforeRuntime pins the flag-pair refusal: it
// happens in validation, before newRuntime, so it reaches no harness.
func TestAddBranchWithCwdIsRefusedBeforeRuntime(t *testing.T) {
	err := run([]string{"bind", "--branch", "x", "--cwd", "/tmp", "--no-feature"})
	if err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("expected an 'exclusive' refusal, got %v", err)
	}
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("expected exit code 2, got %v", err)
	}
}

// TestAddBranchDerivesName pins that a branch alone is enough for the name to
// be derived: with no relevo mastermind for this session the run stops on the
// no-mastermind error, before any harness call, so the derived name is never
// printed and no builder is reached.
func TestAddBranchDerivesName(t *testing.T) {
	t.Setenv("RELEVO_MASTERMIND", "")
	t.Setenv("CLAUDECODE", "")

	err := run([]string{"bind", "--branch", "feature/api-auth", "--no-feature"})
	if err == nil {
		t.Fatal("add without a relevo mastermind must refuse")
	}
	if !strings.Contains(err.Error(), "no relevo MasterMind for this session") {
		t.Fatalf("expected the no-mastermind error, got %v", err)
	}
	if strings.Contains(err.Error(), "api-auth") {
		t.Errorf("the derived name must not appear in the refusal: %v", err)
	}
}

func TestParseFlagsAcceptsFlagsAfterPositionals(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	round := fs.Int("round", 0, "")
	dry := fs.Bool("dry", false, "")

	// This is the README's documented shape: the binding name first, its flags
	// after. Go's flag package stops at the first bare word, so before #48 both
	// flags below were silently dropped.
	if err := parseFlags(fs, []string{"webshop", "--round", "2", "--dry"}); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if *round != 2 {
		t.Errorf("--round after a positional must still parse, got %d", *round)
	}
	if !*dry {
		t.Error("--dry after a positional must still parse")
	}
	if got := fs.Args(); len(got) != 1 || got[0] != "webshop" {
		t.Errorf("fs.Args() = %v, want [webshop]", got)
	}
}

func TestParseFlagsKeepsEveryPositionalInOrder(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "")

	if err := parseFlags(fs, []string{"alpha", "--name", "n", "beta"}); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if *name != "n" {
		t.Errorf("--name = %q, want n", *name)
	}
	// explicitBinding refuses two positionals, so collapsing or reordering them
	// would quietly turn a refusal into a wrong guess.
	got := fs.Args()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("fs.Args() = %v, want [alpha beta]", got)
	}
}

func TestParseFlagsStillRejectsUnknownFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	// Before #48 this was swallowed with the rest of the tail.
	if err := parseFlags(fs, []string{"webshop", "--bogus"}); err == nil {
		t.Fatal("an unknown flag after a positional must still be rejected")
	}
}

func TestParseFlagsStillHandlesHelp(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if err := parseFlags(fs, []string{"-h"}); !errors.Is(err, errHelpShown) {
		t.Fatalf("got %v, want errHelpShown", err)
	}
}

func TestParseFlagsHandlesNoArguments(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if err := parseFlags(fs, nil); err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got := fs.Args(); len(got) != 0 {
		t.Errorf("fs.Args() = %v, want empty", got)
	}
}

func TestBindingArgTakesEitherForm(t *testing.T) {
	cases := []struct {
		label      string
		flag       string
		positional []string
		want       string
		wantErr    bool
	}{
		{"flag only", "webshop", nil, "webshop", false},
		{"positional only", "", []string{"webshop"}, "webshop", false},
		{"neither is not an error", "", nil, "", false},
		{"both at once is refused", "webshop", []string{"other"}, "", true},
		{"same name twice is still refused", "webshop", []string{"webshop"}, "", true},
		{"two positionals refused", "", []string{"a", "b"}, "", true},
	}

	for _, c := range cases {
		got, err := bindingArg(c.flag, c.positional)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected a refusal, got %q", c.label, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.label, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.label, got, c.want)
		}
	}
}

func TestResolveBindingAcceptsAPositionalName(t *testing.T) {
	rt := relevo.Runtime{Store: store.New(t.TempDir())}

	// The whole point of #50: a named binding must be used, not discarded in
	// favour of whatever owns the cwd.
	got, err := resolveBinding(rt, "", []string{"webshop"})
	if err != nil {
		t.Fatalf("resolveBinding: %v", err)
	}
	if got != "webshop" {
		t.Errorf("got %q, want webshop", got)
	}
}

func TestResolveBindingRefusesTwoNames(t *testing.T) {
	rt := relevo.Runtime{Store: store.New(t.TempDir())}

	if _, err := resolveBinding(rt, "webshop", []string{"frontend"}); err == nil {
		t.Fatal("naming the binding twice must be refused rather than one silently winning")
	}
}

func TestResolveBindingStillFallsBackToCWD(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))

	s := store.New(filepath.Join(tempHome, ".local", "state", "relevo"))
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Seed a binding that owns the test's own working directory, so the
	// fallback has something to find without any chdir.
	if err := s.Save(store.Binding{
		Name: "here", CWD: cwd, Round: 1, State: store.StateActive,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := resolveBinding(relevo.Runtime{Store: s}, "", nil)
	if err != nil {
		t.Fatalf("resolveBinding: %v", err)
	}
	if got != "here" {
		t.Errorf("a bare invocation must still fall back to the cwd binding, got %q", got)
	}
}

func TestFilterReportNarrowsToOneBinding(t *testing.T) {
	rep := view.Report{Bindings: []view.BindingStatus{
		{Name: "api"}, {Name: "frontend"}, {Name: "backend"},
	}}

	got, err := filterReport(rep, "frontend")
	if err != nil {
		t.Fatalf("filterReport: %v", err)
	}
	if len(got.Bindings) != 1 || got.Bindings[0].Name != "frontend" {
		t.Fatalf("got %+v, want just frontend", got.Bindings)
	}
}

func TestFilterReportKeepsEverythingWhenUnnamed(t *testing.T) {
	rep := view.Report{Bindings: []view.BindingStatus{
		{Name: "api"}, {Name: "frontend"},
	}}

	// A bare `relevo status` lists every binding; that is its whole job.
	got, err := filterReport(rep, "")
	if err != nil {
		t.Fatalf("filterReport: %v", err)
	}
	if len(got.Bindings) != 2 {
		t.Fatalf("got %+v, want both bindings", got.Bindings)
	}
}

func TestFilterReportRejectsAnUnknownName(t *testing.T) {
	rep := view.Report{Bindings: []view.BindingStatus{{Name: "api"}}}

	// Silence here would look identical to "that binding is fine".
	if _, err := filterReport(rep, "nosuch"); err == nil {
		t.Fatal("an unknown binding name must be an error, not an empty report")
	}
}

// scopeReport is the whole of the status/watch DONE rule, tested as a pure
// function: a test that ran cmdStatus would need a real harness on PATH, which
// CI does not have and which made the first version of this test pass only on
// the dev machine.
func TestScopeReportHidesDoneUnlessAllOrNamed(t *testing.T) {
	rep := view.Report{Bindings: []view.BindingStatus{
		{Name: "live", State: string(store.StateActive)},
		{Name: "finished", State: string(store.StateDone)},
	}}

	plain := scopeReport(rep, "", false)
	if len(plain.Bindings) != 1 || plain.Bindings[0].Name != "live" || plain.DoneHidden != 1 {
		t.Errorf("plain: got %+v, want only live with DoneHidden 1", plain)
	}

	all := scopeReport(rep, "", true)
	if len(all.Bindings) != 2 || all.DoneHidden != 0 {
		t.Errorf("--all: got %+v, want both rows and DoneHidden 0", all)
	}

	// filterReport has already narrowed to the named binding by the time
	// scopeReport runs; what matters is that the name switches the filter off.
	named := scopeReport(view.Report{Bindings: rep.Bindings[1:]}, "finished", false)
	if len(named.Bindings) != 1 || named.DoneHidden != 0 {
		t.Errorf("--name: got %+v, want the DONE row with DoneHidden 0", named)
	}
}

func TestParseFor(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)

	if got, err := parseFor("", now); err != nil || !got.IsZero() {
		t.Fatalf(`parseFor("", now) = %v, %v, want zero time, nil`, got, err)
	}

	if got, err := parseFor("2h", now); err != nil || !got.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("parseFor(2h, now) = %v, %v, want %v, nil", got, err, now.Add(2*time.Hour))
	}

	if got, err := parseFor("90m", now); err != nil || !got.Equal(now.Add(90*time.Minute)) {
		t.Fatalf("parseFor(90m, now) = %v, %v, want %v, nil", got, err, now.Add(90*time.Minute))
	}

	if _, err := parseFor("0", now); err == nil {
		t.Error(`parseFor("0", now) must be an error: a zero duration is not a gate`)
	}
	if _, err := parseFor("-5m", now); err == nil {
		t.Error(`parseFor("-5m", now) must be an error: a negative duration is not a gate`)
	}
	if _, err := parseFor("soon", now); err == nil {
		t.Error(`parseFor("soon", now) must be an error: not a Go duration`)
	}
}

// TestBindRejectsTabFlag pins #79: placement is not a per-bind decision any
// more, so the old --tab spelling must be an unknown flag, not a silent no-op.
// It fails in parseFlags, before newRuntime, so it never reaches a harness.
func TestBindRejectsTabFlag(t *testing.T) {
	for _, args := range [][]string{
		{"bind", "--tab"},
		{"bind", "--worktree", "--name", "x", "--tab"},
		{"bind", "--branch", "b", "--name", "y", "--tab"},
	} {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Errorf("%v: got %v, want an unknown-flag error", args, err)
		}
	}
}

// TestBindRebindNeedsResume pins #92: --rebind only means something on a
// resume. It is refused before newRuntime, so no harness is reached.
func TestBindRebindNeedsResume(t *testing.T) {
	err := run([]string{"bind", "--rebind", "--no-feature", "--name", "x"})
	if err == nil || !strings.Contains(err.Error(), "--rebind") || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("got %v, want an error naming --rebind and --resume", err)
	}
}

// TestBindActorWithResumeRefused pins #382 §4: a resume keeps the binding's
// stored actor, so --actor is refused. The check runs before newRuntime, so no
// harness is reached and nothing is spawned.
func TestBindActorWithResumeRefused(t *testing.T) {
	err := run([]string{"bind", "--resume", "--actor", "x", "--name", "n"})
	if err == nil {
		t.Fatal("bind --resume --actor = nil, want the drop --actor refusal")
	}
	if !strings.Contains(err.Error(), "drop --actor") {
		t.Fatalf("got %v, want an error naming --actor and the resume's stored actor", err)
	}
}

// TestBindFlagsHaveActorNotRole pins A2 round 3 S1: bind's writer flag is
// --actor, and the old --role is removed rather than aliased.
func TestBindFlagsHaveActorNotRole(t *testing.T) {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	bindFlagSet(fs)
	if fs.Lookup("actor") == nil {
		t.Error("bind does not define --actor")
	}
	if fs.Lookup("role") != nil {
		t.Error("bind still defines --role; it must be removed, not aliased")
	}
}

// TestBindFlagSetDefinesLabels pins #637: a fresh bind needs --no-feature as
// the alternative to --feature, and --ticket is the new optional issue flag.
func TestBindFlagSetDefinesLabels(t *testing.T) {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	bindFlagSet(fs)
	for _, name := range []string{"feature", "no-feature", "ticket"} {
		if fs.Lookup(name) == nil {
			t.Errorf("bind does not define --%s", name)
		}
	}
}

// TestBindFlagSetDefinesLocal pins the placement flag: --local runs the
// builder here, whatever the actor's placement says.
func TestBindFlagSetDefinesLocal(t *testing.T) {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	bindFlagSet(fs)
	if fs.Lookup("local") == nil {
		t.Error("bind does not define --local")
	}
}

// TestBindRejectsServerWithLocal pins the placement conflict: --server and
// --local name two different machines, so the pair exits 2 on one line before
// any runtime is built, like the other route refusals.
func TestBindRejectsServerWithLocal(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"bind", "--no-feature", "--name", "x", "--server", "zen", "--local"})
	})
	ce := requireCLIError(t, err, codeRefused, "")
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("run = %v, want exit code 2", err)
	}
	for _, want := range []string{"--server", "--local"} {
		if !strings.Contains(ce.message, want) {
			t.Errorf("message = %q, want it to name %s", ce.message, want)
		}
	}
}

// TestBindRequiresAFeatureChoice pins #637's exactly-one rule on the CLI: a
// fresh bind with neither flag, on either route, exits 2 with one stderr line
// naming both flags, before any runtime is built.
func TestBindRequiresAFeatureChoice(t *testing.T) {
	for _, args := range [][]string{
		{"bind", "--name", "x"},
		{"bind", "--worktree", "--name", "x"},
	} {
		_, _, err := captureOutput(t, func() error { return run(args) })
		ce := requireCLIError(t, err, codeRefused, "")
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("%v: run = %v, want exit code 2", args, err)
			continue
		}
		for _, flag := range []string{"--feature", "--no-feature"} {
			if !strings.Contains(ce.message, flag) {
				t.Errorf("%v: message = %q, want it to name %s", args, ce.message, flag)
			}
		}
	}
}

// TestBindRejectsBothFeatureFlags pins the other half: both flags together is
// refused with the same one-line exit 2.
func TestBindRejectsBothFeatureFlags(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"bind", "--feature", "auth", "--no-feature", "--name", "x"})
	})
	ce := requireCLIError(t, err, codeRefused, "")
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("run = %v, want exit code 2", err)
	}
	for _, flag := range []string{"--feature", "--no-feature"} {
		if !strings.Contains(ce.message, flag) {
			t.Errorf("message = %q, want it to name %s", ce.message, flag)
		}
	}
}

// TestBindRejectsABadFeatureStillExits2 pins the shared pre-route check the
// two route-local store.ValidFeature blocks became: a malformed --feature is
// still one line and exit 2.
func TestBindRejectsABadFeatureStillExits2(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"bind", "--feature", "a/b", "--name", "x"})
	})
	ce := requireCLIError(t, err, codeRefused, "")
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("run = %v, want exit code 2", err)
	}
	if !strings.Contains(ce.message, "feature:") {
		t.Errorf("message = %q, want the feature rule's own text", ce.message)
	}
}

// TestBindRejectsABadTicketExits2 pins #637: a malformed --ticket is refused
// on the same one-line exit 2, before any runtime.
func TestBindRejectsABadTicketExits2(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"bind", "--no-feature", "--ticket", "not a ticket", "--name", "x"})
	})
	ce := requireCLIError(t, err, codeRefused, "")
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("run = %v, want exit code 2", err)
	}
	if !strings.Contains(ce.message, "ticket:") {
		t.Errorf("message = %q, want the ticket rule's own text", ce.message)
	}
}

// TestRemovedFlagsAreUnknown pins D1's clean break: every flag A4 renamed is
// removed, not aliased, so parsing it fails with the flag package's own
// "flag provided but not defined" error. It parses through the verbs' flag
// sets only -- no verb runs, so nothing touches the state directory and no
// harness is spawned.
func TestRemovedFlagsAreUnknown(t *testing.T) {
	cases := []struct {
		name  string
		flags func(*flag.FlagSet)
		args  []string
	}{
		{"bind", func(fs *flag.FlagSet) { bindFlagSet(fs) }, []string{"--builder", "x"}},
		{"send", func(fs *flag.FlagSet) { sendFlagSet(fs) }, []string{"--builder", "x"}},
		{"config agents", func(fs *flag.FlagSet) { agentFlagSet(fs) }, []string{"--role", "x"}},
		{"config init", func(fs *flag.FlagSet) { initFlagSet(fs) }, []string{"--no-roles"}},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		c.flags(fs)
		if err := parseFlags(fs, c.args); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Errorf("%s %v: parse error = %v, want \"flag provided but not defined\"", c.name, c.args, err)
		}
	}
}

// TestPickRejectsAName pins spec §3: --pick chooses the binding, so naming
// one as well is a usage error. Each case fails before newRuntime, so no
// a harness is reached.
func TestPickRejectsAName(t *testing.T) {
	for _, args := range [][]string{
		{"done", "--pick", "x"},
		{"done", "--pick", "--name", "x"},
		{"unbind", "--pick", "x"},
		{"unbind", "--pick", "--name", "x", "--archive"},
	} {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "--pick chooses the binding") {
			t.Errorf("%v: got %v, want the --pick usage error", args, err)
		}
	}
}

// TestPickRejectsAnswerFlags: the answer comes from the screen, so --keys,
// --choice and --text have nothing to apply to. Fails before newRuntime.
// TestBindHeadlessConflictsFailBeforeNewRuntime pins headless spec §6: the
// two flag conflicts are usage errors, refused before relevo talks to a harness.
// CI runners have no harness binary, so reaching newRuntime would be a
// different failure with a different message.
func TestBuilderWhere(t *testing.T) {
	if got := builderWhere(store.Endpoint{PaneID: "w2:p4"}); got != "w2:p4" {
		t.Errorf("pane: %q", got)
	}
	if got := builderWhere(store.Endpoint{Mode: store.ModeHeadless, AgentName: "x-builder"}); got != "headless" {
		t.Errorf("headless: %q", got)
	}
}

// TestStatusNotice is §4.6: exactly one line when the cached check proves
// relevo is behind, and "" for every row the doctor's release table reports as
// SevOK. It is the pure function only -- no subcommand runs, because CI
// runners launch no harness.
func TestStatusNotice(t *testing.T) {
	cases := []struct {
		name    string
		running string
		latest  string
		ok      bool
		kind    release.Kind
		want    string
	}{
		{
			name:    "behind a go install",
			running: "v0.6.0",
			latest:  "v0.7.0",
			ok:      true,
			kind:    release.KindGoInstall,
			want:    "relevo v0.6.0 is behind v0.7.0 -- run relevo doctor",
		},
		{
			name:    "behind a release binary",
			running: "v0.8.0",
			latest:  "v0.9.0",
			ok:      true,
			kind:    release.KindRelease,
			want:    "relevo v0.8.0 is behind v0.9.0 -- run relevo doctor",
		},
		{
			name:    "no usable cache",
			running: "v0.6.0",
			latest:  "v0.7.0",
			ok:      false,
			kind:    release.KindGoInstall,
			want:    "",
		},
		{
			name:    "unknown install kind",
			running: "v0.6.0",
			latest:  "v0.7.0",
			ok:      true,
			kind:    release.KindUnknown,
			want:    "",
		},
		{
			name:    "local build has nothing to update to",
			running: "v0.7.0-8-gbd8aed0",
			latest:  "v0.8.0",
			ok:      true,
			kind:    release.KindLocalBuild,
			want:    "",
		},
		{
			name:    "(devel) claims nothing",
			running: "(devel)",
			latest:  "v0.8.0",
			ok:      true,
			kind:    release.KindLocalBuild,
			want:    "",
		},
		{
			name:    "unparseable latest",
			running: "v0.6.0",
			latest:  "nightly",
			ok:      true,
			kind:    release.KindGoInstall,
			want:    "",
		},
		{
			name:    "already current",
			running: "v0.7.0",
			latest:  "v0.7.0",
			ok:      true,
			kind:    release.KindGoInstall,
			want:    "",
		},
		{
			// A describe of the very tag the cache names is not "behind" it.
			name:    "describe is not behind its own tag",
			running: "v0.7.0-8-gbd8aed0",
			latest:  "v0.7.0",
			ok:      true,
			kind:    release.KindGoInstall,
			want:    "",
		},
		{
			name:    "cached latest is older",
			running: "v0.7.0",
			latest:  "v0.6.0",
			ok:      true,
			kind:    release.KindGoInstall,
			want:    "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := statusNotice(c.running, c.latest, c.ok, c.kind); got != c.want {
				t.Errorf("statusNotice(%q, %q, %v, %q) = %q, want %q",
					c.running, c.latest, c.ok, c.kind, got, c.want)
			}
		})
	}
}

func TestWithEnv(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		key  string
		val  string
		want []string
	}{
		{
			name: "appends when absent",
			in:   []string{"A=1"},
			key:  "K", val: "v",
			want: []string{"A=1", "K=v"},
		},
		{
			name: "replaces an existing key in place",
			in:   []string{"A=1", "K=old", "B=2"},
			key:  "K", val: "v",
			want: []string{"A=1", "K=v", "B=2"},
		},
		{
			name: "collapses duplicates",
			in:   []string{"K=one", "A=1", "K=two"},
			key:  "K", val: "v",
			want: []string{"K=v", "A=1"},
		},
		{
			name: "a value that merely starts with the key is left alone",
			in:   []string{"KEEP=1"},
			key:  "K", val: "v",
			want: []string{"KEEP=1", "K=v"},
		},
		{
			name: "nil env appends",
			in:   nil,
			key:  "K", val: "v",
			want: []string{"K=v"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := withEnv(tc.in, tc.key, tc.val)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("withEnv(%v, %q, %q) = %v, want %v", tc.in, tc.key, tc.val, got, tc.want)
			}
		})
	}
}

// TestDaemonPreflightOpensNothing covers §4.4: --preflight validates config and
// returns before the lock, the DB (which would migrate) or any process. The
// state root must be left with neither relevo.db nor daemon.lock.
func TestDaemonPreflightOpensNothing(t *testing.T) {
	configHome := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	stdout, _, runErr := captureOutput(t, func() error {
		return cmdDaemon([]string{"--preflight"})
	})
	if runErr != nil {
		t.Fatalf("cmdDaemon --preflight: %v", runErr)
	}
	if !strings.HasPrefix(string(stdout), "ok ") {
		t.Errorf("--preflight stdout = %q, want it to start with %q", stdout, "ok ")
	}

	root := filepath.Join(stateHome, "relevo")
	for _, name := range []string{"relevo.db", ".daemon.lock"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("%s present after --preflight (stat err %v); preflight must open nothing", name, err)
		}
	}
}

// TestDaemonPreflightFailsOnBadConfig covers §4.4's failure half: a config
// error goes to stderr and comes back as a non-nil error (exit 1 at the CLI),
// and still opens nothing.
func TestDaemonPreflightFailsOnBadConfig(t *testing.T) {
	configHome := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	relevoDir := filepath.Join(configHome, "relevo")
	if err := os.MkdirAll(relevoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(relevoDir, "policy.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write policy.json: %v", err)
	}

	if err := cmdDaemon([]string{"--preflight"}); err == nil {
		t.Fatal("cmdDaemon --preflight with a malformed policy.json: err = nil, want an error")
	}
}

// TestDaemonPreflightAcceptsAStoredLegacyClientKey covers the upgrade this
// round repairs: a database whose client.key still carries the pre-rename PEM
// label must pass --preflight. The read-only peek parses the stored key, so it
// needs no migration; and because preflight cannot write, the stored bytes must
// come back exactly as they were.
func TestDaemonPreflightAcceptsAStoredLegacyClientKey(t *testing.T) {
	configHome := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	d, err := store.New(root).DB()
	if err != nil {
		t.Fatalf("Store.DB: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	legacy := pem.EncodeToMemory(&pem.Block{
		Type:  "RELAY ED25519 PRIVATE KEY", // name-guard: legacy
		Bytes: []byte(kp.Private),
	})

	// A non-empty servers section is what makes the read parse the key at all.
	if _, err := config.Open(d).As("test", "seed servers").Put(config.Servers,
		[]byte(`{"zen":{"url":"https://zen:7777","fingerprint":"sha256:abcd"}}`)); err != nil {
		t.Fatalf("Put(servers): %v", err)
	}
	if err := d.Tx(func(tx *db.Tx) error {
		return tx.SecretPut(config.SecretClientKey, legacy, time.Now().UTC())
	}); err != nil {
		t.Fatalf("SecretPut: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stdout, stderr, runErr := captureOutput(t, func() error {
		return cmdDaemon([]string{"--preflight"})
	})
	if runErr != nil {
		t.Fatalf("cmdDaemon --preflight with a legacy client key: %v (stderr %q)", runErr, stderr)
	}
	if !strings.HasPrefix(string(stdout), "ok ") {
		t.Errorf("--preflight stdout = %q, want it to start with %q", stdout, "ok ")
	}

	// Preflight writes nothing: the stored row is still legacy.
	d2, err := db.Open(filepath.Join(root, "relevo.db"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = d2.Close() }()
	got, ok, err := d2.SecretGet(config.SecretClientKey)
	if err != nil {
		t.Fatalf("SecretGet: %v", err)
	}
	if !ok {
		t.Fatal("client.key is missing after --preflight")
	}
	if !bytes.Equal(got, legacy) {
		t.Fatalf("stored key = %q, want it unchanged (legacy) after a read-only preflight", got)
	}
}

// TestBindRoutesAndRefusals pins §4.1: the flag combinations choose the path,
// and the forbidden pairs exit 2 before any runtime is built. bindRouteFor is
// a pure function, so the valid half needs no state; the invalid half goes
// through run so the exit code and the one-line refusal are pinned too.
func TestBindRoutesAndRefusals(t *testing.T) {
	valid := []struct {
		name string
		f    bindFlags
		want bindRoute
	}{
		{"no placement is bind", bindFlags{}, routeBind},
		{"resume is bind", bindFlags{name: "x", resume: true}, routeBind},
		{"rebind is bind", bindFlags{name: "x", resume: true, rebind: true}, routeBind},
		{"worktree is add", bindFlags{worktree: true}, routeAdd},
		{"cwd is add", bindFlags{cwd: "/tmp"}, routeAdd},
		{"branch is add", bindFlags{branch: "b"}, routeAdd},
		{"server is add", bindFlags{server: "s"}, routeAdd},
		{"base is add", bindFlags{base: "main"}, routeAdd},
		{"local is not a route flag", bindFlags{local: true}, routeBind},
	}
	for _, c := range valid {
		got, err := bindRouteFor(c.f)
		if err != nil {
			t.Errorf("%s: bindRouteFor = %v, want nil", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: route = %v, want %v", c.name, got, c.want)
		}
	}

	invalid := []struct {
		name string
		args []string
		want string
	}{
		{"resume+worktree", []string{"bind", "--resume", "--name", "x", "--worktree"}, "--resume/--rebind cannot be combined with"},
		{"resume+cwd", []string{"bind", "--resume", "--name", "x", "--cwd", "/tmp"}, "--resume/--rebind cannot be combined with"},
		{"rebind+server", []string{"bind", "--resume", "--rebind", "--name", "x", "--server", "s"}, "--resume/--rebind cannot be combined with"},
	}
	for _, c := range invalid {
		_, _, err := captureOutput(t, func() error { return run(c.args) })
		ce := requireCLIError(t, err, codeRefused, "")
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("%s: run = %v, want exit code 2", c.name, err)
			continue
		}
		if !strings.Contains(ce.message, c.want) {
			t.Errorf("%s: message = %q, want it to contain %q", c.name, ce.message, c.want)
		}
	}
}

// TestUnbindDoneTakesNoBinding pins §4.3: --done clears every DONE binding, so
// naming one or asking to pick one is refused with exit 2 before a runtime is
// built.
func TestUnbindDoneTakesNoBinding(t *testing.T) {
	for _, args := range [][]string{
		{"unbind", "--done", "webshop"},
		{"unbind", "--done", "--name", "webshop"},
		{"unbind", "--done", "--pick"},
	} {
		_, _, err := captureOutput(t, func() error { return run(args) })
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("%v: run = %v, want exit code 2", args, err)
		}
	}
}

// TestUnbindMasterMindFlags pins #482: --mastermind and --all-masterminds only make
// sense with --done, and combining them exits 2 before a runtime is built
// (except the --mastermind+--all-masterminds combo, which gcScope checks after
// newRuntime and is pinned by TestGCScope instead).
func TestUnbindMasterMindFlags(t *testing.T) {
	for _, args := range [][]string{
		{"unbind", "--mastermind", "x"},
		{"unbind", "--all-masterminds"},
		{"unbind", "--sweep", "--mastermind", "x"},
		{"unbind", "--sweep", "--all-masterminds"},
	} {
		_, _, err := captureOutput(t, func() error { return run(args) })
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("%v: run = %v, want exit code 2", args, err)
		}
	}
}

// TestGCScope pins #482: gcScope turns --mastermind/--all-masterminds into a GC
// scope with no fallback to "everything" when the mastermind fails to resolve.
func TestGCScope(t *testing.T) {
	t.Run("empty flag resolves via resolve", func(t *testing.T) {
		resolve := func(ref string) (mastermind.Record, error) {
			if ref != "" {
				t.Errorf("resolve called with %q, want \"\"", ref)
			}
			return mastermind.Record{ID: "pl_aaa"}, nil
		}
		got, err := gcScope("", false, resolve)
		if err != nil {
			t.Fatalf("gcScope: %v", err)
		}
		if got != (relevo.GCOptions{MasterMindID: "pl_aaa"}) {
			t.Fatalf("gcScope = %+v, want {MasterMindID: pl_aaa}", got)
		}
	})

	t.Run("mastermind flag is passed to resolve", func(t *testing.T) {
		resolve := func(ref string) (mastermind.Record, error) {
			if ref != "architect-2" {
				t.Errorf("resolve called with %q, want architect-2", ref)
			}
			return mastermind.Record{ID: "pl_bbb"}, nil
		}
		got, err := gcScope("architect-2", false, resolve)
		if err != nil {
			t.Fatalf("gcScope: %v", err)
		}
		if got != (relevo.GCOptions{MasterMindID: "pl_bbb"}) {
			t.Fatalf("gcScope = %+v, want {MasterMindID: pl_bbb}", got)
		}
	})

	t.Run("all-masterminds never calls resolve", func(t *testing.T) {
		resolve := func(ref string) (mastermind.Record, error) {
			t.Fatal("resolve must not be called when --all-masterminds is set")
			return mastermind.Record{}, nil
		}
		got, err := gcScope("", true, resolve)
		if err != nil {
			t.Fatalf("gcScope: %v", err)
		}
		if got != (relevo.GCOptions{AllMasterMinds: true}) {
			t.Fatalf("gcScope = %+v, want {AllMasterMinds: true}", got)
		}
	})

	t.Run("mastermind and all-masterminds are exclusive", func(t *testing.T) {
		resolve := func(ref string) (mastermind.Record, error) {
			t.Fatal("resolve must not be called when both flags are set")
			return mastermind.Record{}, nil
		}
		if _, err := gcScope("x", true, resolve); err == nil {
			t.Fatal("gcScope with both flags: want a usage error, got nil")
		}
	})

	t.Run("no fallback when the mastermind does not resolve", func(t *testing.T) {
		resolve := func(ref string) (mastermind.Record, error) {
			return mastermind.Record{}, errors.New("boom")
		}
		got, err := gcScope("", false, resolve)
		if err == nil {
			t.Fatal("gcScope with a resolve error: want a usage error, got nil")
		}
		if !strings.Contains(err.Error(), "--all-masterminds") {
			t.Errorf("gcScope error = %q, want it to mention --all-masterminds", err.Error())
		}
		if got.MasterMindID != "" || got.AllMasterMinds {
			t.Errorf("gcScope result = %+v, want the zero value on error (no fallback)", got)
		}
	})
}

// TestUnbindSweepTakesNoBinding pins §4.5: --sweep takes no binding and no other
// flag except --dry-run, so invalid combinations exit 2 before a runtime is built.
func TestUnbindSweepTakesNoBinding(t *testing.T) {
	for _, args := range [][]string{
		{"unbind", "--sweep", "--done"},
		{"unbind", "--sweep", "--delete"},
		{"unbind", "--sweep", "--archive"},
		{"unbind", "--sweep", "--pick"},
		{"unbind", "--sweep", "webshop"},
		{"unbind", "--sweep", "--name", "webshop"},
	} {
		_, _, err := captureOutput(t, func() error { return run(args) })
		ce := requireCLIError(t, err, codeRefused, "")
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("%v: run = %v, want exit code 2", args, err)
		}
		wantMsg := "--sweep takes no binding and no other flag except --dry-run"
		if !strings.Contains(ce.message, wantMsg) {
			t.Errorf("%v: message = %q, want to contain %q", args, ce.message, wantMsg)
		}
	}
}

// TestRemovedVerbsNameTheirReplacement pins §4.6 and §4.1/§4.3: each removed
// name returns the usage-coded refusal whose next command is the form that
// replaces it, and exits 2 through the code bridge.
func TestRemovedVerbsNameTheirReplacement(t *testing.T) {
	cases := []struct{ verb, replacement string }{
		{"add", "relevo bind --worktree"},
		{"diff", "relevo show --diff"},
		{"log", "relevo show --log"},
		{"gc", "relevo unbind --done"},
		{"pause", "relevo done, then relevo bind --resume"},
		{"statusline", "relevo status --line"},
		{"pull", "relevo wait (it prints the report)"},
		{"unavailable", "relevo gate <token>"},
		{"available", "relevo gate --clear <provider>"},
	}
	for _, c := range cases {
		err := run([]string{c.verb})
		ce := requireCLIError(t, err, codeUsage, c.replacement)
		if !strings.Contains(ce.message, "was removed") {
			t.Errorf("%s: message = %q, want it to say it was removed", c.verb, ce.message)
		}
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("%s: run = %v, want exit code 2", c.verb, err)
		}
	}
}

func TestStatusLineFlags(t *testing.T) {
	t.Run("status --line --name x exits 2", func(t *testing.T) {
		_, _, err := captureOutput(t, func() error {
			return run([]string{"status", "--line", "--name", "x"})
		})
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("status --line --name x: err = %v, want exit code 2", err)
		}
	})

	t.Run("status --line --all exits 2", func(t *testing.T) {
		_, _, err := captureOutput(t, func() error {
			return run([]string{"status", "--line", "--all"})
		})
		var ec exitCodeErr
		if !errors.As(err, &ec) || ec.code != 2 {
			t.Errorf("status --line --all: err = %v, want exit code 2", err)
		}
	})

	t.Run("status --line --json prints StatusLineDoc with null mastermind and empty rows", func(t *testing.T) {
		t.Setenv("RELEVO_MASTERMIND", "")
		t.Setenv("CLAUDECODE", "")
		t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "")
		t.Setenv("RELEVO_HARNESS", "")

		stdout, stderr, err := captureOutput(t, func() error {
			return run([]string{"status", "--line", "--json"})
		})
		if err != nil {
			t.Fatalf("run status --line --json failed: %v (stderr: %s)", err, stderr)
		}

		var doc view.StatusLineDoc
		if err := json.Unmarshal(stdout, &doc); err != nil {
			t.Fatalf("unmarshal json %q: %v", stdout, err)
		}
		if doc.MasterMind != nil {
			t.Errorf("doc.MasterMind = %+v, want nil", doc.MasterMind)
		}
		if doc.Rows == nil || len(doc.Rows) != 0 {
			t.Errorf("doc.Rows = %+v, want empty []", doc.Rows)
		}
		s := string(stdout)
		if !strings.Contains(s, `"mastermind":null`) {
			t.Errorf("output %q does not contain '\"mastermind\":null'", s)
		}
		if !strings.Contains(s, `"rows":[]`) {
			t.Errorf("output %q does not contain '\"rows\":[]'", s)
		}
	})
}
