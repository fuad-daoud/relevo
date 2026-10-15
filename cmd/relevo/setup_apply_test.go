package main

// cmd/relevo/setup_apply_test.go exercises the apply steps through their
// seams: a fake runner for the plugin's claude commands, the service test's
// own fake init runner, and an env sandbox whose stubbed codex stands in for
// the logged-in harness. Nothing reaches a real harness or a network.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/serviceunit"
)

// fakePluginRunner answers every claude call from a script and records them,
// so the plugin step's own order is what a test asserts.
type fakePluginRunner struct {
	calls   []string
	listOut string
	listErr bool
	addOut  string
	addExit int
	addErr  bool
	insOut  string
	insExit int
	insErr  bool
}

func (f *fakePluginRunner) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	joined := name + " " + strings.Join(args, " ")
	if strings.Contains(joined, "list") {
		f.calls = append(f.calls, "list")
		if f.listErr {
			return nil, 1, context.DeadlineExceeded
		}
		return []byte(f.listOut), 0, nil
	}
	if strings.Contains(joined, "add") {
		f.calls = append(f.calls, "add")
		if f.addErr {
			return []byte(f.addOut), f.addExit, nil
		}
		return []byte(f.addOut), f.addExit, nil
	}
	f.calls = append(f.calls, "install")
	if f.insErr {
		return []byte(f.insOut), f.insExit, nil
	}
	return []byte(f.insOut), f.insExit, nil
}

// withPluginRunner puts a fake plugin runner in place for one test.
func withPluginRunner(t *testing.T, f *fakePluginRunner) {
	t.Helper()
	orig := setupPluginRunner
	setupPluginRunner = func() setupCommandRunner { return f }
	t.Cleanup(func() { setupPluginRunner = orig })
}

// TestPluginStepInstallsWithoutASecondAdd pins the happy path: a marketplace
// list naming the repository means no add runs, and only the install follows.
func TestPluginStepInstallsWithoutASecondAdd(t *testing.T) {
	f := &fakePluginRunner{listOut: "fuad-daoud/relevo"}
	withPluginRunner(t, f)
	doc, err := runSetupPlugin(context.Background())
	if err != nil {
		t.Fatalf("runSetupPlugin: %v", err)
	}
	if !doc.OK || doc.Step != "plugin" {
		t.Fatalf("doc = %+v, want a passed plugin row", doc)
	}
	if strings.Join(f.calls, ",") != "list,install" {
		t.Fatalf("calls = %v, want list then install only", f.calls)
	}
}

// TestPluginStepAddsAMissingMarketplace pins the other end: a list without
// the repository name runs the add first, then the install.
func TestPluginStepAddsAMissingMarketplace(t *testing.T) {
	f := &fakePluginRunner{listOut: "other/market"}
	withPluginRunner(t, f)
	if _, err := runSetupPlugin(context.Background()); err != nil {
		t.Fatalf("runSetupPlugin: %v", err)
	}
	if got := strings.Join(f.calls, ","); got != "list,add,install" {
		t.Fatalf("calls = %s, want list, add then install", got)
	}
}

// TestPluginStepKeepsAFailedListFromBlocking pins that a failing list does
// not stop the install: the add decides, as the doctor's fix command does.
func TestPluginStepKeepsAFailedListFromBlocking(t *testing.T) {
	f := &fakePluginRunner{listErr: true}
	withPluginRunner(t, f)
	if _, err := runSetupPlugin(context.Background()); err != nil {
		t.Fatalf("runSetupPlugin: %v", err)
	}
	if got := strings.Join(f.calls, ","); got != "list,add,install" {
		t.Fatalf("calls = %s, want the add still attempted", got)
	}
}

// TestPluginStepFailsNamingTheDoctorsFix pins a failing install: the step's
// error code is internal and its next is the doctor's own two-command fix.
func TestPluginStepFailsNamingTheDoctorsFix(t *testing.T) {
	f := &fakePluginRunner{listOut: "fuad-daoud/relevo", insErr: true, insExit: 1, insOut: "boom: no network"}
	withPluginRunner(t, f)
	_, err := runSetupPlugin(context.Background())
	if err == nil {
		t.Fatal("install failure parsed as success")
	}
	ce := requireCLIError(t, err, codeInternal, doctor.PluginInstallCommand)
	if ce.message != "boom: no network" {
		t.Errorf("message = %q, want the command's own first line", ce.message)
	}
}

// TestServiceStepInstallsTheUnit pins the service step: it writes the unit
// as `relevo service install` does (through the fake init runner), and the
// step passes with nothing to print.
func TestServiceStepInstallsTheUnit(t *testing.T) {
	if _, configHome := initRoot(t); configHome == "" {
		t.Fatal("initRoot gave no config root")
	}
	orig := newServiceRunner
	newServiceRunner = func() serviceunit.Runner { runner := &fakeRunner{}; return runner }
	t.Cleanup(func() { newServiceRunner = orig })

	doc, err := runSetupService()
	if err != nil {
		t.Fatalf("runSetupService: %v", err)
	}
	if !doc.OK || doc.Step != "service" {
		t.Fatalf("doc = %+v, want a passed service row", doc)
	}
	configRoot, err := userConfigRoot()
	if err != nil {
		t.Fatalf("userConfigRoot: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(configRoot, "systemd", "user", "relevo.service")); statErr != nil {
		t.Fatalf("unit was not written: %v", statErr)
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
	codexFiles := filepath.Join(homeOf(), ".codex", "plan-executor.config.toml")
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
