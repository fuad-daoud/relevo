package main

// setup_test.go pins the setup verb's thin front end: the flag surface, the
// exit mapping, and the applied runs that reach the real machine through the
// CLI. The wizard's own logic is pinned in internal/setup.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/serviceunit"
	"github.com/fuad-daoud/relevo/internal/setup"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestSetupFlagSurface pins the flag set on fs exactly: --answer repeatable
// and --json, nothing else, so the registry row and the flag package print
// the same surface.
func TestSetupFlagSurface(t *testing.T) {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	v := setupFlagSet(fs)
	var names []string
	fs.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	if !slices.Equal(names, []string{"answer", "json"}) {
		t.Fatalf("setup declares %v, want [answer json]", names)
	}
	if v.asJSON == nil {
		t.Fatal("json value is nil")
	}
	if v.answers == nil {
		t.Fatal("answers value is nil")
	}
	if err := fs.Parse([]string{"--answer", "tier=yolo", "--answer", "plugin=yes"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := []string(*v.answers); !slices.Equal(got, []string{"tier=yolo", "plugin=yes"}) {
		t.Fatalf("--answer kept %v, want both values in order", got)
	}
	if *v.asJSON {
		t.Fatal("json parsed true without a flag")
	}
}

// TestSetupExitIsSilentAfterAFailedRow pins that a failed step is printed
// once: the apply hands main a bare exit code carrying the step's own exit,
// whether the step failed plainly or on the usage-limit failover.
func TestSetupExitIsSilentAfterAFailedRow(t *testing.T) {
	for _, err := range []error{
		&setup.StepError{Message: "limited", Next: "relevo doctor"},
		&setup.LimitError{Message: "limited", Next: "relevo setup"},
	} {
		got := setupExitOf(err)
		var ce *cliError
		if errors.As(got, &ce) {
			t.Fatalf("setupExitOf = %v, a cliError main would print a second time", got)
		}
		var ec exitCodeErr
		if !errors.As(got, &ec) || ec.code != 1 {
			t.Fatalf("setupExitOf = %v, want the silent exit 1", got)
		}
	}
	if setupExitOf(nil) != nil {
		t.Fatal("a clean apply exits with an error")
	}
}

// TestSetupExitReportsCallerFailures pins the other half of the mapping: a
// usage failure and a machine-state refusal printed nothing yet, so they
// report through the frame, and the open-questions return stays the silent
// exit 3.
func TestSetupExitReportsCallerFailures(t *testing.T) {
	ce := requireCLIError(t, setupExitOf(&setup.UsageError{Err: errors.New("bad value")}), codeUsage, "")
	if !strings.Contains(ce.message, "bad value") {
		t.Errorf("message = %q, want the argument failure", ce.message)
	}
	na := requireCLIError(t, setupExitOf(&setup.NotAvailableError{Message: "no harness is logged in"}), codeNotAvailable, "relevo setup")
	if !strings.Contains(na.message, "no harness is logged in") {
		t.Errorf("message = %q, want the refusal", na.message)
	}
	var ec exitCodeErr
	if got := setupExitOf(setup.ErrQuestionsOpen); !errors.As(got, &ec) || ec.code != setup.QuestionsCode {
		t.Fatalf("questions-open = %v, want the silent exit 3", got)
	}
}

// TestSetupQuestionsExitIsSilent pins that the questions-open exit is the
// report's exit-3 code: report prints nothing for it, the code simply exits.
func TestSetupQuestionsExitIsSilent(t *testing.T) {
	_, stderr, err := captureOutput(t, func() error {
		return setupExitOf(setup.ErrQuestionsOpen)
	})
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != setup.QuestionsCode {
		t.Fatalf("err = %v (%T), want exitCodeErr{3}", err, err)
	}
	if len(stderr) != 0 {
		t.Fatalf("exit 3 printed %q on stderr, want silence", stderr)
	}
}

// TestServiceStepInstallsTheUnit pins the service step: it writes the unit
// as `relevo service install` does (through the fake init runner), and an
// edited unit fails shaped as the step's own row.
func TestServiceStepInstallsTheUnit(t *testing.T) {
	if _, configHome := initRoot(t); configHome == "" {
		t.Fatal("initRoot gave no config root")
	}
	orig := newServiceRunner
	newServiceRunner = func() serviceunit.Runner { runner := &fakeRunner{}; return runner }
	t.Cleanup(func() { newServiceRunner = orig })

	if err := setupServiceInstall(); err != nil {
		t.Fatalf("setupServiceInstall: %v", err)
	}
	// The unit lands where this OS keeps it: systemd's user dir on Linux,
	// LaunchAgents on macOS.
	u, err := osUnitSpec()
	if err != nil {
		t.Fatalf("osUnitSpec: %v", err)
	}
	if _, statErr := os.Stat(u.path); statErr != nil {
		t.Fatalf("unit was not written: %v", statErr)
	}

	// An edited unit fails shaped as the step's row, naming the conflict.
	if err := os.WriteFile(u.path, []byte("edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var se *setup.StepError
	if serr := setupServiceInstall(); !errors.As(serr, &se) {
		t.Fatalf("edited unit = %v (%T), want a shaped step failure", serr, serr)
	} else if !strings.Contains(se.Message, "was edited") {
		t.Errorf("message = %q, want the conflict named", se.Message)
	}
}

// TestSetupAppliesAnswersEndToEnd pins an applied run in an env sandbox: a
// stubbed codex stands for the logged-in harness, the answers cover every
// question, and the run writes the configuration, the agent definitions and
// the step rows without touching a plugin, a service or a builder.
func TestSetupAppliesAnswersEndToEnd(t *testing.T) {
	bin := t.TempDir()
	body := "#!/bin/sh\ncase \"$1 $2\" in\n\"login status\") exit 0;;\n\"debug models\") printf '[{\"slug\":\"gpt-6-luna\",\"visibility\":\"list\",\"priority\":1}]\\n';;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	initRoot(t)
	t.Setenv("PATH", bin)

	stdoutBytes, stderr, runErr := captureOutput(t, func() error {
		return run([]string{
			"setup",
			"--answer", "builder=codex/openai/gpt-6-luna:high",
			"--answer", "planner=codex/openai/gpt-6-luna:high",
			"--answer", "tier=yolo",
			"--answer", "no-op=yes",
			"--answer", "plugin=no", "--answer", "service=no", "--answer", "smoke=no",
		})
	})
	stdout := string(stdoutBytes)
	if runErr != nil {
		t.Fatalf("setup run: %v\nstderr:\n%s", runErr, stderr)
	}
	wantOnStdout := []string{
		"codex: logged in",
		"config ok",
		"builder: gpt-6-luna",
		"agents ok",
		"next: relevo doctor",
		"next: open Claude Code in a repository and ask it to plan something",
	}
	for _, want := range wantOnStdout {
		if !strings.Contains(stdout, want) {
			t.Errorf("applied run lines lack %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(string(stderr), "no-op") {
		t.Errorf("the dropped no-op answer left no note:\n%s", stderr)
	}
	if strings.Contains(stdout, "plugin ok") {
		t.Errorf("plugin answered no but its step ran:\n%s", stdout)
	}

	L := storedConfig(t)
	if L.Policy.MaxTier != "yolo" {
		t.Fatalf("policy tier = %q, want the answered yolo", L.Policy.MaxTier)
	}
	for _, section := range []config.Section{config.Candidates, config.Policy, config.Actors} {
		if has, err := rtConfigHas(t, section); err != nil || !has {
			t.Errorf("section %s missing after setup: %v", section, err)
		}
	}
	home, _ := os.UserHomeDir()
	codexFiles := filepath.Join(home, ".codex", "plan-executor.config.toml")
	if _, err := os.Stat(codexFiles); err != nil {
		t.Errorf("agent definition not installed: %v", err)
	}
}

// TestSetupRefusesBeforeAnythingIsWritten pins the machine-state refusals:
// one codex on PATH that is not logged in stops the run with the
// not_available code and the login message, and nothing lands in config.
func TestSetupRefusesBeforeAnythingIsWritten(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	initRoot(t)
	t.Setenv("PATH", bin)

	_, stderr, err := captureOutput(t, func() error {
		return run([]string{"setup"})
	})
	ce := requireCLIError(t, err, codeNotAvailable, "relevo setup")
	if !strings.Contains(ce.message, "no harness is logged in") {
		t.Errorf("message = %q, want the login commands named", ce.message)
	}
	if len(stderr) != 0 {
		t.Fatalf("nothing runs after the refusal, but stderr = %q", stderr)
	}
}

func rtConfigHas(t *testing.T, section config.Section) (bool, error) {
	t.Helper()
	rt, err := newRuntime()
	if err != nil {
		return false, err
	}
	return rt.Config.Has(section)
}

// TestSetupInSandboxNeverAsksForTheService pins that an env sandbox gets no
// service question, because the unit it would install is the host's own.
func TestSetupInSandboxNeverAsksForTheService(t *testing.T) {
	bin := t.TempDir()
	body := "#!/bin/sh\ncase \"$1 $2\" in\n\"login status\") exit 0;;\n\"debug models\") printf '[{\"slug\":\"gpt-6-luna\",\"visibility\":\"list\",\"priority\":1}]\\n';;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	initRoot(t)
	t.Setenv("PATH", bin)
	t.Setenv("RELEVO_SANDBOX", "test")

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"setup", "--json"})
	})
	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != setup.QuestionsCode {
		t.Fatalf("setup --json = %v, want the questions exit", err)
	}
	var doc setup.SetupQuestionsDoc
	if jerr := json.Unmarshal(stdout, &doc); jerr != nil {
		t.Fatalf("questions doc: %v\n%s", jerr, stdout)
	}
	for _, q := range doc.Questions {
		if q.ID == "service" {
			t.Fatalf("service was asked inside a sandbox: %+v", q)
		}
	}
	if !slices.Contains(doc.Notes, "service: skipped inside an env sandbox; it would replace the host's relevo.service") {
		t.Fatalf("notes = %v, want the sandbox service note", doc.Notes)
	}
}

// TestHintFollowsTheStoredCandidates pins the hint through a real verb: a
// fresh machine gets it, and once setup has stored candidates it is gone.
func TestHintFollowsTheStoredCandidates(t *testing.T) {
	bin := t.TempDir()
	body := "#!/bin/sh\ncase \"$1 $2\" in\n\"login status\") exit 0;;\n\"debug models\") printf '[{\"slug\":\"gpt-6-luna\",\"visibility\":\"list\",\"priority\":1}]\\n';;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	initRoot(t)
	t.Setenv("PATH", bin)

	const hint = "relevo is not set up yet: run relevo setup"
	_, stderr, _ := captureOutput(t, func() error { err := run([]string{"history"}); setup.FlushHint(); return err })
	if !strings.Contains(string(stderr), hint) {
		t.Fatalf("fresh machine stderr = %q, want the setup hint", stderr)
	}
	if _, serr, err := captureOutput(t, func() error {
		return run([]string{"setup",
			"--answer", "builder=codex/openai/gpt-6-luna", "--answer", "planner=codex/openai/gpt-6-luna",
			"--answer", "tier=yolo", "--answer", "plugin=no", "--answer", "service=no", "--answer", "smoke=no"})
	}); err != nil {
		t.Fatalf("setup: %v\n%s", err, serr)
	}
	_, stderr, _ = captureOutput(t, func() error { err := run([]string{"history"}); setup.FlushHint(); return err })
	if strings.Contains(string(stderr), hint) {
		t.Fatalf("configured machine stderr = %q, want no setup hint", stderr)
	}
}

// TestSmokeLimitFailureNamesTheResetAndTheFix pins the usage-limit failure:
// a smoke round whose builder is gated fails shaped as the limit failover,
// naming the builder and its reset and sending the reader back to setup for
// a second builder; a round with no gate is not this failure.
func TestSmokeLimitFailureNamesTheResetAndTheFix(t *testing.T) {
	initRoot(t)
	rt, err := newRuntime()
	if err != nil {
		t.Fatal(err)
	}
	token := "codex/openai/gpt-6-luna:high"
	if err := rt.Store.Save(store.Binding{Name: setup.SmokeName, BuilderCandidate: token, Round: 1, CWD: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := setup.SmokeLimitFailure(context.Background(), rt, dir); err != nil {
		t.Fatalf("an ungated round = %v, want no limit failure", err)
	}

	until := time.Now().Add(72 * time.Hour)
	entry := availability.Entry{Kind: availability.RateLimited, Subject: availability.ProviderOf(token), At: time.Now(), Until: until, Source: "relevo", Binding: setup.SmokeName}
	if err := availability.AppendEntryLocked(relevo.AvailabilityDeps(rt), entry); err != nil {
		t.Fatal(err)
	}
	var le *setup.LimitError
	if lerr := setup.SmokeLimitFailure(context.Background(), rt, dir); !errors.As(lerr, &le) {
		t.Fatalf("gated round = %v (%T), want the limit failover", lerr, lerr)
	} else {
		if le.Next != "relevo setup" {
			t.Errorf("next = %q, want the setup fix", le.Next)
		}
		for _, want := range []string{"gpt-6-luna", "resets " + availability.GateTimeText(until), "pick a second builder"} {
			if !strings.Contains(le.Message, want) {
				t.Errorf("message %q lacks %q", le.Message, want)
			}
		}
	}
	if _, err := rt.Store.Load(setup.SmokeName); err == nil {
		t.Error("the smoke binding outlived a limit failure")
	}
}
