package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// agentInstallEnv is the InstallEnv `relevo config agents` uses -- and the
// same one the daemon's once-per-image role refresh uses (#371 §4.10). It
// lives in internal/relevo from round 5 on, so the cockpit's `:agents` view
// can build the same env; see relevo.AgentInstallEnv.
func agentInstallEnv() (harness.InstallEnv, error) {
	return relevo.AgentInstallEnv()
}

// agentFlagValues holds the pointers `relevo config agents`' flags parse into.
// agentFlagSet defines them on fs; cmdAgentInstall and TestRemovedFlagsAreUnknown
// read the same surface (A4-1a).
type agentFlagValues struct {
	kind   *string
	agent  *string
	force  *bool
	dryRun *bool
}

// agentFlagSet defines `relevo config agents`' flags on fs and returns the
// values they parse into, so a test can inspect the flag surface without
// installing anything.
func agentFlagSet(fs *flag.FlagSet) *agentFlagValues {
	v := &agentFlagValues{}
	v.kind = fs.String("kind", "", "harness kind")
	v.agent = fs.String("agent", "", "agent name")
	v.force = fs.Bool("force", false, "force overwrite")
	v.dryRun = fs.Bool("dry-run", false, "dry run")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: relevo config agents [--kind <agy|claude|opencode>] [--agent <name>] [--force] [--dry-run]")
		fmt.Fprintln(fs.Output(), "Installs relevo's shipped agents and the custom agents in your config.")
		fmt.Fprintln(fs.Output(), "For opencode it also installs the relevo OpenCode plugin (~/.config/opencode/plugins/relevo).")
	}
	return v
}

// cmdAgentInstall installs embedded agent definitions, the body
// `relevo config agents` had (now `relevo config agents`).
func cmdAgentInstall(args []string) error {
	fs := flag.NewFlagSet("relevo config agents", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := agentFlagSet(fs)
	kind, agent, force, dryRun := v.kind, v.agent, v.force, v.dryRun
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	opts := harness.InstallOptions{
		Kind:   *kind,
		Role:   *agent,
		Force:  *force,
		DryRun: *dryRun,
		// The OpenCode plugin is opt-in: this verb is the one place that
		// writes it when it is absent (#393 §5.4).
		Files: true,
	}

	env, err := agentInstallEnv()
	if err != nil {
		return err
	}

	// The custom agents live in the machine config, which may be unreadable;
	// that must not stop the shipped install, so a config failure is one
	// warning on stderr and nothing more.
	cfg, cfgErr := relevo.MachineConfig()
	sourceRole := false
	if cfgErr == nil && *agent != "" {
		loaded, lerr := cfg.Load()
		if lerr != nil {
			cfgErr = lerr
		} else {
			sourceRole = relevo.IsSourceAgent(loaded.Agents, *agent)
		}
	}
	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "relevo: custom agents not installed: %v\n", cfgErr)
	}

	var results []harness.InstallResult
	if sourceRole {
		// harness.Install refuses a name it does not ship, so a source agent
		// is installed by the custom path alone.
		custom, cerr := relevo.InstallCustomAgents(cfg, env, opts)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "relevo: custom agents not installed: %v\n", cerr)
		}
		results = append(results, custom...)
	} else {
		shipped, serr := harness.Install(env, opts)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "relevo: %v\n", serr)
			return exitCodeErr{code: 2}
		}
		results = append(results, shipped...)
		if cfgErr == nil && *agent == "" {
			// An --agent names exactly the one agent to install, so the custom
			// walk runs only with no agent: it then installs every custom
			// agent the kind and force flags select.
			customOpts := opts
			customOpts.Role = ""
			custom, cerr := relevo.InstallCustomAgents(cfg, env, customOpts)
			if cerr != nil {
				fmt.Fprintf(os.Stderr, "relevo: custom agents not installed: %v\n", cerr)
			}
			results = append(results, custom...)
		}
	}

	if len(results) == 0 {
		fmt.Println("no harness binaries on PATH (agy, claude, opencode); nothing to install")
		return nil
	}

	failed := false
	for _, r := range results {
		fmt.Println(r.Line())
		if r.Outcome == harness.OutcomeError {
			failed = true
		}
	}
	if failed {
		return exitCodeErr{code: 1}
	}
	return nil
}
