package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/board"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/sanitize"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// filterReport narrows a status report to one binding. An empty name keeps
// every row, because listing them all is what a bare `relevo status` is for.
//
// An unknown name is an error rather than an empty report: a silent blank
// would read exactly like a healthy binding with nothing outstanding.
// scopeReport decides what a status-shaped command shows. A named binding is
// always shown, DONE or not: asking for one by name is already a request for
// that specific thing. Otherwise DONE rows are hidden unless --all, and the
// same rule applies to --json so the two formats never disagree about what
// exists. It is a pure function so the rule can be tested without a harness.
func scopeReport(rep view.Report, name string, all bool) view.Report {
	if name != "" || all {
		return rep
	}
	return view.HideDone(rep)
}

func filterReport(rep view.Report, name string) (view.Report, error) {
	if name == "" {
		return rep, nil
	}
	for _, b := range rep.Bindings {
		if b.Name == name {
			return view.Report{Bindings: []view.BindingStatus{b}}, nil
		}
	}
	return view.Report{}, fail(codeBindingNotFound, "no binding named %q", name)
}

// statusScope is the scope one invocation resolves, and the single value every
// status format reads it through: the human listing, --json and --line. One
// resolution is what keeps the three from disagreeing -- there is no path on
// which one of them picks a scope the others never see.
type statusScope struct {
	// Scope is what every format reads rows for.
	Scope relevo.Scope
	// Rec is the session's MasterMind record when one resolved. HasRec is
	// false in the fleet scope a single-MasterMind or registry-less store
	// resolves to, where there is no record to name.
	Rec    mastermind.Record
	HasRec bool
	// Refusal is why the scope could not be resolved: this session names no
	// MasterMind and more than one owns live bindings here. A refusal is not
	// a scope -- no format falls back to the fleet on it, because the fleet is
	// exactly the other scope the reader must not be shown by accident.
	Refusal string
}

// resolveStatusScope is the whole rule, in one place:
//
//   - --all is the fleet, and needs no identity at all.
//   - A session that resolves its MasterMind reads that Master's bindings.
//   - A session that does not, in a store where at most one MasterMind owns a
//     live binding, has nothing to choose between: the whole store is its own,
//     so it reads everything and the footer is absent.
//   - A session that does not, where more than one MasterMind owns a live
//     binding, is refused -- explicitly, in every format, by the same caller.
func resolveStatusScope(rt relevo.Runtime, all bool) statusScope {
	if all {
		return statusScope{Scope: relevo.Scope{All: true}}
	}

	rec, err := mastermindIdentity(rt)
	if err == nil {
		return statusScope{Scope: relevo.Scope{MasterMindID: rec.ID}, Rec: rec, HasRec: true}
	}

	owners, oerr := mastermindBindingCounts(rt)
	if oerr != nil || len(owners) <= 1 {
		return statusScope{Scope: relevo.Scope{}}
	}
	return statusScope{Refusal: fmt.Sprintf(
		"this session names no MasterMind and %d own live bindings here; set RELEVO_MASTERMIND=<id|name>, or pass --all for every MasterMind's bindings",
		len(owners))}
}

// statusScopeFooter is the one line the human listing adds under its rows when
// the scope it read left another MasterMind's bindings out: the exact count,
// and the command that shows them. Empty for the fleet, for a named binding
// (which never narrows by MasterMind) and when nothing was left out.
func statusScopeFooter(rt relevo.Runtime, scope statusScope, target string) string {
	if target != "" || scope.Scope.Fleet() {
		return ""
	}
	bindings, err := rt.Store.List()
	if err != nil {
		return ""
	}
	n := relevo.OtherMasterMindCount(bindings, scope.Scope)
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d bindings belong to other MasterMinds: relevo status --all", n)
}

// statusChain reports whether name resolves to a chain rather than a binding.
// A store that cannot answer -- no database, a read failure -- reads as "not a
// chain", so the ordinary binding path still decides.
func statusChain(rt relevo.Runtime, name string) bool {
	_, err := rt.Store.Chain(name)
	return err == nil
}

