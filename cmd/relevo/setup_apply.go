package main

// cmd/relevo/setup_apply.go owns the apply side: one revision for the
// configuration, the agent definitions for each logged-in harness, then the
// plugin and service installs, each tracked by a step the result document
// carries. The smoke round runs last, in setup_smoke.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/setup"
)

// setupMarketplaceRef and setupPluginToken are the marketplace and the
// plugin install installs from; they are the doctor's own fix commands.
const (
	setupMarketplaceRef = "fuad-daoud/relevo"
	setupPluginToken    = "relevo@relevo"
)

// setupCommandRunner is the CLI seam the plugin step runs through, so a test
// never execs claude: CI has neither the binary nor a marketplace to add.
type setupCommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, int, error)
}

// setupOSRunner is the production seam: stdout and stderr combined, an exit
// code, and a non-nil error only when the command could not run at all.
type setupOSRunner struct{}

func (setupOSRunner) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, ee.ExitCode(), nil
		}
		return out, -1, err
	}
	return out, 0, nil
}

var setupPluginRunner = func() setupCommandRunner { return setupOSRunner{} }

func refreshSetupRuntime() (relevo.Runtime, error) {
	return newRuntime()
}

// setupStepsFor lists the steps the answers ask for, in apply order: config
// and agents always, the optional installs and the smoke round only on yes.
func setupStepsFor(answers setup.Answers) []string {
	steps := []string{"config", "agents"}
	for _, opt := range []string{"plugin", "service", "smoke"} {
		if setupStepYes(answers, opt) {
			steps = append(steps, opt)
		}
	}
	return steps
}

// setupStepYes reports a yes on a yes/no question: both the parsed values
// and the TTY's y/n abbreviations canonicalize, but the y/n light reads
// either way.
func setupStepYes(answers setup.Answers, id string) bool {
	for _, v := range answers[id] {
		if v == "yes" || v == "y" {
			return true
		}
	}
	return false
}

// setupApply runs the steps' bodies in order and stops at the first failure.
// The steps before it stay applied, the printed rows and the document name
// the one that failed, and its coded error decides the exit.
func setupApply(ctx context.Context, refresh func() (relevo.Runtime, error), env setup.SetupEnv, rt relevo.Runtime, inv setup.Inventory, answers setup.Answers, asJSON bool) error {
	w := noticeWriter(asJSON)
	want := setupStepsFor(answers)

	var steps []SetupStepDoc
	var err error
	var files setup.Files
	run := func(step string, do func() (SetupStepDoc, error)) bool {
		doc, serr := do()
		if serr != nil && !doc.OK {
			doc = setupStepDocOf(step, serr)
		}
		steps = append(steps, doc)
		setupPrintStep(w, doc)
		err = serr
		return serr == nil
	}

	goOn := run("config", func() (SetupStepDoc, error) {
		f, summary, cerr := runSetupConfig(rt, inv, answers)
		files = f
		return SetupStepDoc{Step: "config", OK: true, Detail: summary}, cerr
	})
	if goOn {
		if rt, err = refresh(); err != nil {
			return failNext(codeInternal, "relevo doctor", "reload the configuration after the setup write: %v", err)
		}
		goOn = run("agents", func() (SetupStepDoc, error) {
			return SetupStepDoc{Step: "agents", OK: true}, runSetupAgents(env, files, w)
		})
	}
	if goOn && slices.Contains(want, "plugin") {
		goOn = run("plugin", func() (SetupStepDoc, error) {
			return runSetupPlugin(ctx)
		})
	}
	if goOn && slices.Contains(want, "service") {
		goOn = run("service", runSetupService)
	}
	if goOn && slices.Contains(want, "smoke") {
		goOn = run("smoke", func() (SetupStepDoc, error) {
			return runSetupSmoke(ctx, refresh, firstToken(answers["builder"]))
		})
	}

	if err == nil {
		fmt.Fprintln(w, "next: relevo doctor")
		fmt.Fprintln(w, "next: open Claude Code in a repository and ask it to plan something")
	}
	if asJSON {
		_ = printDoc(setupResultDocOf(steps))
	}
	return err
}

