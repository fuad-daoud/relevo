package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/pick"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

type unbindFlagValues struct {
	name           *string
	archive        *bool
	pick           *bool
	done           *bool
	delete         *bool
	dryRun         *bool
	sweep          *bool
	force          *bool
	mastermindRef  *string
	allMasterMinds *bool
	asJSON         *bool
}

func unbindFlagSet(fs *flag.FlagSet) *unbindFlagValues {
	v := &unbindFlagValues{}
	v.name = fs.String("name", "", "binding to unbind")
	v.archive = fs.Bool("archive", false, "move the binding aside instead of deleting it, keeping its round log")
	v.pick = fs.Bool("pick", false, "choose the binding from a list (needs a terminal)")
	v.done = fs.Bool("done", false, "clear the DONE bindings of the calling mastermind (--all-masterminds: of every mastermind)")
	v.delete = fs.Bool("delete", false, "with --done: remove each finished binding's directory instead of archiving it")
	v.dryRun = fs.Bool("dry-run", false, "with --done or --sweep: list what would be cleared, change nothing")
	v.sweep = fs.Bool("sweep", false, "delete relevo/<name> branches and refs/relevo/<name>/* refs of bindings that no longer exist, once they are on a remote-tracking ref")
	v.force = fs.Bool("force", false, "archive or delete the binding even when its worktree is kept (uncommitted changes); with --done: the DONE bindings too")
	v.mastermindRef = fs.String("mastermind", "", "with --done: clear this mastermind's DONE bindings (id or name; default: $RELEVO_MASTERMIND, else this session's host)")
	v.allMasterMinds = fs.Bool("all-masterminds", false, "with --done: clear every mastermind's DONE bindings, including ones with no mastermind")
	v.asJSON = fs.Bool("json", false, "print the result as a JSON document")
	return v
}

// checkUnbindFlags refuses the flag combinations no unbind path takes, before
// any store or network access.
func checkUnbindFlags(v *unbindFlagValues, nArgs int) error {
	if *v.sweep {
		if *v.done || *v.delete || *v.archive || *v.pick || *v.name != "" || nArgs > 0 || *v.mastermindRef != "" || *v.allMasterMinds || *v.force {
			return fail(codeRefused, "--sweep takes no binding and no other flag except --dry-run")
		}
		return nil
	}
	if (*v.mastermindRef != "" || *v.allMasterMinds) && !*v.done {
		return fail(codeRefused, "--mastermind and --all-masterminds go with --done")
	}
	if *v.dryRun && !*v.done {
		return fail(codeRefused, "--dry-run goes with --done or --sweep")
	}
	if *v.delete && !*v.done {
		return fail(codeRefused, "--delete goes with --done")
	}
	if *v.done && *v.archive {
		return fail(codeRefused, "--archive goes with a named binding or --pick, not --done")
	}
	if *v.done && (*v.name != "" || nArgs > 0 || *v.pick) {
		return fail(codeRefused, "--done clears DONE bindings; do not also name one or pass --pick")
	}
	if *v.pick && *v.force {
		return fail(codeRefused, "--force goes with a named binding or --done, not --pick")
	}
	return nil
}

func cmdUnbind(args []string) error {
	fs := flag.NewFlagSet("unbind", flag.ContinueOnError)
	v := unbindFlagSet(fs)
	name, archive, pickFlag := v.name, v.archive, v.pick
	done, delete, dryRun := v.done, v.delete, v.dryRun
	sweep, mastermindRef, allMasterMinds := v.sweep, v.mastermindRef, v.allMasterMinds
	asJSON, force := v.asJSON, v.force
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if err := checkUnbindFlags(v, len(fs.Args())); err != nil {
		return err
	}

	if *sweep {
		return runSweep(*dryRun, *asJSON)
	}

	// --done is gc, not unbind: naming one or picking one contradicts it.
	if *done {
		return runGC(*delete, *dryRun, *force, *mastermindRef, *allMasterMinds, *asJSON)
	}

	if *pickFlag {
		if err := pickNamesNothing(*name, fs.Args()); err != nil {
			return err
		}
		return runPick(pick.Options{Verb: pick.VerbUnbind, Archive: *archive})
	}

	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fail(codeUsage, "usage: relevo unbind <name> | --pick [--archive]  (or --name <name>)%s\n"+
			"unbind removes a binding; it will not guess which one you meant", bindingHint("unbind"))
	}

	rt, err := newRuntime()
	if err != nil {
		return writeError(err)
	}

	res, err := relevo.UnbindWith(context.Background(), rt, target, *archive, *force)
	if errors.Is(err, relevo.ErrWorktreeKept) {
		return failNext(codeRefused, "relevo unbind "+target+" --force", "%v", err)
	}
	if err != nil {
		return writeError(err)
	}

	if *asJSON {
		if perr := printDoc(unbindDocOf(target, res)); perr != nil {
			return perr
		}
	} else {
		fmt.Println(relevo.UnbindText(target, res))
	}
	warnWaitingOnYou(rt, target)

	return nil
}

func runSweep(dryRun, asJSON bool) error {
	rt, err := newRuntime()
	if err != nil {
		return writeError(err)
	}

	dir, err := os.Getwd()
	if err != nil {
		return writeError(fmt.Errorf("resolve working directory: %w", err))
	}

	res, err := housekeep.SweepRefs(context.Background(), relevo.HousekeepDeps(rt), dir, dryRun)
	if err != nil {
		return writeError(err)
	}

	if asJSON {
		return printDoc(sweepDocOf(dryRun, res))
	}

	if len(res.Refs) == 0 {
		fmt.Printf("no relevo refs in %s\n", dir)
		return nil
	}

	for _, line := range housekeep.RefLines(res.Refs) {
		fmt.Println(line)
	}

	var n, m int
	for _, r := range res.Refs {
		if (dryRun && r.WouldDelete) || (!dryRun && r.Deleted) {
			n++
		} else if r.Reason != "" {
			m++
		}
	}

	if dryRun {
		fmt.Printf("%d would be deleted, %d kept\n", n, m)
	} else {
		fmt.Printf("%d deleted, %d kept\n", n, m)
	}

	return nil
}

