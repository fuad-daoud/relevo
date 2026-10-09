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

// statusChain reports whether name resolves to a chain rather than a binding.
// A store that cannot answer -- no database, a read failure -- reads as "not a
// chain", so the ordinary binding path still decides.
func statusChain(rt relevo.Runtime, name string) bool {
	_, err := rt.Store.Chain(name)
	return err == nil
}

// statusScope resolves the one scope every status verb renders from, and
// classifies what it could not resolve. An identity that stays ambiguous or
// unknown is a refusal naming the next step, never a silent listing of every
// mastermind and never a silent empty set; nothing here is ever internal,
// because an agent can always be told which flag settles it.
func statusScope(mastermindRef string, allMasterMinds, allDone, named bool, rt relevo.Runtime) (relevo.Scope, *mastermind.Record, error) {
	sc, rec, err := relevo.ResolveScope(mastermindRef, allMasterMinds, allDone, named, gcResolver(rt))
	if err != nil {
		var refusal relevo.ScopeRefusal
		if !errors.As(err, &refusal) {
			return sc, nil, fail(codeInternal, "%v", err)
		}
		return sc, nil, failNext(codeUsage, refusal.Next, "%v", refusal)
	}
	return sc, rec, nil
}

// statusFlagValues holds the pointers status parses into.
type statusFlagValues struct {
	all            *bool
	allMasterMinds *bool
	asJSON         *bool
	mastermind     *string
	name           *string
	line           *bool
	chains         *bool
}

// statusFlagSet defines those flags on fs and returns what they parse into.
func statusFlagSet(fs *flag.FlagSet) *statusFlagValues {
	v := &statusFlagValues{}
	v.all = fs.Bool("all", false, "include bindings marked DONE (hidden by default; relevo unbind --done clears them)")
	v.allMasterMinds = fs.Bool("all-masterminds", false, "show every mastermind's bindings instead of this session's")
	v.asJSON = fs.Bool("json", false, "machine-readable output")
	v.mastermind = fs.String("mastermind", "", "show this mastermind's bindings (default: the one this session resolves to)")
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
		return runStatusChains(*asJSON, *v.mastermind, *v.allMasterMinds)
	}

	// --line is today's statusline: one row per builder of the calling
	// mastermind, so it takes no binding and no other output mode. It also
	// takes no --all-masterminds: it names one mastermind on its first line
	// and in its document, which is no answer at all for every one of them.
	if *line {
		if *all || *name != "" || len(fs.Args()) > 0 {
			return fail(codeUsage, "--line cannot be combined with --all/--name")
		}
		if *v.allMasterMinds {
			return fail(codeUsage, "--line cannot be combined with --all-masterminds: it shows one mastermind's builders")
		}
		return runStatusline(*asJSON, *v.mastermind)
	}

	target, err := bindingArg(*name, fs.Args())
	if err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	hintFreshen(*line, nil)
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
	sc, _, err := statusScope(*v.mastermind, *v.allMasterMinds, *all, target != "", rt)
	if err != nil {
		return err
	}
	var rep view.Report
	if isChain {
		rep, err = relevo.ChainStatus(context.Background(), rt, target)
	} else {
		// sc is already resolved, so the rows are built for this scope alone
		// instead of for the whole fleet and then filtered. ScopeReport below
		// still runs, as the post-check.
		rep, err = relevo.ScopedStatus(context.Background(), rt, sc)
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
	rep = relevo.ScopeReport(rep, sc)

	// The mastermind's chat label is computed here, in the command a
	// person ran, and only printed. internal/relevo.Status leaves it empty,
	// so no label is ever computed on, or sent to, a server.
	annotateMasterMindChat(rt, &rep, chatResolver())

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}

	// One line above the rows, only when the daemon's cached check has
	// seen a newer release. Read through the doctor's own Env so `status` and
	// `doctor` can never disagree about the same file. JSON output above stays
	// notice-free.
	env := doctor.NewEnv(rt.Store, releaseInputs())
	running, latest, ok, kind := env.ReleaseState()
	if notice := statusNotice(running, latest, ok, kind); notice != "" {
		fmt.Println(notice)
	}
	// One line above the rows only when a daemon restart right now would
	// kill a process running outside its own scope. The same computation
	// doctor's restart row makes -- cheap, one small file read per running
	// process. JSON output above stays notice-free.
	if notice := restartNotice(restartCheck(rt)); notice != "" {
		fmt.Println(notice)
	}

	// The daemon's own version state, read from its record rather than
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
	return nil
}

