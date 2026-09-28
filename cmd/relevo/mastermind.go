package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/fuad-daoud/relevo/internal/chatlabel"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// mastermindPriorIDTimeout bounds how long `relevo mastermind init` waits for the
// database to open before giving up on reusing a row's id (§4.4 step 3). A
// SessionStart hook must never block a session on sqlite.
const mastermindPriorIDTimeout = 2 * time.Second

// cmdMasterMind dispatches `relevo mastermind init|enable|disable|guide|list|rename|forget`
// (#303 §4.7, #632). It touches no harness:
// a mastermind record is relevo's own identity, not a pane.
func cmdMasterMind(args []string) error {
	const usage = `usage: relevo mastermind init [--name N] [--kind K --session S] [--hook claude]
       relevo mastermind enable [--repo] [--kind K --session S]
       relevo mastermind disable [--repo]
       relevo mastermind reset
       relevo mastermind guide [--cwd DIR] [--kind K --session S] [--json]
       relevo mastermind list [--json]
       relevo mastermind rename <id|name> <new-name>
       relevo mastermind forget <id|name>`

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	switch args[0] {
	case "init":
		return cmdMasterMindInit(args[1:])
	case "enable":
		return cmdMasterMindEnable(args[1:])
	case "disable":
		return cmdMasterMindDisable(args[1:])
	case "reset":
		return cmdMasterMindReset(args[1:])
	case "guide":
		return cmdMasterMindGuide(args[1:])
	case "list":
		return cmdMasterMindList(args[1:])
	case "rename":
		return cmdMasterMindRename(args[1:])
	case "forget":
		return cmdMasterMindForget(args[1:])
	case "prune":
		// §4.4: pruning is automatic now; the verb names that and exits 2
		// like every other removed spelling.
		fmt.Fprintf(os.Stderr, "relevo: %q was removed; the daemon prunes dead MasterMinds hourly\n", "prune")
		return exitCodeErr{code: 2}
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "relevo mastermind: unknown subcommand %q\n", args[0])
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
}

// mastermindRegistry is the CLI's registry: the state root's database kv rows
// `<root>/relevo.db` (P3b round 2 §4.1) on the runtime's clock, importing the
// pre-database masterminds directory the first time a record is touched. A store
// whose database cannot be opened is fatal for the verb.
func mastermindRegistry(rt relevo.Runtime) (*mastermind.DBRegistry, error) {
	d, err := rt.Store.DB()
	if err != nil {
		return nil, err
	}
	return &mastermind.DBRegistry{KV: db.TxKV{DB: d}, Root: rt.Store.MasterMindsDir(), Now: rt.Now}, nil
}

func cmdMasterMindInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	name := fs.String("name", "", "mastermind name (default: <agent>-<n>, else <kind>-<n>)")
	kind := fs.String("kind", "", "harness kind for an explicit registration (e.g. opencode)")
	session := fs.String("session", "", "harness session id for an explicit registration")
	hook := fs.String("hook", "", "read a Claude Code SessionStart payload from stdin (only \"claude\")")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *hook != "" {
		if *hook != "claude" {
			fmt.Fprintf(os.Stderr, "relevo: relevo mastermind init --hook supports only \"claude\", got %q\n", *hook)
			return exitCodeErr{code: 2}
		}
		if *kind != "" || *session != "" {
			fmt.Fprintln(os.Stderr, "relevo: relevo mastermind init --hook reads its kind and session from the hook payload, not --kind/--session")
			return exitCodeErr{code: 2}
		}
		return mastermindInitHook(*name)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	in := mastermind.InitInput{Name: *name, CWD: cwd, Now: rt.Now()}

	caller, err := mastermindCaller(rt, cwd, *kind, *session)
	if err != nil {
		return err
	}
	in.Kind, in.SessionID = caller.Kind, caller.SessionID
	in.Agent, in.HostPID, in.HostStartedAt = caller.Agent, caller.HostPID, caller.HostStartedAt

	d, closeDB := mastermindOpenTimed(rt.Store.DBPath())
	defer closeDB()
	in.PriorID = mastermindPriorIDFunc(d)

	reg, err := mastermindRegistry(rt)
	if err != nil {
		return err
	}
	rec, res, err := mastermind.Init(reg, in)
	if err != nil {
		return err
	}

	fmt.Printf("MasterMind %s (%s) %s\n", rec.Name, rec.ID, res)
	fmt.Printf("export RELEVO_MASTERMIND=%s\n", rec.ID)
	return nil
}

