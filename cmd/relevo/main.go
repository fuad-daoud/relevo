// Command relevo automates plan and report handoff between a mastermind agent and
// a headless builder process.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/pick"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/ui"
)

// version is stamped at build time with -ldflags "-X main.version=v1.2.3".
// A `go install`ed binary carries no such stamp, so buildVersion falls back to
// the module version the toolchain records in the build info.
var version = ""

// distribution is stamped by release.yml with
// -X main.distribution=release. No Makefile target and no `go install` sets
// it, so it is empty in every other build; release.Detect reads the zero
// value as "no claim".
var distribution = ""

const usage = `relevo automates the plan/report handoff between two AI coding agent
processes: a MasterMind hands work to a runner, and relevo moves
the files between them.

Usage:
  relevo <command> [flags]

Commands:
  bind      bind this MasterMind pane to a runner over the current working tree [--tier]
              (--role is now --actor)
              --worktree | --cwd DIR | --branch B | --server S
                        attach another runner to this MasterMind, on its own worktree or tree
  send      stage a plan file as the current round and start the runner [--tier] [--dry-run] [--verify|--no-verify]
  status    one row per binding: round, state, live pane status, what is pending [--all] [--line]
  history   round history as JSON [--here] [--binding B] [--mastermind P] [--since D] [--limit N] [-q QUERY] [--json]
  show      one round's plan, report, diff, drift, gate, findings, log or transcript, live or archived [--round N] [--diff [--stat|--anchors]] [--log [--follow --after N]] [--json]
  wait      block until a round closes or needs you, then print the pending report; exit 0 closed, 2 unmarked, 5 halted/blocked per report, 6 not started, 3 needs you, 4 done/unbound, 124 timeout [--peek]
  ui [:view [args]]  the cockpit: :fleet, :rounds [query], :round <binding> [N]
  done      mark a binding done; relaying stops (--pick to choose it on screen)
  stop      kill the runner process and close its round without a report unless one is already on disk
  unbind    forget a binding, deleting or archiving its directory (--pick to choose it on screen)
              --done clears every binding the MasterMind marked DONE [--delete] [--dry-run]
  daemon    run the long-running reconciler (daemon stop stops the daemon this CLI started)
  mcp       run an MCP server over stdio for a Claude Code MasterMind pane: status/send/show/gate/done
            as tools; in channel mode (auto-detected, or --mode channel) also pushes reports and
            NEEDS YOU into the session instead of typing them into its pane
  doctor    preflight check: plugin, daemon, harness binaries, roles
  bugreport assemble a local, redacted bug-report bundle and print the gh line
  board     open a local Excalidraw whiteboard: this MasterMind's live board, or a repo scene
              relevo board [path] [--board NAME] [--mastermind M] [--theme NAME] [--no-open]
              relevo board url [--board NAME] [--mastermind M]   print a live board's URL
              relevo board comments [path|--board NAME] [--json]   list the scene's comments
              relevo board comment [path|--board NAME] --text S [--x X --y Y] [--by B]   append one comment
              relevo board text <file> [--json]   list a scene's text elements
              relevo board annotate <file> --text S [--x X --y Y]   append one text element
  db        query '<SQL>' [--json]
            read relevo.db with one read-only SQL statement, through the daemon
            when it runs; use it instead of sqlite3, which the daemon's lock
            keeps out
  chain     start a chain: build, review and correct across an ordered list of plans
              --plan F (repeatable) --feature L | --no-feature [--security[=false]] [--base R]
  update    replace this release binary with the latest release, checksum-verified [--check] [--to vX.Y.Z] [--release]
  config    show the actors, the current pick and the candidates
  config edit|get|set|unset|export|import
            read and change the configuration document
  config init|agents
            write starter configuration or agent definitions
  config server add|rm|list|key
            this machine's remote-builder identity and server list
  config secret set|rm|list
            store or forget the typesafe and client.key secrets
  mastermind  register this MasterMind (or re-attach an existing one), and list, rename or forget records
  gate      list this machine's active gates; gate a provider: relevo gate <token> [--for D] [--reason S];
            clear one: relevo gate --clear <provider|token>; relevo gate --serve [--state DIR] acts on the
            local serve daemon's gates instead

  serve                     run the remote-builder server (listener + daemon)
  serve init|enroll|clients|revoke|fingerprint|status|gc|unbind|ui
                            server administration, on the server host

  help      print this message
  version   print the relevo version

Run "relevo <command> -h" for that command's flags.

relevo drives the harness binaries it launches, which must be on PATH
State lives in $XDG_STATE_HOME/relevo (default ~/.local/state/relevo).
`

type exitCodeErr struct {
	code int
}

func (e exitCodeErr) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

// errHelpShown reports that help was printed on request, so main exits 0
// without adding an error line. errUsagePrinted is its failure twin: usage is
// already on stderr and main should exit 1 silently.
var (
	errHelpShown    = errors.New("help shown")
	errUsagePrinted = errors.New("usage printed")
)