// boardBlockFor reads one MasterMind's live board block from the state root's
// files alone -- the pointer, server.json and a pid check, with no database
// read. It is present only when the pointer names a scene, server.json
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

// runStatusline is statusline's body, now reached through `status --line`: the
// same output, COLUMNS and RELEVO_STATUSLINE_MARGIN, over the same scoped
// report every other status surface reads. With asJSON it prints
// StatusLineDoc instead of the text.
//
// A store or owner that cannot answer still prints nothing at all: the
// statusline runs inside a prompt and must never fail one. The scope is the
// exception, because a statusline that cannot say whose builders it is would
// render an empty line that reads exactly like a healthy session.
func runStatusline(asJSON bool, mastermindRef string) error {
	if fi, err := os.Stdin.Stat(); err != nil || view.ShouldDrainStdin(fi.Mode()) {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	rt, err := newRuntime()
	if err != nil {
		if errors.Is(err, errOwnerUnavailable) {
			return nil
		}
		if asJSON {
			return fail(codeInternal, "%v", err)
		}
		fmt.Fprintf(os.Stderr, "relevo status --line: %v\n", err)
		return nil
	}
	sc, rec, err := statusScope(mastermindRef, false, false, false, rt)
	if err != nil {
		return err
	}

	if !asJSON {
		columns, err := strconv.Atoi(os.Getenv("COLUMNS"))
		if err != nil || columns <= 0 {
			columns = 0
		}
		columns = view.StatusLineWidth(columns, os.Getenv("RELEVO_STATUSLINE_MARGIN"))
		// The first line names the mastermind, so each terminal shows which
		// mastermind it is. It is printed before the rows and survives a
		// failure to read them: the line is the mastermind's identity, not a
		// binding row.
		fmt.Print(view.RenderMasterMindLine(rec.Name, columns))
		if root, rerr := store.DefaultRoot(); rerr == nil {
			fmt.Print(view.RenderBoardLine(boardBlockFor(root, sc.MasterMindID, boardProcStart), rec.Name, columns))
		}
		if relevo.PushLive(rt, sc.MasterMindID) {
			return nil
		}
		rep, err := relevo.MasterMindStatus(context.Background(), rt, sc.MasterMindID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "relevo status --line: %v\n", err)
			return nil
		}
		fmt.Print(view.RenderStatusLine(rep, rt.Now(), columns))
		return nil
	}

	doc := view.StatusLineDoc{Now: time.Now().UTC(), Rows: []view.StatusLineRow{}}
	doc.PushLive = relevo.PushLive(rt, sc.MasterMindID)
	doc.MasterMind = &view.StatusLineMasterMind{ID: sc.MasterMindID, Name: rec.Name}
	if root, rerr := store.DefaultRoot(); rerr == nil {
		doc.Board = boardBlockFor(root, sc.MasterMindID, boardProcStart)
	}
	if rep, err := relevo.MasterMindStatus(context.Background(), rt, sc.MasterMindID); err == nil {
		doc.Rows = view.StatusLineRows(rep, rt.Now())
		for i, text := range view.PlainStatusLineRows(doc.Rows, 0) {
			doc.Rows[i].Text = text
		}
	} else {
		fmt.Fprintf(os.Stderr, "relevo status --line: %v\n", err)
	}
	data, _ := json.Marshal(doc)
	fmt.Println(string(data))
	return nil
}

// runStatusChains prints chains and their steps in text or JSON format, under
// the same scope every other status verb uses: a chain is a builder's row, so
// a chain belongs to the mastermind that owns it.
func runStatusChains(asJSON bool, mastermindRef string, allMasterMinds bool) error {
	rt, err := newRuntime()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	if rt.Remote != nil {
		if _, _, serr := relevo.SyncRemoteUnlessDaemon(context.Background(), rt); serr != nil {
			fmt.Fprintf(os.Stderr, "relevo: sync remote bindings: %v\n", serr)
		}
	}
	sc, _, err := statusScope(mastermindRef, allMasterMinds, false, false, rt)
	if err != nil {
		return err
	}
	doc, err := relevo.ReadChainsScope(context.Background(), rt, sc)
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
