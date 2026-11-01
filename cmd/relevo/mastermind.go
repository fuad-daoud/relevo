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
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// mastermindPriorIDTimeout bounds how long `relevo mastermind init` waits for the
// database to open: a SessionStart hook must never block a session on sqlite.
const mastermindPriorIDTimeout = 2 * time.Second

// mastermindFlagSet exists so the registry's parity test finds exactly one
// installer per verb.
func mastermindFlagSet(*flag.FlagSet) {}

// cmdMasterMind dispatches `relevo mastermind init|notice|enable|disable|guide|list|rename|forget`.
// It touches no harness: a mastermind record is relevo's own identity, not a pane.
func cmdMasterMind(args []string) error {
	const usage = `usage: relevo mastermind init [--name N] [--kind K --session S] [--hook claude|muse]
       relevo mastermind notice --hook claude|muse
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
		// Pruning is automatic; the verb names that and exits 2
		// like every other removed spelling.
		return fail(codeUsage, "%q was removed; the daemon prunes dead MasterMinds hourly", "prune")
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		return fail(codeUsage, "relevo mastermind: unknown subcommand %q", args[0])
	}
}

// mastermindRegistry is the registry over the store root's machine-local file:
// records are this machine's own, so they bind the local file.
func mastermindRegistry(rt relevo.Runtime) (*mastermind.DBRegistry, error) {
	d, err := rt.Store.DB()
	if err != nil {
		return nil, err
	}
	return &mastermind.DBRegistry{KV: db.TxKV{DB: d.LocalOrSelf()}, Now: rt.Now}, nil
}

type mastermindInitFlagValues struct {
	name    *string
	kind    *string
	session *string
	hook    *string
	asJSON  *bool
}

func mastermindInitFlagSet(fs *flag.FlagSet) *mastermindInitFlagValues {
	v := &mastermindInitFlagValues{}
	v.name = fs.String("name", "", "mastermind name (default: <agent>-<n>, else <kind>-<n>)")
	v.kind = fs.String("kind", "", "harness kind for an explicit registration (e.g. opencode)")
	v.session = fs.String("session", "", "harness session id for an explicit registration")
	v.hook = fs.String("hook", "", "read a Claude-shaped SessionStart payload from stdin (only \"claude\" or \"muse\")")
	v.asJSON = fs.Bool("json", false, "print the document init produced")
	return v
}

func cmdMasterMindInit(args []string) error {
	return outcomeError(cmdMasterMindInitRun(args))
}

func cmdMasterMindInitRun(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	v := mastermindInitFlagSet(fs)
	name, kind, session, hook, asJSON := v.name, v.kind, v.session, v.hook, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *hook != "" {
		// The hook form's stdout is Claude's hook contract, so --json is
		// accepted and has no effect here.
		if *hook != "claude" && *hook != "muse" {
			return fail(codeUsage, "relevo mastermind init --hook supports only \"claude\" and \"muse\", got %q", *hook)
		}
		if *kind != "" || *session != "" {
			return fail(codeUsage, "relevo mastermind init --hook reads its kind and session from the hook payload, not --kind/--session")
		}
		return mastermindInitHook(*name, *hook)
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

	if *asJSON {
		fmt.Fprintf(os.Stderr, "export RELEVO_MASTERMIND=%s\n", rec.ID)
		return printDoc(mastermindDocOf(rec.ID, rec.Name, string(res), mastermindRepoLabel(rt, cwd)))
	}
	fmt.Printf("MasterMind %s (%s) %s\n", rec.Name, rec.ID, res)
	fmt.Printf("export RELEVO_MASTERMIND=%s\n", rec.ID)
	return nil
}

// mastermindRepoLabel is the repository label a mastermind document names: the
// origin URL, else the common dir, else "" outside a repository.
func mastermindRepoLabel(rt relevo.Runtime, cwd string) string {
	ref := mastermindRepoOf(context.Background(), rt, cwd)
	if ref.OriginURL != nil {
		return *ref.OriginURL
	}
	if ref.CommonDir != nil {
		return *ref.CommonDir
	}
	return ""
}

// mastermindHostStart reads a host process's start time in Unix seconds, the
// pid-reuse defence behind host_started_at. A failure reports 0 rather than
// failing the caller: the record is still useful without it.
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
	if _, err := io.WriteString(f, line); err != nil {
		_ = f.Close()
		return fmt.Errorf("append %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("append %s: %w", path, err)
	}
	return nil
}

type mastermindListFlagValues struct {
	asJSON *bool
}

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

	// DBRegistry.List sorts by name, which is the order the list documents.
	reg, err := mastermindRegistry(rt)
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	records, err := reg.List()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}

	// The count of non-DONE bindings naming each mastermind, derived here rather
	// than stored on the record.
	counts, err := mastermindBindingCounts(rt)
	if err != nil {
		return fail(codeInternal, "%v", err)
	}

	// The chat column is read from the harness's own files when the command
	// runs, never stored on the record.
	res := chatResolver()
	labels := make([]chatlabel.Label, len(records))
	for i, rec := range records {
		labels[i] = res.Resolve(context.Background(), rec.HarnessKind, rec.SessionID)
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

	// Tabwriter, not fixed widths: a long session id or cwd would shift every
	// column after it.
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if err := printMastermindTable(w, records, labels, counts, rt.ProcStart); err != nil {
		return err
	}
	return w.Flush()
}

// printMastermindTable renders the list table; a failed write fails the
// command like any other output error.
func printMastermindTable(w *tabwriter.Writer, records []mastermind.Record, labels []chatlabel.Label, counts map[string]int, procStart func(pid int) (int64, error)) error {
	if _, err := fmt.Fprintln(w, "name\tchat\tid\tkind\tsession\thost pid\tstate\tbindings\tcwd\tseen"); err != nil {
		return fmt.Errorf("write mastermind list: %w", err)
	}
	for i, rec := range records {
		host := "-"
		if rec.HostPID > 0 {
			host = strconv.Itoa(rec.HostPID)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			rec.Name, labels[i].String(), rec.ID, rec.HarnessKind, rec.SessionID, host,
			mastermind.RecordState(rec, procStart), counts[rec.ID], rec.CWD,
			rec.SeenAt.UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("write mastermind list: %w", err)
		}
	}
	return nil
}

// mastermindListView is one record as `relevo mastermind list --json` renders it,
// plus the two derived columns the table shows.
type mastermindListView struct {
	mastermind.Record
	State    string `json:"state"`
	Bindings int    `json:"bindings"`
	// ChatLabel and ChatLink are the harness's own name for the session.
	ChatLabel string `json:"chat_label,omitempty"`
	ChatLink  string `json:"chat_link,omitempty"`
}

// chatResolver is the label reader `relevo mastermind list` uses; the opencode
// title shell-out is wired only when sqlite3 is on PATH.
func chatResolver() chatlabel.Resolver {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return chatlabel.Resolver{}
	}
	return chatlabel.Resolver{Exec: binExec{}, OpencodeDB: opencodeDBPath()}
}

// annotateMasterMindChat fills each binding row's chat label and link from the
// mastermind record the row names. It fills only fields that are printed, at
// most once per mastermind id; every lookup failure ends in the empty label,
// and the function never returns an error and never prints.
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
				lbl = res.Resolve(context.Background(), rec.HarnessKind, rec.SessionID)
			}
			labels[b.MasterMindID] = lbl
		}
		b.MasterMindChatLabel = lbl.Text
		b.MasterMindChatLink = lbl.Link
	}
}

type mastermindRenameFlagValues struct {
	asJSON *bool
}

func mastermindRenameFlagSet(fs *flag.FlagSet) *mastermindRenameFlagValues {
	v := &mastermindRenameFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the rename produced")
	return v
}

func cmdMasterMindRename(args []string) error {
	return outcomeError(cmdMasterMindRenameRun(args))
}

func cmdMasterMindRenameRun(args []string) error {
	fs := flag.NewFlagSet("rename", flag.ContinueOnError)
	v := mastermindRenameFlagSet(fs)
	asJSON := v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	positional := fs.Args()
	if len(positional) != 2 {
		return fail(codeUsage, "mastermind rename wants <id|name> <new-name>")
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

	if *asJSON {
		return printDoc(mastermindRenameDocOf(rec.ID, rec.Name, updated.Name))
	}
	fmt.Printf("renamed mastermind %s (%s) to %s\n", rec.Name, rec.ID, updated.Name)
	return nil
}

type mastermindForgetFlagValues struct {
	asJSON *bool
}

func mastermindForgetFlagSet(fs *flag.FlagSet) *mastermindForgetFlagValues {
	v := &mastermindForgetFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the forget produced")
	return v
}

func cmdMasterMindForget(args []string) error {
	return outcomeError(cmdMasterMindForgetRun(args))
}

func cmdMasterMindForgetRun(args []string) error {
	fs := flag.NewFlagSet("forget", flag.ContinueOnError)
	v := mastermindForgetFlagSet(fs)
	asJSON := v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	positional := fs.Args()
	if len(positional) != 1 {
		return fail(codeUsage, "mastermind forget wants <id|name>")
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

	if *asJSON {
		return printDoc(mastermindDocOf(rec.ID, rec.Name, "forgotten", ""))
	}
	fmt.Printf("forgot mastermind %s (%s)\n", rec.Name, rec.ID)
	return nil
}

// mastermindLookup resolves one <id|name> argument: by id when it has the id
// shape, else by name.
func mastermindLookup(reg mastermind.Registry, ref string) (mastermind.Record, error) {
	if mastermind.ValidID(ref) == nil {
		rec, err := reg.Get(ref)
		switch {
		case err == nil:
			return rec, nil
		case errors.Is(err, mastermind.ErrNotFound):
			return mastermind.Record{}, failWrap(codeMastermindNotFound, err, "no mastermind with id %s", ref)
		default:
			return mastermind.Record{}, err
		}
	}

	rec, err := reg.ByName(ref)
	switch {
	case err == nil:
		return rec, nil
	case errors.Is(err, mastermind.ErrNotFound):
		return mastermind.Record{}, failWrap(codeMastermindNotFound, err, "no mastermind named %s", ref)
	default:
		return mastermind.Record{}, err
	}
}

// errGCUsage is gcScope's usage-error shape, printed to stderr verbatim.
type errGCUsage string

func (e errGCUsage) Error() string { return string(e) }

// gcScope turns unbind --done's --mastermind/--all-masterminds flags into a GC
// scope, with no fallback to "everything". resolve is injected so the function
// stays pure and needs no registry to test.
func gcScope(mastermindFlag string, all bool, resolve func(ref string) (mastermind.Record, error)) (housekeep.GCOptions, error) {
	switch {
	case all && mastermindFlag != "":
		return housekeep.GCOptions{}, errGCUsage("relevo: --all-masterminds and --mastermind are exclusive")
	case all:
		return housekeep.GCOptions{AllMasterMinds: true}, nil
	}

	rec, err := resolve(mastermindFlag)
	if err != nil {
		return housekeep.GCOptions{}, errGCUsage(fmt.Sprintf(
			"relevo: unbind --done clears this mastermind's DONE bindings, and no mastermind resolved (%v); pass --mastermind <name|id>, or --all-masterminds to clear every mastermind's", err))
	}
	return housekeep.GCOptions{MasterMindID: rec.ID}, nil
}

// mastermindInUse is Forget's guard: a record any binding that is not DONE still
// names is in use, and forgetting it would strand that binding's history. An
// unreadable store refuses the forget: deleting a record a live binding may name
// is worse than making the human retry.
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
// and name that mastermind. An unreadable store is an error, never an empty map:
// "no bindings" would let the hourly prune delete a record a binding still names.
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
