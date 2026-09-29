package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/view"
)

// configUsage is the whole `relevo config` surface: what a bare `relevo config`
// does, and every subcommand that replaced a top-level verb (§4.1).
const configUsage = `usage: relevo config [--probe [token...]]
       relevo config export
       relevo config import <file|->
       relevo config get <section>[.<key>...]
       relevo config set <section>[.<key>...] <json>
       relevo config unset <section>[.<key>...]
       relevo config edit
       relevo config log [-n N] [--rev N] [--json]
       relevo config rollback <rev> [--yes] [-m <message>]
       relevo config init [--force] [--no-agents]
       relevo config agents [--kind <agy|claude|opencode>] [--agent <name>] [--force] [--dry-run]
       relevo config server add <name> <url> [--fingerprint F] [--ca system] [--insecure]
       relevo config server rm <name>
       relevo config server list
       relevo config server key
       relevo config secret set <typesafe|client.key>
       relevo config secret rm <typesafe|client.key>
       relevo config secret list`

// cmdConfig is the one verb that replaced init, candidates, policy, roles,
// agent, client and servers (§4.1). A bare invocation, or one whose first
// argument is a flag, shows the configuration; every subcommand reuses the
// body the removed verb had.
func cmdConfig(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return configShow(args)
	}

	switch args[0] {
	case "export":
		return configExport(args[1:])
	case "import":
		return configImport(args[1:])
	case "get":
		return configGet(args[1:])
	case "set":
		return configSet(args[1:])
	case "unset":
		return configUnset(args[1:])
	case "edit":
		return configEdit(args[1:])
	case "log":
		return cmdConfigLog(args[1:])
	case "rollback":
		return cmdConfigRollback(args[1:])
	case "init":
		return cmdInit(args[1:])
	case "roles-init":
		fmt.Fprintln(os.Stderr, "relevo config roles-init is gone: roles migrate to actors on their own (relevo config log)")
		return exitCodeErr{code: 2}
	case "agents":
		return cmdAgentInstall(args[1:])
	case "server":
		return configServer(args[1:])
	case "secret":
		return configSecret(args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stderr, configUsage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "relevo config: unknown command %q\n", args[0])
		fmt.Fprintln(os.Stderr, configUsage)
		return exitCodeErr{code: 2}
	}
}

// configShow is the bare `relevo config`: the actors block, the current pick
// and the candidates block, each under a one-line heading. `--probe` runs
// exactly cmdCandidates --probe and prints nothing else.
func configShow(args []string) error {
	fs := flag.NewFlagSet("relevo config", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	probe := fs.Bool("probe", false, "run each candidate once with a one-line prompt from this machine and record its time to first output")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	if *probe {
		return cmdCandidates(args)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	// The first block is the actors section after round 2's migration; a
	// config that still has no actors (a newer schema this binary did not
	// migrate) keeps today's roles block under its own heading (R7).
	L, err := rt.Config.Load()
	if err != nil {
		return err
	}
	if len(L.Actors) > 0 {
		fmt.Println("actors")
		fmt.Print(view.FormatActors(L, rt.RoleRegistry()))
	} else {
		fmt.Println("roles")
		fmt.Print(view.FormatRoles(rt.RoleRegistry()))
	}
	fmt.Println("pick")
	fmt.Print(formatPolicy(rt))
	fmt.Println("candidates")
	fmt.Print(formatCandidates(rt))
	return nil
}