// setupPrintStep renders one step's row: the step and what it did on
// success, the reason and its fix on failure.
func setupPrintStep(w io.Writer, doc SetupStepDoc) {
	if doc.OK {
		fmt.Fprintf(w, "%s ok\n", doc.Step)
		if doc.Detail != "" {
			fmt.Fprintf(w, "  %s\n", doc.Detail)
		}
		return
	}
	fmt.Fprintf(w, "%s FAILED: %s\n", doc.Step, doc.Detail)
	if doc.Next != "" {
		fmt.Fprintf(w, "  next: %s\n", doc.Next)
	}
}

// firstToken is a multi answer's first pick, the smoke round's builder.
func firstToken(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// runSetupConfig writes Build's three sections as one revision, like init
// does, and names the actors its summary line carries.
func runSetupConfig(rt relevo.Runtime, inv setup.Inventory, answers setup.Answers) (setup.Files, string, error) {
	files, err := setup.Build(inv, answers)
	if err != nil {
		return setup.Files{}, "", setupBuildError(err)
	}
	if err := putSetupSections(rt, files); err != nil {
		return setup.Files{}, "", err
	}
	summary, err := actorSummary(files.Actors, files.ActorOrder)
	return files, summary, err
}

// putSetupSections is the config write: candidates, policy and actors land
// together as one named revision, so the revision history can roll it back.
func putSetupSections(rt relevo.Runtime, files setup.Files) error {
	doc := map[config.Section]json.RawMessage{
		config.Candidates: files.Candidates,
		config.Policy:     files.Policy,
		config.Actors:     files.Actors,
	}
	_, err := rt.Config.As("setup", "relevo setup").PutDoc(doc)
	return err
}

func setupBuildError(err error) error {
	if isNotAvailable(err) {
		var na *setup.NotAvailableError
		_ = errors.As(err, &na)
		return failNext(codeNotAvailable, "relevo setup", "%s", na.Message)
	}
	return fail(codeInternal, "%v", err)
}

// runSetupAgents installs the agent definitions for each harness the
// configuration names as logged in, one result line to the notice writer, a
// per-file error failing the step the way init does.
func runSetupAgents(env setup.SetupEnv, files setup.Files, w io.Writer) error {
	for _, kind := range files.Kinds {
		results, err := harness.Install(env, harness.InstallOptions{Kind: kind})
		if err != nil {
			return err
		}
		for _, r := range results {
			fmt.Fprintln(w, r.Line())
			if r.Outcome == harness.OutcomeError {
				return fail(codeInternal, "agent definitions for %s failed to install", kind)
			}
		}
	}
	return nil
}

// runSetupPlugin installs the Claude Code plugin through the seam: add the
// marketplace only when a marketplace list does not already name it, then
// install, so an already-added marketplace is never a failure.
func runSetupPlugin(ctx context.Context) (SetupStepDoc, error) {
	r := setupPluginRunner()
	listOut, listExit, _ := r.Run(ctx, "claude", "plugin", "marketplace", "list")
	if !setupMarketplaceAdded(listOut, listExit) {
		if _, exit, rerr := r.Run(ctx, "claude", "plugin", "marketplace", "add", setupMarketplaceRef); rerr != nil || exit != 0 {
			return SetupStepDoc{}, failNext(codeInternal, doctor.PluginInstallCommand, "the marketplace add failed")
		}
	}
	out, exit, rerr := r.Run(ctx, "claude", "plugin", "install", setupPluginToken)
	if rerr != nil || exit != 0 {
		detail := "the plugin install failed"
		if line := strings.TrimSpace(string(out)); line != "" {
			detail = firstLine(line)
		}
		return SetupStepDoc{}, failNext(codeInternal, doctor.PluginInstallCommand, "%s", detail)
	}
	return SetupStepDoc{Step: "plugin", OK: true}, nil
}

// setupMarketplaceAdded reports whether the marketplace list already names
// the repository: a list exit says nothing when claude answered nothing, and
// a reader with the marketplace present must not pay for the add again.
func setupMarketplaceAdded(out []byte, exit int) bool {
	return exit == 0 && bytes.Contains(out, []byte(setupMarketplaceRef))
}

// runSetupService does what `relevo service install` does: write the unit
// unless it was edited by hand (conflict), then enable and restart it.
func runSetupService() (SetupStepDoc, error) {
	u, err := osUnitSpec()
	if err != nil {
		return SetupStepDoc{}, fail(codeInternal, "%v", err)
	}
	_, err = installServiceUnit(u, newServiceRunner(), false)
	if err != nil {
		return SetupStepDoc{}, err
	}
	return SetupStepDoc{Step: "service", OK: true}, nil
}
