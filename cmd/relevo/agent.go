package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/setup"
)

// installAccountHomes installs each configured claude and codex account's role
// definitions into that account's own home. opts.Kind limits the walk when set;
// an account of another kind has no per-process home and is skipped.
func installAccountHomes(env harness.InstallEnv, accounts account.Set, opts harness.InstallOptions) ([]harness.InstallResult, error) {
	var out []harness.InstallResult
	for _, a := range accounts {
		kind := string(a.Harness)
		if opts.Kind != "" && opts.Kind != kind {
			continue
		}
		home, ok := harness.AccountHome(a)
		if !ok {
			continue
		}
		res, err := harness.InstallAccountHome(env, harness.InstallOptions{
			Kind:   kind,
			Role:   opts.Role,
			Force:  opts.Force,
			DryRun: opts.DryRun,
		}, kind, home)
		if err != nil {
			return out, err
		}
		out = append(out, res...)
	}
	return out, nil
}

// agentInstallEnv is the SetupEnv `relevo config agents` and the daemon's
// role refresh share: the OS-backed env over relevo.AgentInstallEnv.
func agentInstallEnv() (setup.SetupEnv, error) {
	return setup.OSEnv()
}

type agentFlagValues struct {
	kind   *string
	agent  *string
	force  *bool
	dryRun *bool
	asJSON *bool
}

// agentFlagSet exists so a test can inspect the flag surface without installing
// anything.
func agentFlagSet(fs *flag.FlagSet) *agentFlagValues {
	v := &agentFlagValues{}
	v.kind = fs.String("kind", "", "harness kind")
	v.agent = fs.String("agent", "", "agent name")
	v.force = fs.Bool("force", false, "force overwrite")
	v.dryRun = fs.Bool("dry-run", false, "dry run")
	v.asJSON = fs.Bool("json", false, "print the document the install produced")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "usage: relevo config agents [--kind <agy|claude|opencode>] [--agent <name>] [--force] [--dry-run]")
		_, _ = fmt.Fprintln(fs.Output(), "Installs relevo's shipped agents and the custom agents in your config.")
		_, _ = fmt.Fprintln(fs.Output(), "For opencode it also installs the relevo OpenCode plugin (~/.config/opencode/plugins/relevo).")
	}
	return v
}

func cmdAgentInstall(args []string) error {
	return outcomeError(cmdAgentInstallRun(args))
}

// agentInstallInput is the validated input cmdAgentInstallRun acts on: the
// parsed flags as install options, the env they install into, and the machine
// config's custom agents with the load failure when it is only a warning.
type agentInstallInput struct {
	opts       harness.InstallOptions
	agent      string
	asJSON     bool
	env        setup.SetupEnv
	cfg        *config.Store
	loaded     config.Loaded
	sourceRole bool
	cfgErr     error
}

// agentInstallInputOf parses and validates one `relevo config agents`
// invocation: flags, then the env, then the machine config, which stays a
// warning when unreadable so a bad config never stops the shipped install.
func agentInstallInputOf(args []string) (agentInstallInput, error) {
	var in agentInstallInput
	fs := flag.NewFlagSet("relevo config agents", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := agentFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return in, err
	}

	in.opts = harness.InstallOptions{
		Kind:   *v.kind,
		Role:   *v.agent,
		Force:  *v.force,
		DryRun: *v.dryRun,
		// The OpenCode plugin is opt-in: this verb is the one place that
		// writes it when it is absent.
		Files: true,
	}
	in.agent = *v.agent
	in.asJSON = *v.asJSON

	env, err := agentInstallEnv()
	if err != nil {
		return in, err
	}
	in.env = env

	cfg, cfgErr := relevo.MachineConfig()
	if cfgErr == nil {
		in.loaded, cfgErr = cfg.Load()
		if cfgErr == nil && in.agent != "" {
			in.sourceRole = relevo.IsSourceAgent(in.loaded.Agents, in.agent)
		}
	}
	in.cfg = cfg
	in.cfgErr = cfgErr
	return in, nil
}

// reportAgentInstall prints one line per install result and the document when
// asked. A failed write fails the command like any other output error.
func reportAgentInstall(w io.Writer, results []harness.InstallResult, asJSON bool) error {
	if len(results) == 0 {
		if _, err := fmt.Fprintln(w, "no harness binaries on PATH (agy, claude, opencode); nothing to install"); err != nil {
			return fmt.Errorf("write install report: %w", err)
		}
		if asJSON {
			return printDoc(configAgentsDocOf(nil))
		}
		return nil
	}

	failed := false
	for _, r := range results {
		if _, err := fmt.Fprintln(w, r.Line()); err != nil {
			return fmt.Errorf("write install report: %w", err)
		}
		if r.Outcome == harness.OutcomeError {
			failed = true
		}
	}
	if failed {
		return fail(codeInternal, "one or more agent definitions failed to install")
	}
	if asJSON {
		return printDoc(configAgentsDocOf(results))
	}
	return nil
}

func cmdAgentInstallRun(args []string) error {
	in, err := agentInstallInputOf(args)
	if err != nil {
		return err
	}
	opts := in.opts

	// A config failure is only a warning on stderr and nothing more.
	if in.cfgErr != nil {
		fmt.Fprintf(os.Stderr, "relevo: custom agents not installed: %v\n", in.cfgErr)
	}

	var results []harness.InstallResult
	if in.sourceRole {
		// A source agent goes by the custom path alone: harness.Install refuses
		// a name it does not ship.
		custom, cerr := relevo.InstallCustomAgents(in.cfg, in.env, opts)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "relevo: custom agents not installed: %v\n", cerr)
		}
		results = append(results, custom...)
	} else {
		shipped, serr := harness.Install(in.env, opts)
		if serr != nil {
			return fail(codeUsage, "%v", serr)
		}
		results = append(results, shipped...)
		if in.cfgErr == nil && in.agent == "" {
			// An --agent names exactly the one agent to install, so the custom
			// walk runs only with no agent.
			customOpts := opts
			customOpts.Role = ""
			custom, cerr := relevo.InstallCustomAgents(in.cfg, in.env, customOpts)
			if cerr != nil {
				fmt.Fprintf(os.Stderr, "relevo: custom agents not installed: %v\n", cerr)
			}
			results = append(results, custom...)
		}
	}
	if in.cfgErr == nil {
		// Every configured claude and codex account reads its own home, so the
		// definitions land there too; a failure never fails the default install.
		acct, aerr := installAccountHomes(in.env, in.loaded.Accounts, opts)
		if aerr != nil {
			fmt.Fprintf(os.Stderr, "relevo: account homes not installed: %v\n", aerr)
		}
		results = append(results, acct...)
	}

	// Human lines move to stderr under --json, where stdout carries the document
	// alone.
	return reportAgentInstall(noticeWriter(in.asJSON), results, in.asJSON)
}
