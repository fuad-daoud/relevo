package main

import (
	"flag"
	"fmt"
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
		fmt.Fprintln(fs.Output(), "usage: relevo config agents [--kind <agy|claude|opencode>] [--agent <name>] [--force] [--dry-run]")
		fmt.Fprintln(fs.Output(), "Installs relevo's shipped agents and the custom agents in your config.")
		fmt.Fprintln(fs.Output(), "For opencode it also installs the relevo OpenCode plugin (~/.config/opencode/plugins/relevo).")
	}
	return v
}

func cmdAgentInstall(args []string) error {
	return outcomeError(cmdAgentInstallRun(args))
}

func cmdAgentInstallRun(args []string) error {
	fs := flag.NewFlagSet("relevo config agents", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := agentFlagSet(fs)
	kind, agent, force, dryRun, asJSON := v.kind, v.agent, v.force, v.dryRun, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	opts := harness.InstallOptions{
		Kind:   *kind,
		Role:   *agent,
		Force:  *force,
		DryRun: *dryRun,
		// The OpenCode plugin is opt-in: this verb is the one place that
		// writes it when it is absent.
		Files: true,
	}

	env, err := agentInstallEnv()
	if err != nil {
		return err
	}

	// The custom agents live in the machine config, which may be unreadable; a
	// config failure is one warning on stderr and nothing more.
	cfg, cfgErr := relevo.MachineConfig()
	var loaded config.Loaded
	sourceRole := false
	if cfgErr == nil {
		loaded, cfgErr = cfg.Load()
		if cfgErr == nil && *agent != "" {
			sourceRole = relevo.IsSourceAgent(loaded.Agents, *agent)
		}
	}
	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "relevo: custom agents not installed: %v\n", cfgErr)
	}

	var results []harness.InstallResult
	if sourceRole {
		// Harness.Install refuses a name it does not ship, so a source agent goes
		// by the custom path alone.
		custom, cerr := relevo.InstallCustomAgents(cfg, env, opts)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "relevo: custom agents not installed: %v\n", cerr)
		}
		results = append(results, custom...)
	} else {
		shipped, serr := harness.Install(env, opts)
		if serr != nil {
			return fail(codeUsage, "%v", serr)
		}
		results = append(results, shipped...)
		if cfgErr == nil && *agent == "" {
			// An --agent names exactly the one agent to install, so the custom
			// walk runs only with no agent.
			customOpts := opts
			customOpts.Role = ""
			custom, cerr := relevo.InstallCustomAgents(cfg, env, customOpts)
			if cerr != nil {
				fmt.Fprintf(os.Stderr, "relevo: custom agents not installed: %v\n", cerr)
			}
			results = append(results, custom...)
		}
	}
	if cfgErr == nil {
		// Every configured claude and codex account reads its own home, so the
		// definitions land there too; a failure never fails the default install.
		acct, aerr := installAccountHomes(env, loaded.Accounts, opts)
		if aerr != nil {
			fmt.Fprintf(os.Stderr, "relevo: account homes not installed: %v\n", aerr)
		}
		results = append(results, acct...)
	}

	// Human lines move to stderr under --json, where stdout carries the document
	// alone.
	w := noticeWriter(*asJSON)

	if len(results) == 0 {
		fmt.Fprintln(w, "no harness binaries on PATH (agy, claude, opencode); nothing to install")
		if *asJSON {
			return printDoc(configAgentsDocOf(nil))
		}
		return nil
	}

	failed := false
	for _, r := range results {
		fmt.Fprintln(w, r.Line())
		if r.Outcome == harness.OutcomeError {
			failed = true
		}
	}
	if failed {
		return fail(codeInternal, "one or more agent definitions failed to install")
	}
	if *asJSON {
		return printDoc(configAgentsDocOf(results))
	}
	return nil
}