// statusFlagValues holds the pointers status parses into.
type statusFlagValues struct {
	all    *bool
	asJSON *bool
	name   *string
	line   *bool
	chains *bool
}

// statusFlagSet defines those flags on fs and returns what they parse into.
func statusFlagSet(fs *flag.FlagSet) *statusFlagValues {
	v := &statusFlagValues{}
	v.all = fs.Bool("all", false, "every MasterMind's bindings, DONE ones included (hidden by default; relevo unbind --done clears them)")
	v.asJSON = fs.Bool("json", false, "machine-readable output")
	v.name = fs.String("name", "", "show only this binding (default: all)")
	v.line = fs.Bool("line", false, "this mastermind's builders, one row each, for Claude Code's statusLine setting; with --json, output as JSON")
	v.chains = fs.Bool("chains", false, "show chains and their steps")
	return v
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	v := statusFlagSet(fs)
	all, asJSON, name, line, chains := v.all, v.asJSON, v.name, v.line, v.chains
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *chains {
		if *line || *name != "" || len(fs.Args()) > 0 {
			return fail(codeUsage, "--chains cannot be combined with --line/--name")
		}
		return runStatusChains(*asJSON)
	}

	// --line is today's statusline: one row per builder of the calling
	// mastermind, so it takes no binding and no other output mode. --all is
	// the fleet view and reaches it like any other format; naming a binding
	// does not, because the line has no row for one binding.
	if *line {
		if *name != "" || len(fs.Args()) > 0 {
			return fail(codeUsage, "--line cannot be combined with --name")
		}
		return runStatusline(*asJSON, *all)
	}

	target, err := bindingArg(*name, fs.Args())
	if err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	if rt.Remote != nil {
		if _, _, serr := relevo.SyncRemoteUnlessDaemon(context.Background(), rt); serr != nil {
			fmt.Fprintf(os.Stderr, "relevo: sync remote bindings: %v\n", serr)
		}
	}

	// A name that resolves to a chain -- including the chain's own builder
	// member, `<n>` -- is the chain: `status <chain>` lists the chain row with
	// its members under it. The chain lookup runs before filterReport, so a
	// chain name never comes back as a missing binding.
	isChain := target != "" && statusChain(rt, target)
	var rep view.Report
	// The footer is a human line, so it is computed from the scope alone and
	// printed after the rows -- never inside the machine document.
	var footer string
	if isChain {
		rep, err = relevo.ChainStatus(context.Background(), rt, target)
	} else {
		// One scope resolution for the human listing and for --json alike: the
		// report is built from it before the output format is chosen, so the
		// two cannot name different bindings. A named binding keeps the whole
		// store behind it -- asking for one by name is a request for that
		// specific thing, whichever MasterMind owns it.
		scope := resolveStatusScope(rt, *all)
		if target == "" && scope.Refusal != "" {
			return fail(codeRefused, "%s", scope.Refusal)
		}
		if target != "" {
			scope.Scope = relevo.Scope{All: true}
		}
		rep, err = relevo.ScopedStatus(context.Background(), rt, scope.Scope)
		if err != nil {
			return fail(codeInternal, "%v", err)
		}
		footer = statusScopeFooter(rt, scope, target)
	}
	if err != nil {
		return fail(codeInternal, "%v", err)
	}

	// A chain report already holds exactly the chain and its members, so it
	// skips filterReport: narrowing it to the chain row alone would drop the
	// members the named view exists to show.
	if !isChain {
		rep, err = filterReport(rep, target)
		if err != nil {
			return err
		}
	}
	rep = scopeReport(rep, target, *all)

	// #386: the mastermind's chat label is computed here, in the command a
	// person ran, and only printed. internal/relevo.Status leaves it empty,
	// so no label is ever computed on, or sent to, a server.
	annotateMasterMindChat(rt, &rep, chatResolver())

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}

	// #293: one line above the rows, only when the daemon's cached check has
	// seen a newer release. Read through the doctor's own Env so `status` and
	// `doctor` can never disagree about the same file. JSON output above stays
	// notice-free.
	env := doctor.NewEnv(rt.Store, releaseInputs())
	running, latest, ok, kind := env.ReleaseState()
	if notice := statusNotice(running, latest, ok, kind); notice != "" {
		fmt.Println(notice)
	}
	// #370: one line above the rows only when a daemon restart right now would
	// kill a process running outside its own scope. The same computation
	// doctor's restart row makes -- cheap, one small file read per running
	// process. JSON output above stays notice-free.
	if notice := restartNotice(restartCheck(rt)); notice != "" {
		fmt.Println(notice)
	}

	// #371: the daemon's own version state, read from its record rather than
	// probed. A read error prints nothing: a status must never fail because its
	// record could not be read.
	if daemonRunning, derr := rt.Store.DaemonRunning(); derr == nil {
		if info, iok, ierr := rt.Store.ReadDaemonInfo(); ierr == nil {
			if notice := daemonNotice(buildVersion(), info, iok, daemonRunning); notice != "" {
				fmt.Println(notice)
			}
		}
	}

	fmt.Print(view.RenderStatus(rep))
	if footer != "" {
		fmt.Println(footer)
	}
	return nil
}