func main() {
	// Every remote request carries this as Relevo-Client-Version, so a server's
	// journal shows which client build asked (#373 §4.4). Set before any
	// command runs, and informational only: it is never signed.
	client.Version = buildVersion()

	args := os.Args[1:]
	err := run(args)
	recordInternal(args, err)
	os.Exit(report(os.Stderr, err, jsonRequested(args)))
}

// report renders run's error on w and returns the process exit code. It is the
// one renderer every failure funnels through: a nil error, or help already
// printed on request, is silent; a coded failure prints one human line plus
// its next command, or the JSON envelope when the caller asked for --json; an
// exitCodeErr exits with its own code and prints nothing; anything else keeps
// the prose line the CLI has always printed.
func report(w io.Writer, err error, jsonMode bool) int {
	switch {
	case err == nil, errors.Is(err, errHelpShown):
		return 0
	}

	var ce *cliError
	if errors.As(err, &ce) {
		if jsonMode {
			_ = json.NewEncoder(w).Encode(errorEnvelope{Error: errorDocument{
				Code:    ce.code,
				Message: ce.message,
				Next:    ce.next,
			}})
		} else {
			fmt.Fprintf(w, "relevo: %s: %s\n", ce.code, ce.message)
			if ce.next != "" {
				fmt.Fprintf(w, "  next: %s\n", ce.next)
			}
		}
		return catalogExit(ce.code)
	}

	var ec exitCodeErr
	if errors.As(err, &ec) {
		return ec.code
	}
	if errors.Is(err, errUsagePrinted) {
		// Usage is a refusal (spec §2.6), so a bare `relevo` on a pipe exits 2
		// exactly like every other usage failure; the text is already on stderr.
		return catalog[codeUsage].exit
	}

	fmt.Fprintf(w, "relevo: %v\n", err)
	return 1
}

// jsonRequested reports whether --json is one of args. The scan is an exact
// token match, not a prefix one: this CLI has no `--` passthrough, so a
// standalone --json anywhere in the arguments is the caller asking for the
// machine shape, while a value that merely starts with the word is not.
func jsonRequested(args []string) bool {
	for _, a := range args {
		if a == "--json" {
			return true
		}
	}
	return false
}

func run(args []string) error {
	if len(args) == 0 {
		// A bare `relevo` on a terminal is `relevo ui` (round 3, step 3.1);
		// it then falls through every guard below exactly as `ui` does.
		if uiArgs := bareArgs(isTerminal(os.Stdin), isTerminal(os.Stdout)); uiArgs != nil {
			args = uiArgs
		} else {
			fmt.Fprint(os.Stderr, usage)
			return errUsagePrinted
		}
	}

	// Every verb captures the calling agy session's agentapi credentials
	// (#349): an agy mastermind runs relevo constantly (send, wait, pull,
	// status), and whichever verb it happens to run after an agy restart is
	// the one that refreshes the session's agentapi credentials.
	//
	// The route is installed first, from the same command line: the peek verbs
	// are read-only and must not capture (which opens and can migrate the
	// database), and every other verb opens the machine database through the
	// route once it is set.
	if peek := installRouteForArgs(args); !peek {
		captureAgyEnv()
	}

	switch args[0] {
	case "help", "-h", "--help":
		return cmdHelp(args[1:])
	case "version", "-v", "--version":
		return cmdVersion(args[1:])
	case "bind":
		return cmdBind(args[1:])
	case "unbind":
		return cmdUnbind(args[1:])
	case "send":
		return cmdSend(args[1:])
	case "ask":
		return failNext(codeUsage, "relevo bind --actor reviewer",
			"relevo ask is gone: bind a reader actor (relevo bind --actor reviewer) and send it a plan")
	case "status":
		return cmdStatus(args[1:])
	case "history":
		return cmdHistory(args[1:])
	case "show":
		return cmdShow(args[1:])
	case "wait":
		return cmdWait(args[1:])
	case "ui":
		return cmdUI(args[1:])
	case "done":
		return cmdDone(args[1:])
	case "stop":
		return cmdStop(args[1:])
	case "daemon":
		return cmdDaemon(args[1:])
	case "mcp":
		return cmdMCP(args[1:])
	case "doctor":
		return cmdDoctor(args[1:])
	case "bugreport":
		return cmdBugreport(args[1:])
	case "board":
		return cmdBoard(args[1:])
	case "db":
		return cmdDB(args[1:])
	case "chain":
		return cmdChain(args[1:])
	case "update":
		return cmdUpdate(args[1:])
	case "config":
		return cmdConfig(args[1:])
	case "mastermind":
		return cmdMasterMind(args[1:])
	case "gate":
		return cmdGate(args[1:])
	case "serve":
		return cmdServe(args[1:])
	default:
		// The verbs P2b folded into `relevo config`, and the verbs P4a merged
		// into bind/show/unbind/status, name their replacement rather than the
		// generic unknown-subcommand error (§4.4, §4.6).
		if replacement, ok := removedVerbs[args[0]]; ok {
			return failNext(codeUsage, replacement, "%q was removed; use %s", args[0], replacement)
		}
		return fail(codeUsage, "unknown subcommand %q; run \"relevo help\" for the command list", args[0])
	}
}

