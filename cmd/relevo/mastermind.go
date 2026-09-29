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

// mastermindFlagSet declares the bare `mastermind` dispatcher's flags: none
// today. It exists so the registry's parity test finds exactly one installer
// per verb.
func mastermindFlagSet(*flag.FlagSet) {}

// cmdMasterMind dispatches `relevo mastermind init|notice|enable|disable|guide|list|rename|forget`
// (#303 §4.7, #632). It touches no harness:
// a mastermind record is relevo's own identity, not a pane.
func cmdMasterMind(args []string) error {
	const usage = `usage: relevo mastermind init [--name N] [--kind K --session S] [--hook claude]
       relevo mastermind notice --hook claude
       relevo mastermind enable [--repo] [--kind K --session S]
       relevo mastermind disable [--repo] [--kind K --session S]
       relevo mastermind reset [--kind K --session S]
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
	case "notice":
		return cmdMasterMindNotice(args[1:])
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
		return fail(codeUsage, "%q was removed; the daemon prunes dead MasterMinds hourly", "prune")
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		return fail(codeUsage, "relevo mastermind: unknown subcommand %q", args[0])
	}
}

// mastermindRegistry is the CLI's registry: the state root's database kv rows
// `<root>/relevo.db` (P3b round 2 §4.1) on the runtime's clock. A store whose
// database cannot be opened is fatal for the verb.
func mastermindRegistry(rt relevo.Runtime) (*mastermind.DBRegistry, error) {
	d, err := rt.Store.DB()
	if err != nil {
		return nil, err
	}
	return &mastermind.DBRegistry{KV: db.TxKV{DB: d}, Now: rt.Now}, nil
}

// mastermindInitFlagValues holds the pointers `mastermind init` parses into.
type mastermindInitFlagValues struct {
	name    *string
	kind    *string
	session *string
	hook    *string
}

// mastermindInitFlagSet defines those flags on fs and returns what they parse
// into.
func mastermindInitFlagSet(fs *flag.FlagSet) *mastermindInitFlagValues {
	v := &mastermindInitFlagValues{}
	v.name = fs.String("name", "", "mastermind name (default: <agent>-<n>, else <kind>-<n>)")
	v.kind = fs.String("kind", "", "harness kind for an explicit registration (e.g. opencode)")
	v.session = fs.String("session", "", "harness session id for an explicit registration")
	v.hook = fs.String("hook", "", "read a Claude Code SessionStart payload from stdin (only \"claude\")")
	return v
}

func cmdMasterMindInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	v := mastermindInitFlagSet(fs)
	name, kind, session, hook := v.name, v.kind, v.session, v.hook
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
// mastermindListFlagValues holds the pointer `mastermind list` parses into.
type mastermindListFlagValues struct {
	asJSON *bool
}

// mastermindListFlagSet defines that flag on fs and returns what it parses
// into.
func mastermindListFlagSet(fs *flag.FlagSet) *mastermindListFlagValues {
	v := &mastermindListFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the records as a JSON array")
	return v
}

func cmdMasterMindList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	v := mastermindListFlagSet(fs)
	asJSON := v.asJSON
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
		return fail(codeInternal, "%v", err)
	}
	records, err := reg.List()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}

	// The count of non-DONE bindings naming each mastermind, from the same store
	// walk forget's guard uses. Both it and the state column are derived here,
	// not stored on the record: the file is identity, and this is the
	// machine's present answer about it.
	counts, err := mastermindBindingCounts(rt)
	if err != nil {
		return fail(codeInternal, "%v", err)
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

// mastermindRenameFlagSet declares `mastermind rename`'s flags: none today.
func mastermindRenameFlagSet(*flag.FlagSet) {}

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

// mastermindForgetFlagSet declares `mastermind forget`'s flags: none today.
func mastermindForgetFlagSet(*flag.FlagSet) {}

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
