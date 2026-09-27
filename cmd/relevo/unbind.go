package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/pick"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

func cmdUnbind(args []string) error {
	fs := flag.NewFlagSet("unbind", flag.ContinueOnError)
	name := fs.String("name", "", "binding to unbind")
	archive := fs.Bool("archive", false, "move the binding aside instead of deleting it, keeping its round log")
	pickFlag := fs.Bool("pick", false, "choose the binding from a list (needs a terminal)")
	done := fs.Bool("done", false, "clear the DONE bindings of the calling mastermind (--all-masterminds: of every mastermind)")
	delete := fs.Bool("delete", false, "with --done: remove each finished binding's directory instead of archiving it")
	dryRun := fs.Bool("dry-run", false, "with --done or --sweep: list what would be cleared, change nothing")
	sweep := fs.Bool("sweep", false, "delete relevo/<name> branches and refs/relevo/<name>/* refs of bindings that no longer exist, once they are on a remote-tracking ref")
	mastermindRef := fs.String("mastermind", "", "with --done: clear this mastermind's DONE bindings (id or name; default: $RELEVO_MASTERMIND, else this session's host)")
	allMasterMinds := fs.Bool("all-masterminds", false, "with --done: clear every mastermind's DONE bindings, including ones with no mastermind")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *sweep {
		if *done || *delete || *archive || *pickFlag || *name != "" || len(fs.Args()) > 0 || *mastermindRef != "" || *allMasterMinds {
			fmt.Fprintln(os.Stderr, "relevo: --sweep takes no binding and no other flag except --dry-run")
			return exitCodeErr{code: 2}
		}
		return runSweep(*dryRun)
	}

	if (*mastermindRef != "" || *allMasterMinds) && !*done {
		fmt.Fprintln(os.Stderr, "relevo: --mastermind and --all-masterminds go with --done")
		return exitCodeErr{code: 2}
	}

	// --done is gc, not unbind: it clears every DONE binding, so naming one or
	// asking to pick one contradicts it (§4.3).
	if *done {
		if *name != "" || len(fs.Args()) > 0 || *pickFlag {
			fmt.Fprintln(os.Stderr, "relevo: --done clears DONE bindings; do not also name one or pass --pick")
			return exitCodeErr{code: 2}
		}
		return runGC(*delete, *dryRun, *mastermindRef, *allMasterMinds)
	}

	if *pickFlag {
		if err := pickNamesNothing(*name, fs.Args()); err != nil {
			return err
		}
		return runPick(pick.Options{Verb: pick.VerbUnbind, Archive: *archive})
	}

	target, ok := explicitBinding(*name, fs.Args())
	if !ok {
		return fmt.Errorf("usage: relevo unbind <name> | --pick [--archive]  (or --name <name>)%s\n"+
			"unbind removes a binding; it will not guess which one you meant", bindingHint("unbind"))
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	res, err := relevo.Unbind(context.Background(), rt, target, *archive)
	if err != nil {
		return err
	}

	fmt.Println(relevo.UnbindText(target, res))
	warnWaitingOnYou(rt, target)

	return nil
}

func runSweep(dryRun bool) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}

	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	res, err := relevo.SweepRefs(context.Background(), rt, dir, dryRun)
	if err != nil {
		return err
	}

	if len(res.Refs) == 0 {
		fmt.Printf("no relevo refs in %s\n", dir)
		return nil
	}

	for _, line := range relevo.RefLines(res.Refs) {
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

// runGC is gc's body (the old cmdGC), now reached through `unbind --done`
// (§4.3). It clears the calling mastermind's DONE bindings, or, with
// --all-masterminds, every mastermind's (#482).
func runGC(delete, dryRun bool, mastermindRef string, all bool) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}

	opts, err := gcScope(mastermindRef, all, gcResolver(rt))
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return exitCodeErr{code: 2}
	}
	opts.Delete = delete
	opts.DryRun = dryRun

	done, err := relevo.GC(context.Background(), rt, opts)
	if err != nil {
		return err
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
		lbl := label(r.MasterMindID)
		switch {
		case dryRun:
			wtMsg := ""
			if r.WorktreeRemoved != "" {
				wtMsg = fmt.Sprintf(" (worktree %s would be removed)", r.WorktreeRemoved)
			} else if r.WorktreeKept != "" {
				wtMsg = fmt.Sprintf(" (worktree %s kept: %s)", r.WorktreeKept, r.KeptReason)
			} else if r.WorktreeGone != "" {
				wtMsg = fmt.Sprintf(" (worktree %s already gone)", r.WorktreeGone)
			}
			fmt.Printf("would clear %-10s %-14s %s (%d rounds)%s\n", r.Name, "["+lbl+"]", r.CWD, r.Rounds, wtMsg)
			for _, line := range relevo.RefLines(r.Refs) {
				fmt.Printf("            %s\n", line)
			}
		case r.Archived:
			fmt.Printf("archived    %-10s [%s]\n", r.Name, lbl)
			if r.WorktreeRemoved != "" {
				fmt.Printf("            removed worktree %s\n", r.WorktreeRemoved)
			} else if r.WorktreeKept != "" {
				fmt.Printf("            kept worktree %s (%s)\n              remove by hand: git -C %s worktree remove %s\n",
					r.WorktreeKept, r.KeptReason, r.WorktreeKept, r.WorktreeKept)
			} else if r.WorktreeGone != "" {
				fmt.Printf("            worktree %s was already gone\n", r.WorktreeGone)
			}
			for _, line := range relevo.RefLines(r.Refs) {
				fmt.Printf("            %s\n", line)
			}
		default:
			fmt.Printf("deleted     %-10s [%s] %s (%d rounds)\n", r.Name, lbl, r.CWD, r.Rounds)
			if r.WorktreeRemoved != "" {
				fmt.Printf("            removed worktree %s\n", r.WorktreeRemoved)
			} else if r.WorktreeKept != "" {
				fmt.Printf("            kept worktree %s (%s)\n              remove by hand: git -C %s worktree remove %s\n",
					r.WorktreeKept, r.KeptReason, r.WorktreeKept, r.WorktreeKept)
			} else if r.WorktreeGone != "" {
				fmt.Printf("            worktree %s was already gone\n", r.WorktreeGone)
			}
			for _, line := range relevo.RefLines(r.Refs) {
				fmt.Printf("            %s\n", line)
			}
		}
	}

	if dryRun {
		fmt.Printf("\n%d binding(s) would be cleared; re-run without --dry-run\n", len(done))
	}

	return nil
}

// gcResolver is gcScope's production resolve function: the same input
// mastermindFilter (mastermind.go:517) builds, plus Flag, closed over rt. It never
// calls mastermind.Init: unbind --done never registers a mastermind (#482).
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
			Now:             now,
			CWD:             cwd,
			OpencodeSession: rt.OpencodeSession,
		})
		return rec, err
	}
}

// mastermindLabel is a GC result line's mastermind field (#482): "(none)" for "",
// the record's Name when the registry has it, else the id itself.
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
