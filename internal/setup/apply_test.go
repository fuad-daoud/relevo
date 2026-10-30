package setup

// setup_apply_test.go exercises the apply steps through their seams: a fake
// runner for the plugin's claude commands and an env sandbox whose stubbed
// codex stands in for the logged-in harness. Nothing reaches a real harness
// or a network.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/doctor"
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

// TestPluginStepFailsNamingTheDoctorsFix pins a failing install: the step
// carries the command's first line and its next is the doctor's own
// two-command fix.
func TestPluginStepFailsNamingTheDoctorsFix(t *testing.T) {
	f := &fakePluginRunner{listOut: "fuad-daoud/relevo", insErr: true, insExit: 1, insOut: "boom: no network"}
	withPluginRunner(t, f)
	_, err := runSetupPlugin(context.Background())
	if err == nil {
		t.Fatal("install failure parsed as success")
	}
	se := requireStepError(t, err, doctor.PluginInstallCommand)
	if se.Message != "boom: no network" {
		t.Errorf("message = %q, want the command's own first line", se.Message)
	}
}

// requireStepError asserts a step failure's shape: its fix and its detail.
func requireStepError(t *testing.T, err error, next string) *StepError {
	t.Helper()
	var se *StepError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want a shaped step failure", err, err)
	}
	if se.Next != next {
		t.Errorf("next = %q, want %q", se.Next, next)
	}
	return se
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
// non-zero exit: its next is the muse fix command, with the command's own
// first line as the detail.
func TestMusePluginStepFailsInstallNamingTheFix(t *testing.T) {
	f := &fakePluginRunner{listOut: museListLine, insErr: true, insExit: 1, insOut: "boom: no network"}
	withPluginRunner(t, f)
	_, err := runSetupMusePlugin(context.Background())
	if err == nil {
		t.Fatal("install failure parsed as success")
	}
	se := requireStepError(t, err, setupMusePluginCommand)
	if se.Message != "boom: no network" {
		t.Errorf("message = %q, want the command's own first line", se.Message)
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
	se := requireStepError(t, err, setupMusePluginCommand)
	if se.Message != "muse: plugin relevo not found" {
		t.Errorf("message = %q, want the command's own first line", se.Message)
	}
	if got := strings.Join(f.calls, ","); got != "list,install,approve" {
		t.Fatalf("calls = %s, want list, install then approve", got)
	}
}

// TestSetupAgentsStepSkipsInSandbox pins that a sandbox run never installs
// agent definitions, which land under the real HOME.
func TestSetupAgentsStepSkipsInSandbox(t *testing.T) {
	var w strings.Builder
	files := Files{Kinds: []string{"codex"}}
	if err := runSetupAgents(nil, files, &w, true); err != nil {
		t.Fatalf("runSetupAgents: %v", err)
	}
	if !strings.Contains(w.String(), sandboxAgentsNote) {
		t.Fatalf("output = %q, want the sandbox note", w.String())
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
