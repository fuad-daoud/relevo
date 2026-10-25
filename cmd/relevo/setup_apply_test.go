package main

// cmd/relevo/setup_apply_test.go exercises the apply steps through their
// seams: a fake runner for the plugin's claude commands, the service test's
// own fake init runner, and an env sandbox whose stubbed codex stands in for
// the logged-in harness. Nothing reaches a real harness or a network.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/serviceunit"
	"github.com/fuad-daoud/relevo/internal/setup"
	"github.com/fuad-daoud/relevo/internal/store"
)

// fakePluginRunner answers every claude and muse plugin call from a script
// and records them, so the plugin steps' own order is what a test asserts.
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
	appOut  string
	appExit int
	appErr  bool
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
	if strings.Contains(joined, "approve") {
		f.calls = append(f.calls, "approve")
		if f.appErr {
			return []byte(f.appOut), f.appExit, nil
		}
		return []byte(f.appOut), f.appExit, nil
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

// museListLine is what `muse plugins marketplace list` prints once the
// marketplace is there: the line names the repository URL, which contains
// the setupMarketplaceRef the shared checker looks for.
const museListLine = "relevo\tplugins=1\tsource=https://github.com/fuad-daoud/relevo.git"

// TestMusePluginStepInstallsWithoutASecondAdd pins the happy path: a
// marketplace list naming the repository means no add runs, and only the
// install and the approve follow.
func TestMusePluginStepInstallsWithoutASecondAdd(t *testing.T) {
	f := &fakePluginRunner{listOut: museListLine}
	withPluginRunner(t, f)
	doc, err := runSetupMusePlugin(context.Background())
	if err != nil {
		t.Fatalf("runSetupMusePlugin: %v", err)
	}
	if !doc.OK || doc.Step != "muse-plugin" {
		t.Fatalf("doc = %+v, want a passed muse-plugin row", doc)
	}
	if strings.Join(f.calls, ",") != "list,install,approve" {
		t.Fatalf("calls = %v, want list then install then approve only", f.calls)
	}
}

// TestMusePluginStepAddsAMissingMarketplace pins the other end: a list
// without the repository name runs the add first, then install, then approve.
func TestMusePluginStepAddsAMissingMarketplace(t *testing.T) {
	f := &fakePluginRunner{listOut: "other/market"}
	withPluginRunner(t, f)
	if _, err := runSetupMusePlugin(context.Background()); err != nil {
		t.Fatalf("runSetupMusePlugin: %v", err)
	}
	if got := strings.Join(f.calls, ","); got != "list,add,install,approve" {
		t.Fatalf("calls = %s, want list, add, install then approve", got)
	}
}

// TestMusePluginStepKeepsAFailedListFromBlocking pins that a failing list
// does not stop the install: the add decides, as the Claude Code step does.
func TestMusePluginStepKeepsAFailedListFromBlocking(t *testing.T) {
	f := &fakePluginRunner{listErr: true}
	withPluginRunner(t, f)
	if _, err := runSetupMusePlugin(context.Background()); err != nil {
		t.Fatalf("runSetupMusePlugin: %v", err)
	}
	if got := strings.Join(f.calls, ","); got != "list,add,install,approve" {
		t.Fatalf("calls = %s, want the add still attempted", got)
	}
}

// TestMusePluginStepFailsInstallNamingTheFix pins a failing install at a
// non-zero exit: the step's error code is internal and its next is the muse
// fix command, with the command's own first line as the detail.
func TestMusePluginStepFailsInstallNamingTheFix(t *testing.T) {
	f := &fakePluginRunner{listOut: museListLine, insErr: true, insExit: 1, insOut: "boom: no network"}
	withPluginRunner(t, f)
	_, err := runSetupMusePlugin(context.Background())
	if err == nil {
		t.Fatal("install failure parsed as success")
	}
	ce := requireCLIError(t, err, codeInternal, setupMusePluginCommand)
	if ce.message != "boom: no network" {
		t.Errorf("message = %q, want the command's own first line", ce.message)
	}
	if got := strings.Join(f.calls, ","); got != "list,install" {
		t.Fatalf("calls = %s, want the approve never reached", got)
	}
}

// TestMusePluginStepTreatsAlreadyInstalledAsSuccess pins that an install
// exit 0 is a success however its first line reads: an "already installed"
// line passes the step and the approve still runs, mirroring runSetupPlugin,
// which checks only exit and runner error and never parses install stdout.
func TestMusePluginStepTreatsAlreadyInstalledAsSuccess(t *testing.T) {
	f := &fakePluginRunner{listOut: museListLine, insOut: "already installed: relevo@relevo"}
	withPluginRunner(t, f)
	doc, err := runSetupMusePlugin(context.Background())
	if err != nil {
		t.Fatalf("already-installed first line failed the step: %v", err)
	}
	if !doc.OK {
		t.Fatalf("doc = %+v, want a passed muse-plugin row", doc)
	}
	if got := strings.Join(f.calls, ","); got != "list,install,approve" {
		t.Fatalf("calls = %s, want list, install then approve", got)
	}
}

// TestMusePluginStepFailsWhenApproveFails pins the approval: a non-zero
// approve exit fails the step naming the muse fix, with the command's own
// first line as the detail.
func TestMusePluginStepFailsWhenApproveFails(t *testing.T) {
	f := &fakePluginRunner{listOut: museListLine, appOut: "muse: plugin relevo not found", appExit: 1}
	withPluginRunner(t, f)
	_, err := runSetupMusePlugin(context.Background())
	if err == nil {
		t.Fatal("approve failure parsed as success")
	}
	ce := requireCLIError(t, err, codeInternal, setupMusePluginCommand)
	if ce.message != "muse: plugin relevo not found" {
		t.Errorf("message = %q, want the command's own first line", ce.message)
	}
	if got := strings.Join(f.calls, ","); got != "list,install,approve" {
		t.Fatalf("calls = %s, want list, install then approve", got)
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
	// The unit lands where this OS keeps it: systemd's user dir on Linux,
	// LaunchAgents on macOS.
	u, err := osUnitSpec()
	if err != nil {
		t.Fatalf("osUnitSpec: %v", err)
	}
	if _, statErr := os.Stat(u.path); statErr != nil {
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
	if !errors.As(err, &ec) || ec.code != setupQuestionsCode {
		t.Fatalf("setup --json = %v, want the questions exit", err)
	}
	var doc SetupQuestionsDoc
	if jerr := json.Unmarshal(stdout, &doc); jerr != nil {
		t.Fatalf("questions doc: %v\n%s", jerr, stdout)
	}
	for _, q := range doc.Questions {
		if q.ID == "service" {
			t.Fatalf("service was asked inside a sandbox: %+v", q)
		}
	}
	if !slices.Contains(doc.Notes, sandboxServiceNote) {
		t.Fatalf("notes = %v, want the sandbox service note", doc.Notes)
	}
}

// TestSetupPluginCheckReadsClaudeConfigDir pins that the plugin question
// follows CLAUDE_CONFIG_DIR, where Claude Code keeps its plugins when set.
func TestSetupPluginCheckReadsClaudeConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if got := claudeConfigDir(); got != dir {
		t.Fatalf("claudeConfigDir = %q, want %q", got, dir)
	}
	if doctor.RelevoPluginInstalledIn(dir) {
		t.Fatalf("an empty config dir reads as installed")
	}
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := `{"version":2,"plugins":{"relevo@relevo":[{"scope":"user","version":"0.17.3"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "plugins", "installed_plugins.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	if !doctor.RelevoPluginInstalledIn(dir) {
		t.Fatalf("installed_plugins.json under CLAUDE_CONFIG_DIR not read")
	}
}

// TestSmokeRepoCommitsTheReadme pins the throwaway repository the smoke
// round binds: initialised, with README.md committed.
func TestSmokeRepoCommitsTheReadme(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := smokeRepo(repo); err != nil {
		t.Fatalf("smokeRepo: %v", err)
	}
	out, err := exec.Command("git", "-C", repo, "ls-files").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	if strings.TrimSpace(string(out)) != "README.md" {
		t.Fatalf("committed files = %q, want README.md", out)
	}
}

// TestSetupAgentsStepSkipsInSandbox pins that a sandbox run never installs
// agent definitions, which land under the real HOME.
func TestSetupAgentsStepSkipsInSandbox(t *testing.T) {
	t.Setenv("RELEVO_SANDBOX", "test")
	var w strings.Builder
	files := setup.Files{Kinds: []string{"codex"}}
	if err := runSetupAgents(nil, files, &w); err != nil {
		t.Fatalf("runSetupAgents: %v", err)
	}
	if !strings.Contains(w.String(), sandboxAgentsNote) {
		t.Fatalf("output = %q, want the sandbox note", w.String())
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

	t.Cleanup(func() { setupHintDue = false })
	_, stderr, _ := captureOutput(t, func() error { err := run([]string{"history"}); flushSetupHint(); return err })
	if !strings.Contains(string(stderr), setupHint) {
		t.Fatalf("fresh machine stderr = %q, want the setup hint", stderr)
	}
	if _, serr, err := captureOutput(t, func() error {
		return run([]string{"setup",
			"--answer", "builder=codex/openai/gpt-6-luna", "--answer", "planner=codex/openai/gpt-6-luna",
			"--answer", "tier=yolo", "--answer", "plugin=no", "--answer", "service=no", "--answer", "smoke=no"})
	}); err != nil {
		t.Fatalf("setup: %v\n%s", err, serr)
	}
	_, stderr, _ = captureOutput(t, func() error { err := run([]string{"history"}); flushSetupHint(); return err })
	if strings.Contains(string(stderr), setupHint) {
		t.Fatalf("configured machine stderr = %q, want no setup hint", stderr)
	}
}

// TestStatusOnAFreshMachineListsNothing pins a newcomer's first command: with
// no MasterMind registered anywhere, status succeeds with an empty listing
// instead of refusing for want of one.
func TestStatusOnAFreshMachineListsNothing(t *testing.T) {
	initRoot(t)
	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"status"}) })
	if err != nil {
		t.Fatalf("status on a fresh machine: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(string(stdout), "no bindings") {
		t.Fatalf("stdout = %q, want the empty listing", stdout)
	}
}

// TestStatusShapesOnAFreshMachineSaySoInsteadOfRefusing pins the status
// shapes a statusline or a newcomer runs before any MasterMind registers:
// --line names the missing MasterMind rather than failing the prompt, --line
// --json still refuses (a null mastermind reads as a healthy session), and
// --chains lists nothing.
func TestStatusShapesOnAFreshMachineSaySoInsteadOfRefusing(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"status", "--line"}, "relevo: no MasterMind yet"},
		{[]string{"status", "--chains"}, ""},
		{[]string{"status", "--chains", "--json"}, "{"},
	} {
		t.Run(strings.Join(c.args[1:], " "), func(t *testing.T) {
			initRoot(t)
			t.Setenv("CLAUDECODE", "")
			t.Setenv("RELEVO_MASTERMIND", "")
			stdout, stderr, err := captureOutput(t, func() error { return run(c.args) })
			if err != nil {
				t.Fatalf("%v on a fresh machine: %v\nstderr: %s", c.args, err, stderr)
			}
			if !strings.Contains(string(stdout), c.want) {
				t.Fatalf("stdout = %q, want %q", stdout, c.want)
			}
		})
	}
	t.Run("line json", func(t *testing.T) {
		initRoot(t)
		t.Setenv("CLAUDECODE", "")
		t.Setenv("RELEVO_MASTERMIND", "")
		_, _, err := captureOutput(t, func() error { return run([]string{"status", "--line", "--json"}) })
		requireCLIError(t, err, codeUsage, "relevo mastermind list")
	})
}

// TestSmokeLimitFailureNamesTheResetAndTheFix pins the usage-limit failure:
// a smoke round whose builder is gated fails as gate_active, naming the
// builder and its reset and sending the reader back to setup for a second
// builder; a round with no gate is not this failure.
func TestSmokeLimitFailureNamesTheResetAndTheFix(t *testing.T) {
	initRoot(t)
	rt, err := newRuntime()
	if err != nil {
		t.Fatal(err)
	}
	token := "codex/openai/gpt-6-luna:high"
	if err := rt.Store.Save(store.Binding{Name: smokeName, BuilderCandidate: token, Round: 1, CWD: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := smokeLimitFailure(context.Background(), rt, dir); err != nil {
		t.Fatalf("an ungated round = %v, want no limit failure", err)
	}

	until := time.Now().Add(72 * time.Hour)
	entry := availability.Entry{Kind: availability.RateLimited, Subject: availability.ProviderOf(token), At: time.Now(), Until: until, Source: "relevo", Binding: smokeName}
	if err := availability.AppendEntryLocked(relevo.AvailabilityDeps(rt), entry); err != nil {
		t.Fatal(err)
	}
	ce := requireCLIError(t, smokeLimitFailure(context.Background(), rt, dir), codeGateActive, "relevo setup")
	for _, want := range []string{"gpt-6-luna", "resets " + availability.GateTimeText(until), "pick a second builder"} {
		if !strings.Contains(ce.message, want) {
			t.Errorf("message %q lacks %q", ce.message, want)
		}
	}
	if _, err := rt.Store.Load(smokeName); err == nil {
		t.Error("the smoke binding outlived a limit failure")
	}
}

// TestSetupExitIsSilentAfterAFailedRow pins that a failed step is printed
// once: the apply hands main a bare exit code carrying the step's own exit.
func TestSetupExitIsSilentAfterAFailedRow(t *testing.T) {
	got := setupExitOf(fail(codeGateActive, "limited"))
	var ce *cliError
	if errors.As(got, &ce) {
		t.Fatalf("setupExitOf = %v, a cliError main would print a second time", got)
	}
	var ec exitCodeErr
	if !errors.As(got, &ec) || ec.code != catalogExit(codeGateActive) {
		t.Fatalf("setupExitOf = %v, want the gate_active exit", got)
	}
	if setupExitOf(nil) != nil {
		t.Fatal("a clean apply exits with an error")
	}
}

// TestSetupOpenHintNamesTheHarnessThePluginWentInto pins the closing hint:
// muse alone points at muse, both name both, and neither keeps Claude Code.
func TestSetupOpenHintNamesTheHarnessThePluginWentInto(t *testing.T) {
	cases := []struct {
		want []string
		hint string
	}{
		{[]string{"config", "agents"}, "open Claude Code in a repository and ask it to plan something"},
		{[]string{"config", "agents", "plugin"}, "open Claude Code in a repository and ask it to plan something"},
		{[]string{"config", "agents", "muse-plugin"}, "open muse in a repository and ask it to plan something"},
		{[]string{"config", "agents", "plugin", "muse-plugin"}, "open Claude Code or muse in a repository and ask it to plan something"},
	}
	for _, tc := range cases {
		if got := setupOpenHint(tc.want); got != tc.hint {
			t.Errorf("setupOpenHint(%v) = %q, want %q", tc.want, got, tc.hint)
		}
	}
}