// runGC clears the calling mastermind's DONE bindings, or every mastermind's
// with --all-masterminds.
func runGC(delete, dryRun, force bool, mastermindRef string, all, asJSON bool) error {
	rt, err := newRuntime()
	if err != nil {
		return writeError(err)
	}

	opts, err := gcScope(mastermindRef, all, gcResolver(rt))
	if err != nil {
		return fail(codeUsage, "%v", err)
	}
	opts.Delete = delete
	opts.DryRun = dryRun
	opts.Force = force

	done, err := housekeep.GC(context.Background(), relevo.HousekeepDeps(rt), opts)
	if err != nil {
		return writeError(err)
	}

	if asJSON {
		return printDoc(gcDocOf(dryRun, done))
	}

	if len(done) == 0 {
		if opts.AllMasterMinds {
			fmt.Println("no finished bindings to clear")
		} else {
			fmt.Printf("no finished bindings to clear for mastermind %s\n", mastermindLabel(rt, opts.MasterMindID))
		}
		return nil
	}

	labels := make(map[string]string)
	label := func(id string) string {
		if l, ok := labels[id]; ok {
			return l
		}
		l := mastermindLabel(rt, id)
		labels[id] = l
		return l
	}

	for _, r := range done {
		printGCResult(r, label(r.MasterMindID), dryRun)
	}

	if dryRun {
		fmt.Printf("\n%d binding(s) would be cleared; re-run without --dry-run\n", len(done))
	}

	return nil
}

func printGCResult(r housekeep.GCResult, lbl string, dryRun bool) {
	switch {
	case dryRun:
		verb := "would clear"
		if r.BindingKept {
			verb = "would keep "
		}
		wtMsg := ""
		if r.WorktreeRemoved != "" {
			wtMsg = fmt.Sprintf(" (worktree %s would be removed)", r.WorktreeRemoved)
		} else if r.WorktreeKept != "" {
			wtMsg = fmt.Sprintf(" (worktree %s kept: %s)", r.WorktreeKept, r.KeptReason)
		} else if r.WorktreeGone != "" {
			wtMsg = fmt.Sprintf(" (worktree %s already gone)", r.WorktreeGone)
		}
		fmt.Printf("%s %-10s %-14s %s (%d rounds)%s\n", verb, r.Name, "["+lbl+"]", r.CWD, r.Rounds, wtMsg)
	case r.BindingKept:
		fmt.Printf("kept        %-10s [%s]\n", r.Name, lbl)
		fmt.Printf("            worktree %s kept: %s\n              commit or discard it, then run relevo unbind --done again, or --force to archive it anyway\n",
			r.WorktreeKept, r.KeptReason)
	case r.Archived:
		fmt.Printf("archived    %-10s [%s]\n", r.Name, lbl)
		printGCWorktree(r)
	default:
		fmt.Printf("deleted     %-10s [%s] %s (%d rounds)\n", r.Name, lbl, r.CWD, r.Rounds)
		printGCWorktree(r)
	}
	if r.ServerErr != "" {
		fmt.Printf("            note: server not released: %s\n", r.ServerErr)
	}
	for _, line := range housekeep.RefLines(r.Refs) {
		fmt.Printf("            %s\n", line)
	}
}

func printGCWorktree(r housekeep.GCResult) {
	switch {
	case r.WorktreeRemoved != "":
		fmt.Printf("            removed worktree %s\n", r.WorktreeRemoved)
	case r.WorktreeKept != "":
		fmt.Printf("            kept worktree %s (%s)\n              remove by hand: git -C %s worktree remove %s\n",
			r.WorktreeKept, r.KeptReason, r.WorktreeKept, r.WorktreeKept)
	case r.WorktreeGone != "":
		fmt.Printf("            worktree %s was already gone\n", r.WorktreeGone)
	}
}

// gcResolver resolves a mastermind without registering one. A runtime with no
// registry is a resolve error, not a miss, so a caller can refuse rather than
// fall back.
func gcResolver(rt relevo.Runtime) func(ref string) (mastermind.Record, error) {
	return func(ref string) (mastermind.Record, error) {
		if rt.MasterMinds == nil {
			return mastermind.Record{}, errors.New("no mastermind registry")
		}
		var now time.Time
		if rt.Now != nil {
			now = rt.Now()
		}
		cwd, _ := os.Getwd()
		rec, _, err := mastermind.Resolve(rt.MasterMinds, mastermind.ResolveInput{
			Flag:            ref,
			Env:             os.Getenv,
			PPID:            os.Getppid(),
			ProcStart:       rt.ProcStart,
			MuseHost:        rt.MuseHost,
			Now:             now,
			CWD:             cwd,
			OpencodeSession: rt.OpencodeSession,
			Heal:            true,
		})
		return rec, err
	}
}

// mastermindLabel names a mastermind in a result line: "(none)" for "", else
// the record's name or the id itself.
func mastermindLabel(rt relevo.Runtime, id string) string {
	if id == "" {
		return "(none)"
	}
	if rt.MasterMinds != nil {
		if rec, err := rt.MasterMinds.Get(id); err == nil {
			return rec.Name
		}
	}
	return id
}
