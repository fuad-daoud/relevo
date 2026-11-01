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
	"strings"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/pick"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/setup"
	"github.com/fuad-daoud/relevo/internal/ui"
)

// version is stamped at build time with -ldflags "-X main.version=v1.2.3".
// A `go install`ed binary carries no such stamp, so buildVersion falls back to
// the module version the toolchain records in the build info.
var version = ""

// distribution is stamped by release.yml; empty in every other build, which
// release.Detect reads as "no claim".
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
  handover  give a binding to another machine: push its branch, release the tree, flip the writer [--to M]
  takeover  make this machine the writer of a binding another machine writes (--force fences the old writer)
  daemon    run the long-running reconciler (daemon stop stops the daemon this CLI started)
  service   install|uninstall the daemon's service unit (systemd user unit or launchd agent) [--force] [--json]
  setup     the first-run wizard: probe the machine, ask what is still open, then write the starter configuration,
              the agent definitions, the Claude Code plugin and the daemon service, and run one real round
              (--answer <id>=<value> repeats; --json prints the questions document, exit 3, then the result document)
  mcp       run an MCP server over stdio for a Claude Code MasterMind: status/send/wait/done/show/gate
            as tools
  push      hold this MasterMind's push claim and write each round event to stdout as NDJSON;
            "relevo push --ack <binding> <seq>" confirms one entry the holder wrote, then exits
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
  db sync   enable|disable|status|push|pull
            turn this machine's cloud sync on and off; enable names the remote
            with --url and reads the token with --token-stdin, both stored in the
            machine-local file, never in the database that leaves this machine
            (stop the daemon first)
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
// silently; errUsagePrinted is its failure twin, exiting 1 the same way.
var (
	errHelpShown    = errors.New("help shown")
	errUsagePrinted = errors.New("usage printed")
)

// activeVerb is the subcommand this invocation dispatched, read beside the
// runtime construction; empty under --json, whose stderr carries only the
// error document.
var activeVerb string

func main() {
	// Every remote request carries this as Relevo-Client-Version, so a server's
	// journal shows which client build asked. Set before any
	// command runs, and informational only: it is never signed.
	client.Version = buildVersion()

	// A muse hook or plugin MCP server runs with a scrubbed environment: take
	// the XDG roots from the muse host before any path is resolved from them.
	applyMuseHostEnv()

	args := os.Args[1:]
	err := run(args)
	recordInternal(args, err)
	code := report(os.Stderr, err, jsonRequested(args))
	setup.FlushHint()
	os.Exit(code)
}

// report renders run's error on w and returns the process exit code: a coded
// failure prints one human line plus its next command, or the JSON envelope
// under --json; an exitCodeErr exits with its own code and prints nothing.
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
		// Usage is a refusal, so a bare `relevo` on a pipe exits 2
		// exactly like every other usage failure; the text is already on stderr.
		return catalog[codeUsage].exit
	}

	fmt.Fprintf(w, "relevo: %v\n", err)
	return 1
}

// jsonRequested reports whether --json is one of args. The scan is an exact
// token match: this CLI has no `--` passthrough, so a standalone --json
// anywhere is the caller asking for the machine shape.
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
		// A bare `relevo` on a terminal is `relevo ui`; it then falls through
		// every guard below exactly as `ui` does.
		if uiArgs := bareArgs(isTerminal(os.Stdin), isTerminal(os.Stdout)); uiArgs != nil {
			args = uiArgs
		} else {
			fmt.Fprint(os.Stderr, usage)
			return errUsagePrinted
		}
	}

	// Every verb captures the calling agy session's credentials, since
	// whichever runs after an agy restart refreshes them. The route is
	// installed first: the peek verbs must not capture, which opens and can
	// migrate the database.
	if peek := installRouteForArgs(args); !peek {
		captureAgyEnv()
	}

	// Inside an env sandbox, name the sandbox above the command's output: the
	// exports are invisible in a transcript, so this is the only line that says
	// which root the write landed in.
	if name := sandboxBannerVerb(args); name != "" {
		fmt.Fprintf(os.Stderr, "relevo: sandbox %s\n", name)
	}

	activeVerb = args[0]
	if jsonRequested(args) {
		// A --json caller reads stderr as the error document alone.
		activeVerb = ""
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
	case "handover":
		return cmdHandover(args[1:])
	case "takeover":
		return cmdTakeover(args[1:])
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
	case "service":
		return cmdService(args[1:])
	case "setup":
		return cmdSetup(args[1:])
	case "mcp":
		return cmdMCP(args[1:])
	case "push":
		return cmdPush(args[1:])
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
	case "sync-worker":
		return cmdSyncWorker(args[1:])
	default:
		// The verbs folded into `relevo config`, and the verbs merged
		// into bind/show/unbind/status, name their replacement rather than the
		// generic unknown-subcommand error.
		if replacement, ok := removedVerbs[args[0]]; ok {
			return failNext(codeUsage, replacement, "%q was removed; use %s", args[0], replacement)
		}
		return fail(codeUsage, "unknown subcommand %q; run \"relevo help\" for the command list", args[0])
	}
}

// removedVerbs maps each entry below to its replacement: the seven folded
// into `relevo config`, the seven merged into bind, show, unbind and status,
// the verbs folded into wait and gate, and the three folded into history and
// doctor.
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
	"tab":         "relevo ui :rounds",
	"stats":       "relevo ui :stats",

	// The old spelling exits 2 through this map like every other entry here.
	"planner": "relevo mastermind",
}

// userConfigRoot resolves $XDG_CONFIG_HOME, falling back to ~/.config. The
// rule lives in internal/config so the importer, the file applier and this
// command cannot disagree about where the config root is.
func userConfigRoot() (string, error) { return config.UserRoot() }

type uiFlagValues struct {
	interval *time.Duration
}

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
	// built, so nothing touches the state directory.
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
	// database does.
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
	// succeeded; a failed open leaves them disabled, as an empty PrefsPath
	// does.
	var prefsKV db.KV
	if rt.DB != nil {
		prefsKV = rt.DB.LocalOrSelf()
	}

	hintFreshen(false, nil)
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

// uiStart maps `relevo ui`'s positional args to the shell's start command:
// empty starts at :fleet, otherwise the first arg names a view after ':'. It
// is pure, so a cmd test covers it without a terminal.
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

// runPick opens the interactive picker for one verb. The three non-zero
// outcomes already showed on screen, so they exit 1 silently; anything else
// is a startup failure and prints.
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

// pickNamesNothing is the rule shared by the three verbs: --pick and a
// binding name are mutually exclusive.
func pickNamesNothing(nameFlag string, positional []string) error {
	if nameFlag != "" || len(positional) > 0 {
		return fail(codeRefused, "--pick chooses the binding; do not also name one")
	}
	return nil
}
