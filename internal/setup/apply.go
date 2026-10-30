package setup

// apply.go owns the apply side: one revision for the configuration, the
// agent definitions for each logged-in harness, then the plugin and service
// installs, each tracked by a step the result document carries. The smoke
// round runs last, in smoke.go.

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
)

// setupMarketplaceRef and setupPluginToken are the marketplace and the
// plugin install installs from; they are the doctor's own fix commands.
const (
	setupMarketplaceRef = "fuad-daoud/relevo"
	setupPluginToken    = "relevo@relevo"
)

// setupMuseName and setupMuseMarketplaceURL name the marketplace the muse
// plugin install adds, and setupMusePluginCommand is the fix a failed
// muse-plugin step names: the same three commands, in order.
const (
	setupMuseName           = "relevo"
	setupMuseMarketplaceURL = "https://github.com/fuad-daoud/relevo.git"
	setupMusePluginCommand  = "muse plugins marketplace add relevo https://github.com/fuad-daoud/relevo.git && muse plugins install relevo@relevo && muse plugins approve relevo"
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

// setupStepsFor lists the steps the answers ask for, in apply order: config
// and agents always, the optional installs and the smoke round only on yes.
func setupStepsFor(answers Answers) []string {
	steps := []string{"config", "agents"}
	for _, opt := range []string{"plugin", "muse-plugin", "service", "smoke"} {
		if setupStepYes(answers, opt) {
			steps = append(steps, opt)
		}
	}
	return steps
}

// setupStepYes reports a yes on a yes/no question: both the parsed values
// and the TTY's y/n abbreviations canonicalize, but the y/n light reads
// either way.
func setupStepYes(answers Answers, id string) bool {
	for _, v := range answers[id] {
		if v == "yes" || v == "y" {
			return true
		}
	}
	return false
}

// setupApply runs the steps' bodies in order and stops at the first failure.
// The steps before it stay applied, the printed rows and the document name
// the one that failed, and the shaped error it returns decides the exit.
func setupApply(ctx context.Context, deps Deps, rt relevo.Runtime, inv Inventory, answers Answers, asJSON bool) error {
	w := deps.Notices
	want := setupStepsFor(answers)

	var steps []SetupStepDoc
	var err error
	var files Files
	run := func(step string, do func() (SetupStepDoc, error)) bool {
		if start := setupStepStarts[step]; start != "" {
			fmt.Fprintf(w, "%s: %s...\n", step, start)
		}
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
		f, summary, cerr := runSetupConfig(rt, deps, inv, answers)
		files = f
		return SetupStepDoc{Step: "config", OK: true, Detail: summary}, cerr
	})
	if goOn {
		goOn = run("agents", func() (SetupStepDoc, error) {
			if _, rerr := deps.Refresh(); rerr != nil {
				return SetupStepDoc{}, &StepError{Message: fmt.Sprintf("reload the configuration after the setup write: %v", rerr), Next: "relevo doctor"}
			}
			return SetupStepDoc{Step: "agents", OK: true}, runSetupAgents(deps.Env, files, w, deps.InSandbox)
		})
	}
	if goOn && slices.Contains(want, "plugin") {
		goOn = run("plugin", func() (SetupStepDoc, error) {
			return runSetupPlugin(ctx)
		})
	}
	if goOn && slices.Contains(want, "muse-plugin") {
		goOn = run("muse-plugin", func() (SetupStepDoc, error) {
			return runSetupMusePlugin(ctx)
		})
	}
	if goOn && slices.Contains(want, "service") {
		goOn = run("service", func() (SetupStepDoc, error) {
			return runSetupService(deps.InstallService)
		})
	}
	if goOn && slices.Contains(want, "smoke") {
		goOn = run("smoke", func() (SetupStepDoc, error) {
			return runSetupSmoke(ctx, deps, firstToken(answers["builder"]))
		})
	}

	if err == nil {
		fmt.Fprintln(w, "next: relevo doctor")
		fmt.Fprintln(w, "next: "+setupOpenHint(want))
	}
	if asJSON {
		_ = deps.PrintDoc(setupResultDocOf(steps))
	}
	return silenceStep(err)
}

// silenceStep shapes a step failure for the caller: StepError, LimitError
// and NotAvailableError already carry their row; anything else failed after
// its row printed, so it becomes a StepError with the row's own detail.
func silenceStep(err error) error {
	if err == nil {
		return nil
	}
	var se *StepError
	if errors.As(err, &se) {
		return err
	}
	var ce *CodedStepError
	if errors.As(err, &ce) {
		return err
	}
	var le *LimitError
	if errors.As(err, &le) {
		return err
	}
	var na *NotAvailableError
	if errors.As(err, &na) {
		return err
	}
	return &StepError{Message: firstLine(err.Error())}
}

// setupOpenHint is the closing hint: open the MasterMind harness the plugin
// went into. A setup that installed neither plugin names Claude Code, the
// harness the README walks through first.
func setupOpenHint(want []string) string {
	harness := "Claude Code"
	switch {
	case slices.Contains(want, "muse-plugin") && slices.Contains(want, "plugin"):
		harness = "Claude Code or muse"
	case slices.Contains(want, "muse-plugin"):
		harness = "muse"
	}
	return "open " + harness + " in a repository and ask it to plan something"
}