// removedVerbs names each removed verb and the form that replaces it: the
// seven P2b folded into `relevo config` (§4.4), the seven P4a merged into
// bind, show, unbind and status (§4.6), the P4a round 2 verbs folded into
// wait and gate (§4.1, §4.3), and the three P3d folded into history and
// doctor (P3d §4.6).
var removedVerbs = map[string]string{
	"init":       "relevo config init",
	"candidates": "relevo config",
	"policy":     "relevo config",
	"roles":      "relevo config",
	"agent":      "relevo config agents",
	"client":     "relevo config server",
	"servers":    "relevo config server list",

	"add":        "relevo bind --worktree",
	"diff":       "relevo show --diff",
	"log":        "relevo show --log",
	"gc":         "relevo unbind --done",
	"pause":      "relevo done, then relevo bind --resume",
	"statusline": "relevo status --line",

	"pull":        "relevo wait (it prints the report)",
	"unavailable": "relevo gate <token>",
	"available":   "relevo gate --clear <provider>",
	"tab":         "relevo history --tab",
	"stats":       "relevo history --stats",

	// The rename (D5): the verb is `relevo mastermind` now, and the old
	// spelling exits 2 through this map exactly like every other removed verb.
	"planner": "relevo mastermind",
}

// userConfigRoot resolves $XDG_CONFIG_HOME, falling back to ~/.config.
func userConfigRoot() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}

	return filepath.Join(home, ".config"), nil
}

// uiFlagValues holds the pointer the cockpit parses into.
type uiFlagValues struct {
	interval *time.Duration
}

// uiFlagSet defines that flag on fs and returns what it parses into.
func uiFlagSet(fs *flag.FlagSet) *uiFlagValues {
	v := &uiFlagValues{}
	v.interval = fs.Duration("interval", 0, "refresh interval")
	return v
}

func cmdUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	v := uiFlagSet(fs)
	interval := v.interval
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// A positional `:view` is refused with exit 2 before any runtime is
	// built, so nothing touches the state directory (round 3, step 3.2).
	start, err := uiStart(fs.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	// An open failure never blocks the ui from starting -- it runs in
	// live scope, with a sticky notice, exactly as pressing "a" with no
	// database does (docs/specs/2026-09-20-persistence-design.md §6).
	var notice string
	if d, dbErr := openDB(rt.Store.DBPath()); dbErr != nil {
		notice = fmt.Sprintf("no database: %v", dbErr)
	} else {
		rt.DB = d
		defer d.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Preferences live in the machine database rt.DB holds once openDB
	// succeeded; a failed open leaves them disabled, as an empty PrefsPath did
	// (P3b plan §4.4).
	var prefsKV db.KV
	if rt.DB != nil {
		prefsKV = rt.DB
	}

	return ui.Run(ctx, rt, ui.Options{
		Interval: *interval,
		Prefs: ui.PrefsStore{
			KV:  prefsKV,
			Key: "ui",
		},
		Notice:    notice,
		Start:     start,
		Version:   buildVersion(),
		ProbeExec: lineExec{},
	})
}

// uiStart maps `relevo ui`'s positional args to the shell's start command
// (round 3, step 3.2): an empty list starts at :fleet; otherwise the first
// arg names a view after ':' and every arg is joined into its command line.
// Pure, so a cmd test covers it without running the ui, which needs a
// terminal.
func uiStart(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if !strings.HasPrefix(args[0], ":") {
		return "", fmt.Errorf("relevo ui: want :<view> (e.g. relevo ui :rounds), got %q", args[0])
	}
	parts := append([]string{args[0][1:]}, args[1:]...)
	return strings.Join(parts, " "), nil
}

// runPick opens the interactive picker for one verb (#15). The three
// non-zero outcomes have already been shown on screen, so they exit 1
// silently: a second "relevo: ..." line would go to the plugin log, not to
// the human (spec §3). Anything else is a startup failure and prints.
func runPick(opts pick.Options) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = pick.Run(ctx, rt, opts)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pick.ErrCancelled), errors.Is(err, pick.ErrNothingToPick), errors.Is(err, pick.ErrVerbFailed):
		return exitCodeErr{code: 1}
	default:
		return err
	}
}

// pickNamesNothing is the spec §3 rule shared by the three verbs: --pick and
// a binding name are mutually exclusive.
func pickNamesNothing(nameFlag string, positional []string) error {
	if nameFlag != "" || len(positional) > 0 {
		return fail(codeRefused, "--pick chooses the binding; do not also name one")
	}
	return nil
}