// boardBlockFor reads one MasterMind's live board block from the state root's
// files alone -- the pointer, server.json and a pid check, with no database
// read (S8). It is present only when the pointer names a scene, server.json
// advertises the same scene, and the server's pid is alive with a matching
// start. Any read error, a missing pointer, a scene mismatch or a dead pid
// yields nil, never a failure.
func boardBlockFor(root, id string, procStart func(pid int) (int64, error)) *view.StatusLineBoard {
	if root == "" || id == "" {
		return nil
	}
	liveDir := filepath.Join(root, "boards", id)
	scene, present, err := board.Pointer(liveDir)
	if err != nil || !present {
		return nil
	}
	url, ok, err := board.LiveURL(liveDir, scene, procStart)
	if err != nil || !ok {
		return nil
	}
	return &view.StatusLineBoard{Name: scene, Scope: string(board.ScopeLive), URL: url}
}

// runStatusline is statusline's body (the old cmdStatusline), now reached
// through `status --line`: the same output, COLUMNS,
// RELEVO_STATUSLINE_MARGIN and the same scope every other status format reads.
// With asJSON, it prints StatusLineDoc as JSON.
//
// A refused scope names itself on stderr and draws no row. The line never
// fails a prompt and never falls back to the fleet on a refusal: showing every
// MasterMind's builders because the session could not name one is the one
// output that would silently disagree with what `relevo status` refuses to
// show.
func runStatusline(asJSON bool, all bool) error {
	if fi, err := os.Stdin.Stat(); err != nil || view.ShouldDrainStdin(fi.Mode()) {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	if !asJSON {
		columns, err := strconv.Atoi(os.Getenv("COLUMNS"))
		if err != nil || columns <= 0 {
			columns = 0
		}
		columns = view.StatusLineWidth(columns, os.Getenv("RELEVO_STATUSLINE_MARGIN"))
		rt, err := newRuntime()
		if err != nil {
			// An owner that never answered prints nothing at all: the
			// statusline runs inside a prompt and must never fail one. Every
			// other failure keeps the line below.
			if errors.Is(err, errOwnerUnavailable) {
				return nil
			}
			fmt.Fprintf(os.Stderr, "relevo status --line: %v\n", err)
			return nil
		}
		scope := resolveStatusScope(rt, all)
		if scope.Refusal != "" {
			fmt.Fprintf(os.Stderr, "relevo status --line: %s\n", scope.Refusal)
			return nil
		}
		// The first line names the mastermind, so each terminal shows which
		// mastermind it is. It is printed before MasterMindStatus and survives a
		// MasterMindStatus failure: the line is the mastermind's identity, not a
		// binding row. The fleet scope has no record to name, so it draws the
		// rows alone -- the same bindings `relevo status` would list.
		if scope.HasRec {
			fmt.Print(view.RenderMasterMindLine(scope.Rec.Name, columns))
			if root, rerr := store.DefaultRoot(); rerr == nil {
				fmt.Print(view.RenderBoardLine(boardBlockFor(root, scope.Rec.ID, boardProcStart), scope.Rec.Name, columns))
			}
		}
		rep, err := relevo.MasterMindStatus(context.Background(), rt, scope.Scope)
		if err != nil {
			fmt.Fprintf(os.Stderr, "relevo status --line: %v\n", err)
			return nil
		}
		fmt.Print(view.RenderStatusLine(rep, rt.Now(), columns))
		return nil
	}

	rt, err := newRuntime()
	if err != nil {
		doc := view.StatusLineDoc{Now: time.Now().UTC(), Rows: []view.StatusLineRow{}}
		data, _ := json.Marshal(doc)
		fmt.Println(string(data))
		return nil
	}
	now := rt.Now().UTC()
	doc := view.StatusLineDoc{Now: now, Rows: []view.StatusLineRow{}}
	scope := resolveStatusScope(rt, all)
	switch {
	case scope.Refusal != "":
		fmt.Fprintf(os.Stderr, "relevo status --line: %s\n", scope.Refusal)
	case scope.HasRec:
		doc.MasterMind = &view.StatusLineMasterMind{ID: scope.Rec.ID, Name: scope.Rec.Name}
		if root, rerr := store.DefaultRoot(); rerr == nil {
			doc.Board = boardBlockFor(root, scope.Rec.ID, boardProcStart)
		}
		rep, err := relevo.MasterMindStatus(context.Background(), rt, scope.Scope)
		if err == nil {
			doc.Rows = view.StatusLineRows(rep, rt.Now())
			for i, text := range view.PlainStatusLineRows(doc.Rows, 0) {
				doc.Rows[i].Text = text
			}
		} else {
			fmt.Fprintf(os.Stderr, "relevo status --line: %v\n", err)
		}
	default:
		// The fleet scope: rows without a mastermind to name, which is what
		// `relevo status` lists in the same store.
		rep, err := relevo.MasterMindStatus(context.Background(), rt, scope.Scope)
		if err == nil {
			doc.Rows = view.StatusLineRows(rep, rt.Now())
			for i, text := range view.PlainStatusLineRows(doc.Rows, 0) {
				doc.Rows[i].Text = text
			}
		} else {
			fmt.Fprintf(os.Stderr, "relevo status --line: %v\n", err)
		}
	}
	data, _ := json.Marshal(doc)
	fmt.Println(string(data))
	return nil
}

// runStatusChains prints chains and their steps in text or JSON format.
func runStatusChains(asJSON bool) error {
	rt, err := newRuntime()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	if rt.Remote != nil {
		if _, _, serr := relevo.SyncRemoteUnlessDaemon(context.Background(), rt); serr != nil {
			fmt.Fprintf(os.Stderr, "relevo: sync remote bindings: %v\n", serr)
		}
	}
	doc, err := relevo.ReadChains(context.Background(), rt)
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	if asJSON {
		return printDoc(doc)
	}
	return printChainsStatus(doc)
}

// printChainsStatus formats chains as one line per chain, indented for nesting,
// followed by the halt reason when halted.
func printChainsStatus(doc relevo.ChainsDoc) error {
	for _, c := range doc.Chains {
		indent := strings.Repeat("  ", c.Depth)
		stepPlans := ""
		if c.Step != "" {
			stepPlans = fmt.Sprintf("%s · plans %d/%d", sanitize.Text(c.Step), c.PlanPos, c.PlanTotal)
		} else {
			stepPlans = fmt.Sprintf("plans %d/%d", c.PlanPos, c.PlanTotal)
		}
		elapsed := view.AgeText(c.Elapsed)
		where := c.Where
		if where == "" {
			where = "local"
		}
		fmt.Printf("%s%s  %s  %s  %s  %s\n", indent, c.Name, sanitize.Text(c.Status), stepPlans, elapsed, where)
		if c.Reason != "" {
			fmt.Printf("%s  %s\n", indent, sanitize.Text(c.Reason))
		}
	}
	return nil
}