// setupStepStarts are the lines a step prints before it starts, so a step
// that takes seconds never reads as a hang: each waits on a CLI, the daemon
// or a builder.
var setupStepStarts = map[string]string{
	"agents":      "loading the new configuration and installing agent definitions",
	"plugin":      "installing the relevo plugin into Claude Code",
	"muse-plugin": "installing the relevo plugin into muse",
	"service":     "installing and starting the relevo user service",
	"smoke":       "binding a builder to a throwaway repository and sending it one small plan; this takes a minute or two",
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
func runSetupConfig(rt relevo.Runtime, deps Deps, inv Inventory, answers Answers) (Files, string, error) {
	files, err := Build(inv, answers)
	if err != nil {
		return Files{}, "", err
	}
	if err := putSetupSections(rt, files); err != nil {
		return Files{}, "", err
	}
	summary, err := deps.ActorSummary(files.Actors, files.ActorOrder)
	return files, summary, err
}

// putSetupSections is the config write: candidates, policy and actors land
// together as one named revision, so the revision history can roll it back.
func putSetupSections(rt relevo.Runtime, files Files) error {
	doc := map[config.Section]json.RawMessage{
		config.Candidates: files.Candidates,
		config.Policy:     files.Policy,
		config.Actors:     files.Actors,
	}
	_, err := rt.Config.As("setup", "relevo setup").PutDoc(doc)
	return err
}

// runSetupAgents installs the agent definitions for each harness the
// configuration names as logged in, one result line to the notice writer, a
// per-file error failing the step the way init does.
func runSetupAgents(env SetupEnv, files Files, w io.Writer, inSandbox bool) error {
	// Agent definitions install under the real HOME, not the sandbox's, so a
	// sandbox run would rewrite the developer's own files.
	if inSandbox {
		fmt.Fprintln(w, sandboxAgentsNote)
		return nil
	}
	for _, kind := range files.Kinds {
		results, err := harness.Install(env, harness.InstallOptions{Kind: kind})
		if err != nil {
			return err
		}
		for _, r := range results {
			fmt.Fprintln(w, r.Line())
			if r.Outcome == harness.OutcomeError {
				return fmt.Errorf("agent definitions for %s failed to install", kind)
			}
		}
	}
	return nil
}

const sandboxAgentsNote = "agents: skipped inside an env sandbox; relevo-sandbox.sh create copied them"

// runSetupPlugin installs the Claude Code plugin through the seam: add the
// marketplace only when a marketplace list does not already name it, then
// install, so an already-added marketplace is never a failure.
func runSetupPlugin(ctx context.Context) (SetupStepDoc, error) {
	r := setupPluginRunner()
	listOut, listExit, _ := r.Run(ctx, "claude", "plugin", "marketplace", "list")
	if !setupMarketplaceAdded(listOut, listExit) {
		if _, exit, rerr := r.Run(ctx, "claude", "plugin", "marketplace", "add", setupMarketplaceRef); rerr != nil || exit != 0 {
			return SetupStepDoc{}, &StepError{Message: "the marketplace add failed", Next: doctor.PluginInstallCommand}
		}
	}
	out, exit, rerr := r.Run(ctx, "claude", "plugin", "install", setupPluginToken)
	if rerr != nil || exit != 0 {
		detail := "the plugin install failed"
		if line := strings.TrimSpace(string(out)); line != "" {
			detail = firstLine(line)
		}
		return SetupStepDoc{}, &StepError{Message: detail, Next: doctor.PluginInstallCommand}
	}
	return SetupStepDoc{Step: "plugin", OK: true}, nil
}

// setupMarketplaceAdded reports whether the marketplace list already names
// the repository: a list exit says nothing when claude answered nothing, and
// a reader with the marketplace present must not pay for the add again.
func setupMarketplaceAdded(out []byte, exit int) bool {
	return exit == 0 && bytes.Contains(out, []byte(setupMarketplaceRef))
}

// runSetupMusePlugin installs the muse plugin through the same seam the
// Claude Code step uses, in order: add the marketplace only when a
// marketplace list does not already name it, install, then approve, which
// muse requires before its hooks and MCP servers run. An install that exits
// 0 is a success however its first line reads -- "already installed" at
// exit 0 included -- mirroring runSetupPlugin, which never parses install
// stdout on success.
func runSetupMusePlugin(ctx context.Context) (SetupStepDoc, error) {
	r := setupPluginRunner()
	listOut, listExit, _ := r.Run(ctx, "muse", "plugins", "marketplace", "list")
	if !setupMarketplaceAdded(listOut, listExit) {
		if _, exit, rerr := r.Run(ctx, "muse", "plugins", "marketplace", "add", setupMuseName, setupMuseMarketplaceURL); rerr != nil || exit != 0 {
			return SetupStepDoc{}, &StepError{Message: "the marketplace add failed", Next: setupMusePluginCommand}
		}
	}
	out, exit, rerr := r.Run(ctx, "muse", "plugins", "install", setupPluginToken)
	if rerr != nil || exit != 0 {
		detail := "the plugin install failed"
		if line := strings.TrimSpace(string(out)); line != "" {
			detail = firstLine(line)
		}
		return SetupStepDoc{}, &StepError{Message: detail, Next: setupMusePluginCommand}
	}
	appOut, appExit, appErr := r.Run(ctx, "muse", "plugins", "approve", setupMuseName)
	if appErr != nil || appExit != 0 {
		detail := "the plugin approval failed"
		if line := strings.TrimSpace(string(appOut)); line != "" {
			detail = firstLine(line)
		}
		return SetupStepDoc{}, &StepError{Message: detail, Next: setupMusePluginCommand}
	}
	return SetupStepDoc{Step: "muse-plugin", OK: true}, nil
}

// runSetupService does what `relevo service install` does: write the unit
// unless it was edited by hand (conflict), then enable and restart it. The
// install itself lives in the caller, already shaped as a StepError.
func runSetupService(install func() error) (SetupStepDoc, error) {
	if err := install(); err != nil {
		return SetupStepDoc{}, err
	}
	return SetupStepDoc{Step: "service", OK: true}, nil
}