// mastermindInitHook runs `relevo mastermind init --hook claude` (§5.1).
//
// It never returns an error and never exits non-zero: the hook runs at the
// start of a Claude Code session, and blocking that session because relevo could
// not read its own state would be a far worse failure than an unregistered
// mastermind (§4.4, §6.1). Every failure prints HookNote on stdout -- where the
// model reads its context -- and the error on stderr for the human.
func mastermindInitHook(nameFlag string) error {
	in, err := mastermind.ParseHookInput(os.Stdin)
	if err != nil {
		return mastermindInitHookFailure(err)
	}

	rt, err := newRuntime()
	if err != nil {
		return mastermindInitHookFailure(err)
	}

	// The repo's answer gates the hook before anything is written. A cwd the
	// hook cannot resolve to a repository is left alone: there is nothing to
	// remember, so there is nothing to ask.
	ref := mastermindRepoOf(context.Background(), rt, in.CWD)
	if !mastermindRepoKnown(ref) {
		_, _ = os.Stdout.Write(mastermind.HookConsent(""))
		return nil
	}

	// One timed open serves both the prior id and the consent read, so the
	// hook never waits on sqlite. A read that did not arrive reads as unset --
	// the ask, never a silent registration.
	d, closeDB := mastermindOpenTimed(rt.Store.DBPath())
	defer closeDB()
	consent := mastermind.ConsentUnset
	if d != nil {
		if c, err := d.RepoConsent(ref); err == nil {
			consent = c
		}
	}
	if consent != mastermind.ConsentYes {
		_, _ = os.Stdout.Write(mastermind.HookConsent(mastermind.ConsentText(consent, nil)))
		return nil
	}

	// The hook's parent is the Claude Code process, which is the same pid
	// CLAUDE_PID names in a Bash tool and `relevo mcp`'s parent (§1.1).
	host := os.Getppid()

	reg, err := mastermindRegistry(rt)
	if err != nil {
		return mastermindInitHookFailure(err)
	}
	rec, _, err := mastermind.Init(reg, mastermind.InitInput{
		Kind:           "claude",
		SessionID:      in.SessionID,
		TranscriptPath: in.TranscriptPath,
		CWD:            in.CWD,
		Name:           nameFlag,
		Agent:          os.Getenv("CLAUDE_CODE_AGENT"),
		HostPID:        host,
		HostStartedAt:  mastermindHostStart(host),
		Now:            rt.Now(),
		PriorID:        mastermindPriorIDFunc(d),
	})
	if err != nil {
		return mastermindInitHookFailure(err)
	}

	// The export line is how every later Bash call in the session learns its
	// mastermind (§3.4). Appending, never truncating: Claude Code reads the whole
	// file, and it may already carry lines from other tools.
	//
	// With no $CLAUDE_ENV_FILE there is nowhere to export to, so the answer
	// says so in additionalContext instead of pretending the export happened
	// (§3.4): relevo then resolves the session through the host process.
	out := mastermind.HookOutput(rec)
	if envFile := os.Getenv("CLAUDE_ENV_FILE"); envFile != "" {
		if err := appendEnvLine(envFile, mastermind.EnvLine(rec.ID)); err != nil {
			return mastermindInitHookFailure(err)
		}
	} else {
		out = mastermind.HookOutputNoEnv(rec)
	}

	_, _ = os.Stdout.Write(out)
	return nil
}

// mastermindInitHookFailure reports a hook failure and swallows it: the note goes
// to stdout as the hook's answer, the error to stderr for the human, and the
// command still succeeds.
func mastermindInitHookFailure(err error) error {
	fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
	_, _ = os.Stdout.Write(mastermind.HookNote(fmt.Sprintf("relevo mastermind init failed: %v", err)))
	return nil
}

// mastermindHostStart reads a host process's start time in Unix seconds, the
// pid-reuse defence §3.1 gives host_started_at. A failure reports 0 rather than
// failing the caller: the record is still useful, and §4.3's session step
// resolves the mastermind when the host cannot be matched.
func mastermindHostStart(pid int) int64 {
	if pid <= 0 {
		return 0
	}
	started, err := proc.StartTime(context.Background(), pid)
	if err != nil {
		return 0
	}
	return started.Unix()
}

// mastermindOpenTimed opens relevo.db on a goroutine and gives up after
// mastermindPriorIDTimeout, so a hook never waits on sqlite. The caller must
// call the returned close function; a nil *db.DB means the open did not finish
// in time, and the safe answers (no prior id, unset consent) follow from it.
func appendEnvLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("append %s: %w", path, err)
	}
	defer f.Close()

	if _, err := io.WriteString(f, line); err != nil {
		return fmt.Errorf("append %s: %w", path, err)
	}
	return nil
}

