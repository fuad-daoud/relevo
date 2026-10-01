package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/relevo"
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

// filterReportMasterMind narrows a status report to one mastermind's bindings. It
// is a pure function so the rule is testable without a harness. An empty
// mastermind id keeps every row, which is what a runtime with no registry gets.
func filterReportMasterMind(rep view.Report, mastermindID string) view.Report {
	if mastermindID == "" {
		return rep
	}
	kept := rep.Bindings[:0:0]
	for _, b := range rep.Bindings {
		if b.MasterMindID == mastermindID {
			kept = append(kept, b)
		}
	}
	rep.Bindings = kept
	return rep
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
}

// statusFlagSet defines those flags on fs and returns what they parse into.
func statusFlagSet(fs *flag.FlagSet) *statusFlagValues {
	v := &statusFlagValues{}
	v.all = fs.Bool("all", false, "include bindings marked DONE (hidden by default; relevo unbind --done clears them)")
	v.asJSON = fs.Bool("json", false, "machine-readable output")
	v.name = fs.String("name", "", "show only this binding (default: all)")
	v.line = fs.Bool("line", false, "this mastermind's builders, one row each, for Claude Code's statusLine setting; with --json, output as JSON")
	return v
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	v := statusFlagSet(fs)
	all, asJSON, name, line := v.all, v.asJSON, v.name, v.line
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// --line is today's statusline: one row per builder of the calling
	// mastermind, so it takes no binding and no other output mode (§4.5).
	if *line {
		if *all || *name != "" || len(fs.Args()) > 0 {
			return fail(codeUsage, "--line cannot be combined with --all/--name")
		}
		return runStatusline(*asJSON)
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
	if isChain {
		rep, err = relevo.ChainStatus(context.Background(), rt, target)
	} else {
		rep, err = relevo.Status(context.Background(), rt)
	}
	if err != nil {
		return fail(codeInternal, "%v", err)
	}

	// §3.3: a bare `relevo status` shows the calling mastermind's bindings. A
	// session with no mastermind -- no registry, no match -- keeps the old
	// behaviour and lists everything.
	if target == "" {
		if rec, ok := mastermindFilter(rt); ok {
			rep = filterReportMasterMind(rep, rec.ID)
		}
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
	return nil
}

// runStatusline is statusline's body (the old cmdStatusline), now reached
// through `status --line` (§4.5): the same output, COLUMNS,
// RELEVO_STATUSLINE_MARGIN and mastermind filtering. With asJSON, it prints
// StatusLineDoc as JSON.
func runStatusline(asJSON bool) error {
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
		// §3.3: the row set is the calling mastermind's bindings. A session with no
		// mastermind renders nothing, the same as no mastermind did
		// before #303.
		rec, ok := mastermindFilter(rt)
		if !ok {
			return nil
		}
		// #386: the first line names the mastermind, so each terminal shows which
		// mastermind it is. It is printed before MasterMindStatus and survives a
		// MasterMindStatus failure: the line is the mastermind's identity, not a
		// binding row.
		fmt.Print(view.RenderMasterMindLine(rec.Name, columns))
		rep, err := relevo.MasterMindStatus(context.Background(), rt, rec.ID)
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
	rec, ok := mastermindFilter(rt)
	now := rt.Now().UTC()
	doc := view.StatusLineDoc{Now: now, Rows: []view.StatusLineRow{}}
	if ok {
		doc.MasterMind = &view.StatusLineMasterMind{ID: rec.ID, Name: rec.Name}
		rep, err := relevo.MasterMindStatus(context.Background(), rt, rec.ID)
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
