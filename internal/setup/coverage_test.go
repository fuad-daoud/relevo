package setup

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestBuildNothingLoggedInIsNotAvailable pins the not-logged-in refusal:
// nothing is written, and the message names every harness's login command.
func TestBuildNothingLoggedInIsNotAvailable(t *testing.T) {
	inv := Inventory{Harnesses: []HarnessStatus{
		status("claude", false),
		status("codex", false),
	}}
	_, err := Build(inv, RecommendedAnswers(inv, nil))
	var na *NotAvailableError
	if !errors.As(err, &na) {
		t.Fatalf("Build error = %v, want NotAvailableError", err)
	}
	if na.Error() == "" || !strings.Contains(na.Error(), "no harness is logged in") {
		t.Fatalf("message %q does not say what is missing", na.Message)
	}
	for _, login := range []string{"claude auth login", "codex login"} {
		if !strings.Contains(na.Error(), login) {
			t.Errorf("message %q does not name %q", na.Message, login)
		}
	}
}

// TestBuildRefusesMissingAnswers pins the answers Build cannot do without:
// a token out of the question's choices, and an answer missing at all, each
// named plainly.
func TestBuildRefusesMissingAnswers(t *testing.T) {
	_, err := Build(codexInventory(), Answers{})
	if err == nil || !strings.Contains(err.Error(), "missing builder answer") {
		t.Fatalf("Build with no answers: %v, want a missing-builder refusal", err)
	}

	_, err = Build(codexInventory(), Answers{"builder": {"codex/openai/gpt-6-luna:high"}, "planner": {"codex/openai/gpt-6-luna:high"}})
	if err == nil || !strings.Contains(err.Error(), "missing tier answer") {
		t.Fatalf("Build without a tier: %v, want a missing-tier refusal", err)
	}
}

// TestRunnerRunsAndReportsExitCode pins the OS runner: real stdout through,
// and a non-zero exit reports with its code and no error.
func TestRunnerRunsAndReportsExitCode(t *testing.T) {
	out, exit, err := runnerEnv{}.Run("sh", "-c", "printf runner")
	if err != nil || exit != 0 || string(out) != "runner" {
		t.Errorf("Run = %q/%d/%v, want runner/0/nil", out, exit, err)
	}
	_, exit, err = runnerEnv{}.Run("sh", "-c", "exit 3")
	if err != nil || exit != 3 {
		t.Errorf("Run = %d/%v, want exit 3 and nil", exit, err)
	}
}

// TestOSEnvIsTheRunnerEnv pins the production constructor: the env it builds
// carries the runner too, so Probe could ask real CLIs with one SetupEnv.
func TestOSEnvIsTheRunnerEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	env, err := OSEnv()
	if err != nil {
		t.Fatalf("OSEnv: %v", err)
	}
	if _, err := env.LookPath("agy-not-on-path"); err == nil {
		t.Error("LookPath found a binary that is not on PATH")
	}
	out, _, err := env.Run("sh", "-c", "printf probe")
	if err != nil || string(out) != "probe" {
		t.Errorf("Run through OSEnv = %q/%v, want probe/nil", out, err)
	}
}

// TestProbeTimeoutForDefaultsToTheDeadline pins the fallback: an env without
// its own probe bound gets the shared deadline.
func TestProbeTimeoutForDefaultsToTheDeadline(t *testing.T) {
	env := runnerEnv{probeEnv{}.pathEnv}
	if got := probeTimeoutFor(env); got != probeDeadline {
		t.Errorf("probeTimeoutFor = %s, want %s", got, probeDeadline)
	}
}

// TestCodexModelsParsesWrappedShapes pins the two codex offer shapes: the
// bare array and one wrapped under models, and silence parsing as no offer.
func TestCodexModelsParsesWrappedShapes(t *testing.T) {
	if got := codexModels([]byte(`{"models": [{"slug": "gpt-6-luna", "visibility": "list", "priority": 2}]}`)); !reflect.DeepEqual(got, []string{"gpt-6-luna"}) {
		t.Errorf("wrapped shapes = %v, want [gpt-6-luna]", got)
	}
	if got := codexModels([]byte("not json at all")); got != nil {
		t.Errorf("malformed stdout = %v, want no offer", got)
	}
}

// TestMatchChoiceRefusesAnEmptyEffort pins the suffix rule's edge: a lone
// colon names no choice.
func TestMatchChoiceRefusesAnEmptyEffort(t *testing.T) {
	q := tierQuestion()
	if _, ok := matchChoice(q, "yolo:"); ok {
		t.Error("empty effort suffix matched a choice, want no match")
	}
	if _, ok := matchChoice(q, "yolo"); !ok {
		t.Error("a plain choice value did not match")
	}
}

// TestProviderForDefaultsToTheKind pins the fallback: a harness with no
// provider row lists its candidates under its own kind.
func TestProviderForDefaultsToTheKind(t *testing.T) {
	if got := providerFor("nonesuch"); got != "nonesuch" {
		t.Errorf("providerFor = %q, want the kind itself", got)
	}
	if got := providerFor("codex"); got != "openai" {
		t.Errorf("providerFor(codex) = %q, want openai", got)
	}
}