// cmdMasterMindEnable answers yes for this session, and with --repo for the
// repository, then registers the calling session so the answer takes effect
// without a restart (#632).
func cmdMasterMindList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the records as a JSON array")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	// DBRegistry.List sorts by name, which is the order `relevo mastermind list`
	// documents (§4.7).
	reg, err := mastermindRegistry(rt)
	if err != nil {
		return err
	}
	records, err := reg.List()
	if err != nil {
		return err
	}

	// The count of non-DONE bindings naming each mastermind, from the same store
	// walk forget's guard uses. Both it and the state column are derived here,
	// not stored on the record: the file is identity, and this is the
	// machine's present answer about it.
	counts, err := mastermindBindingCounts(rt)
	if err != nil {
		return err
	}

	// The chat column (#386): what the harness itself calls each session, read
	// from the harness's own files now and never stored on the record.
	res := chatResolver()
	labels := make([]chatlabel.Label, len(records))
	for i, rec := range records {
		labels[i] = res.Resolve(context.Background(), rec.HarnessKind, rec.SessionID, rec.TranscriptLocator)
	}

	if *asJSON {
		views := make([]mastermindListView, 0, len(records))
		for i, rec := range records {
			views = append(views, mastermindListView{
				Record:    rec,
				State:     string(mastermind.RecordState(rec, rt.ProcStart)),
				Bindings:  counts[rec.ID],
				ChatLabel: labels[i].Text,
				ChatLink:  labels[i].Link,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(views)
	}

	// tabwriter, not fixed widths: a 36-character session id or a long cwd
	// used to overflow its cell and shift every column after it.
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "name\tchat\tid\tkind\tsession\thost pid\tstate\tbindings\tcwd\tseen")
	for i, rec := range records {
		host := "-"
		if rec.HostPID > 0 {
			host = strconv.Itoa(rec.HostPID)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			rec.Name, labels[i].String(), rec.ID, rec.HarnessKind, rec.SessionID, host,
			mastermind.RecordState(rec, rt.ProcStart), counts[rec.ID], rec.CWD,
			rec.SeenAt.UTC().Format(time.RFC3339))
	}
	return w.Flush()
}

// mastermindListView is one record as `relevo mastermind list --json` renders it: the
// record's own fields, flattened by the embedded struct, plus the two derived
// columns the table shows.
type mastermindListView struct {
	mastermind.Record
	State    string `json:"state"`
	Bindings int    `json:"bindings"`
	// ChatLabel and ChatLink are the harness's own name for the session
	// (#386), read when the command runs and never stored.
	ChatLabel string `json:"chat_label,omitempty"`
	ChatLink  string `json:"chat_link,omitempty"`
}

// chatResolver is the label reader `relevo mastermind list` uses (#386): a claude
// transcript is read directly, and an opencode session title is read through
// the sqlite3 shell-out, which is wired only when sqlite3 is on PATH.
func chatResolver() chatlabel.Resolver {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return chatlabel.Resolver{}
	}
	return chatlabel.Resolver{Exec: binExec{}, OpencodeDB: opencodeDBPath()}
}

// annotateMasterMindChat fills each binding row's MasterMindChatLabel and
// MasterMindChatLink (#386) from the mastermind record the row names. It runs only
// from cmd/relevo, inside the command a person ran, and fills only fields that
// are printed: internal/relevo.Status itself never computes a label, because it
// also serves relevo serve. The label is resolved at most once per mastermind id,
// every lookup failure ends in the empty label, and the function never returns
// an error and never prints.
func annotateMasterMindChat(rt relevo.Runtime, rep *view.Report, res chatlabel.Resolver) {
	if rt.MasterMinds == nil {
		return
	}
	labels := make(map[string]chatlabel.Label)
	for i := range rep.Bindings {
		b := &rep.Bindings[i]
		if b.MasterMindID == "" {
			continue
		}
		lbl, ok := labels[b.MasterMindID]
		if !ok {
			if rec, err := rt.MasterMinds.Get(b.MasterMindID); err != nil {
				lbl = chatlabel.Label{}
			} else {
				lbl = res.Resolve(context.Background(), rec.HarnessKind, rec.SessionID, rec.TranscriptLocator)
			}
			labels[b.MasterMindID] = lbl
		}
		b.MasterMindChatLabel = lbl.Text
		b.MasterMindChatLink = lbl.Link
	}
}

func cmdMasterMindRename(args []string) error {
	fs := flag.NewFlagSet("rename", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	positional := fs.Args()
	if len(positional) != 2 {
		return fmt.Errorf("usage: relevo mastermind rename <id|name> <new-name>")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	reg, err := mastermindRegistry(rt)
	if err != nil {
		return err
	}
	rec, err := mastermindLookup(reg, positional[0])
	if err != nil {
		return err
	}

	updated, err := reg.Rename(rec.ID, positional[1])
	if err != nil {
		return err
	}

	fmt.Printf("renamed mastermind %s (%s) to %s\n", rec.Name, rec.ID, updated.Name)
	return nil
}

func cmdMasterMindForget(args []string) error {
	fs := flag.NewFlagSet("forget", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	positional := fs.Args()
	if len(positional) != 1 {
		return fmt.Errorf("usage: relevo mastermind forget <id|name>")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	reg, err := mastermindRegistry(rt)
	if err != nil {
		return err
	}
	rec, err := mastermindLookup(reg, positional[0])
	if err != nil {
		return err
	}

	if err := reg.Forget(rec.ID, mastermindInUse(rt)); err != nil {
		return err
	}

	fmt.Printf("forgot mastermind %s (%s)\n", rec.Name, rec.ID)
	return nil
}

// mastermindLookup resolves one <id|name> argument the way Resolve resolves a
// --mastermind value: by id when it has the id shape, else by name.
func mastermindLookup(reg mastermind.Registry, ref string) (mastermind.Record, error) {
	if mastermind.ValidID(ref) == nil {
		rec, err := reg.Get(ref)
		switch {
		case err == nil:
			return rec, nil
		case errors.Is(err, mastermind.ErrNotFound):
			return mastermind.Record{}, fmt.Errorf("no mastermind with id %s", ref)
		default:
			return mastermind.Record{}, err
		}
	}

	rec, err := reg.ByName(ref)
	switch {
	case err == nil:
		return rec, nil
	case errors.Is(err, mastermind.ErrNotFound):
		return mastermind.Record{}, fmt.Errorf("no mastermind named %s", ref)
	default:
		return mastermind.Record{}, err
	}
}

// errGCUsage is gcScope's usage-error shape: a message cmdUnbind/runGC print
// to stderr verbatim before exiting 2 (#482).
type errGCUsage string

func (e errGCUsage) Error() string { return string(e) }

// gcScope turns unbind --done's --mastermind/--all-masterminds flags into a GC
// scope, with no fallback to "everything" (#482). resolve is injected so the
// function stays pure and needs no registry to test; in production it closes
// over a Runtime and calls mastermind.Resolve.
func gcScope(mastermindFlag string, all bool, resolve func(ref string) (mastermind.Record, error)) (relevo.GCOptions, error) {
	switch {
	case all && mastermindFlag != "":
		return relevo.GCOptions{}, errGCUsage("relevo: --all-masterminds and --mastermind are exclusive")
	case all:
		return relevo.GCOptions{AllMasterMinds: true}, nil
	}

	rec, err := resolve(mastermindFlag)
	if err != nil {
		return relevo.GCOptions{}, errGCUsage(fmt.Sprintf(
			"relevo: unbind --done clears this mastermind's DONE bindings, and no mastermind resolved (%v); pass --mastermind <name|id>, or --all-masterminds to clear every mastermind's", err))
	}
	return relevo.GCOptions{MasterMindID: rec.ID}, nil
}

// mastermindFilter resolves this session's mastermind for the commands that filter
// by it without requiring one: `relevo status` with no name (§3.3) and
// `relevo status --line`. A miss is not an error there -- the caller keeps its
// old behaviour -- and neither is a Runtime with no registry (tests).
func mastermindFilter(rt relevo.Runtime) (mastermind.Record, bool) {
	if rt.MasterMinds == nil {
		return mastermind.Record{}, false
	}
	var now time.Time
	if rt.Now != nil {
		now = rt.Now()
	}
	cwd, _ := os.Getwd()
	rec, _, err := mastermind.Resolve(rt.MasterMinds, mastermind.ResolveInput{
		Env:             os.Getenv,
		PPID:            os.Getppid(),
		ProcStart:       rt.ProcStart,
		Now:             now,
		CWD:             cwd,
		OpencodeSession: rt.OpencodeSession,
	})
	if err != nil {
		return mastermind.Record{}, false
	}
	return rec, true
}

// mastermindInUse is Forget's guard (§4.7): a record any binding that is not DONE
// still names is in use, and forgetting it would strand that binding's
// history. It walks the same store listing `relevo status --all` reads.
//
// An unreadable store refuses the forget: deleting a record a live binding may
// name is worse than making the human retry.
func mastermindInUse(rt relevo.Runtime) func(id string) bool {
	return func(id string) bool {
		counts, err := mastermindBindingCounts(rt)
		if err != nil {
			return true
		}
		return counts[id] > 0
	}
}

// mastermindBindingCounts counts, per mastermind id, the bindings that are not DONE
// and name that mastermind -- the store listing Forget's in-use guard walks. An
// unreadable store is an error, never an empty map: "no bindings" would let
// the daemon's hourly prune delete a record a binding still names.
func mastermindBindingCounts(rt relevo.Runtime) (map[string]int, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, b := range bindings {
		if b.State != store.StateDone && b.MasterMindID != "" {
			counts[b.MasterMindID]++
		}
	}
	return counts, nil
}
