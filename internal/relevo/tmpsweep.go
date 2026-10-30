package relevo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// The tmp root is the one place a crashed or killed process leaves something
// behind: its private temp directory. Every route that ends a process removes
// its own, but a kill the route never reaches -- a crash between the prepare
// and the record, a chain stopped beside its running check -- leaves the
// directory, and the state tmp is the default every process now writes to.
// The sweep is the backstop, and it decides from the records alone: it never
// opens a leftover's contents and never asks a process whether it is done.

const (
	// tmpFreshGuard is how long a checks/ entry outlives every record. A
	// check's directory is created when its process is prepared and the row
	// that names the unit lands only after the start is recorded; a sweep
	// reading between the two would take a live process's temp directory out
	// from under it.
	tmpFreshGuard = 10 * time.Minute

	// tmpLooseAge is how long a loose directory must have sat untouched before
	// the sweep takes it: longer than a round's whole budget, so the age rule
	// alone never catches a tree a live process still works in.
	tmpLooseAge = 24 * time.Hour
)

// tmpSweepEvery paces the sweep: one walk of a full root costs thousands of syscalls, too many for every tick to pay.
const tmpSweepEvery = 10 * time.Minute

// tmpSweepMaxRemovals bounds one sweep's unlinking so a first sweep over an aged root cannot hog a single tick.
const tmpSweepMaxRemovals = 256

// tmpSubRounds and tmpSubChecks are the two subdirectories of a runtime's tmp
// root the per-process routes own; their names are the rules that made them in
// roundTmpRel and checkTmpRel.
const (
	tmpSubRounds = "rounds"
	tmpSubChecks = "checks"
)

// SweepTmp removes the leftovers under rt.TmpRoot: round directories whose
// binding is gone, DONE, or whose round is no longer open; check directories
// that no live record names any more; and up to tmpSweepMaxRemovals loose
// directories nobody has touched for the whole age window, the rest waiting
// for a later sweep. bindings is the caller's own binding list, so the sweep
// decides from the same snapshot its tick is reconciling rather than a second
// read that could disagree with it. now is the clock, passed in so the rules
// are testable without one.
//
// The removed paths are returned sorted. A runtime with no tmp root has
// nothing under it and the sweep answers empty. Every failure is per entry:
// errors are joined after the whole walk, so one bad entry never stops the
// rest, and a listing or collection that fails skips only its own rule.
func SweepTmp(ctx context.Context, rt Runtime, bindings []store.Binding, now time.Time) ([]string, error) {
	if rt.TmpRoot == "" {
		return nil, nil
	}
	var errs []error
	removed, err := sweepRoundDirs(rt, bindings)
	if err != nil {
		errs = append(errs, err)
	}
	checked, err := sweepCheckDirs(rt, bindings, now)
	if err != nil {
		errs = append(errs, err)
	}
	loose, err := sweepLooseDirs(rt, now, tmpSweepMaxRemovals)
	if err != nil {
		errs = append(errs, err)
	}
	removed = append(append(removed, checked...), loose...)
	sort.Strings(removed)
	return removed, errors.Join(errs...)
}

// sweepTmpTick is the daemon's hook: the sweep on the tick's own binding list
// and clock, every removal logged the way every other sweep logs its work. A
// runtime with no tmp root -- container mode, any host that names none -- has
// nothing to sweep and is skipped. The walk reads the whole root, so the hook
// runs it at most once per tmpSweepEvery per daemon, on the daemon's own
// first tick and never on a package-wide timer.
func (d *Daemon) sweepTmpTick(ctx context.Context, bindings []store.Binding) {
	rt := d.rt
	if rt.TmpRoot == "" {
		return
	}
	now := rt.Now()
	if !d.tmpSweepDue(now) {
		return
	}
	// The pace records the attempt rather than a clean sweep: the walk logs
	// its failures per entry and returns, so a root that resists is walked
	// again on the interval, not every tick.
	d.tmpSweptAt = now
	removed, err := SweepTmp(ctx, rt, bindings, now)
	if err != nil {
		slog.Warn("tmp sweep", "err", err)
	}
	for _, path := range removed {
		slog.Info("removed tmp entry", "path", path)
	}
}

// tmpSweepDue is the pacing decision: a zero last run means this daemon has
// never swept, and after that the walk is due once tmpSweepEvery has passed
// on the runtime's own clock.
func (d *Daemon) tmpSweepDue(now time.Time) bool {
	return d.tmpSweptAt.IsZero() || !now.Before(d.tmpSweptAt.Add(tmpSweepEvery))
}

// TmpSweptAt and SetTmpSweptAt carry the sweep's pace across Daemons: serve
// builds a fresh Daemon per owner on every tick, so it keeps each owner's last
// sweep itself and hands it back to the next tick's Daemon.
func (d *Daemon) TmpSweptAt() time.Time { return d.tmpSweptAt }

func (d *Daemon) SetTmpSweptAt(t time.Time) { d.tmpSweptAt = t }

// tmpRoundKept is the rounds rule given the facts the sweep read: the binding
// the entry names, or nil when no binding does, and whether the log says that
// round is open. A round ahead of the binding's current one is kept without
// the log being consulted at all: send makes the directory before it logs the
// plan, and the scratch sweep keeps the same entries by the same rule.
func tmpRoundKept(b *store.Binding, open bool, round int) bool {
	if b == nil || b.State == store.StateDone {
		return false
	}
	if round > b.Round {
		return true
	}
	return round == b.Round && open
}

// sweepRoundDirs walks rounds/ and removes what the rule condemns. A binding's
// log is read once per binding: the daemon ticks every few seconds over a
// store that may hold a hundred records, and most ticks own no tmp entry at
// all. Short entries match the expected set the sweep derives through the
// same helpers the prepare and remove routes use; pre-change long entries
// still parse and keep the old rule, so they self-clean without a separate
// migration. An entry whose round cannot be decided -- an unreadable log,
// an unparseable name -- is kept: the sweep removes what it is sure about,
// and the next tick reads again.
func sweepRoundDirs(rt Runtime, bindings []store.Binding) ([]string, error) {
	entries, err := rt.Store.ListTmpEntries(rt.TmpRoot, tmpSubRounds)
	if err != nil {
		return nil, fmt.Errorf("tmp sweep: list %s: %w", tmpSubRounds, err)
	}
	byName := make(map[string]store.Binding, len(bindings))
	for _, b := range bindings {
		byName[b.Name] = b
	}
	logged := map[string][]store.LogEntry{}

	// expected holds the short entry names a live binding still owns: its
	// open current round, and the next round a send may be preparing before
	// it logs the plan.
	expected := make(map[string]bool, len(bindings)*2)
	var errs []error
	for _, b := range bindings {
		if b.State == store.StateDone {
			continue
		}
		next := roundTmpRel(b.Name, b.Round+1)
		logEntries, seen := logged[b.Name]
		if !seen {
			var rerr error
			logEntries, rerr = rt.Store.ReadLog(b.Name)
			if rerr != nil {
				errs = append(errs, fmt.Errorf("tmp sweep: rounds/%s: %w", b.Name, rerr))
				expected[roundTmpRel(b.Name, b.Round)] = true
				expected[next] = true
				continue
			}
			logged[b.Name] = logEntries
		}
		expected[next] = true
		if store.RoundOpen(logEntries, b.Round) {
			expected[roundTmpRel(b.Name, b.Round)] = true
		}
	}

	var removed []string
	for _, e := range entries {
		if expected[filepath.Join(tmpSubRounds, e.Name)] {
			continue
		}
		if housekeep.IsShortTmpID(e.Name) {
			path, rerr := removeTmpEntry(rt, filepath.Join(tmpSubRounds, e.Name))
			if rerr != nil {
				errs = append(errs, rerr)
				continue
			}
			removed = append(removed, path)
			continue
		}
		name, round, ok := parseScratchName(e.Name)
		if !ok {
			continue
		}
		var b *store.Binding
		if found, foundOK := byName[name]; foundOK {
			b = &found
		}
		open := false
		if b != nil && b.State != store.StateDone && round == b.Round {
			logEntries, seen := logged[name]
			if !seen {
				var rerr error
				logEntries, rerr = rt.Store.ReadLog(name)
				if rerr != nil {
					errs = append(errs, fmt.Errorf("tmp sweep: rounds/%s: %w", e.Name, rerr))
					continue
				}
				logged[name] = logEntries
			}
			open = store.RoundOpen(logEntries, round)
		}
		if tmpRoundKept(b, open, round) {
			continue
		}
		path, rerr := removeTmpEntry(rt, filepath.Join(tmpSubRounds, e.Name))
		if rerr != nil {
			errs = append(errs, rerr)
			continue
		}
		removed = append(removed, path)
	}
	return removed, errors.Join(errs...)
}

// tmpCheckKept is the checks rule given the open set's answer and the entry's
// own mtime: a unit a live record still names is kept, and so is any entry
// touched inside the fresh guard.
func tmpCheckKept(unitOpen bool, modTime, now time.Time) bool {
	return unitOpen || now.Sub(modTime) < tmpFreshGuard
}

// sweepCheckDirs walks checks/ and removes every entry no live record names
// and the fresh guard does not cover.
func sweepCheckDirs(rt Runtime, bindings []store.Binding, now time.Time) ([]string, error) {
	entries, err := rt.Store.ListTmpEntries(rt.TmpRoot, tmpSubChecks)
	if err != nil {
		return nil, fmt.Errorf("tmp sweep: list %s: %w", tmpSubChecks, err)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	// A chain walk that fails leaves the open set provably incomplete, and
	// every entry this rule would take is a check: the sweep removes nothing
	// on a partial answer and lets the next tick read again.
	chainChecks, err := tmpChainCheckRuns(rt)
	if err != nil {
		return nil, fmt.Errorf("tmp sweep: collect chain checks: %w", err)
	}
	open := tmpOpenCheckUnits(rt, bindings, chainChecks)

	var errs []error
	var removed []string
	for _, e := range entries {
		if tmpCheckKept(open[e.Name], e.ModTime, now) {
			continue
		}
		path, err := removeTmpEntry(rt, filepath.Join(tmpSubChecks, e.Name))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, path)
	}
	return removed, errors.Join(errs...)
}

// tmpOpenChainCheck is one unsettled chain check run: the chain's row and the
// run number together name the gate unit, and so the directory, the run owns.
type tmpOpenChainCheck struct {
	chain db.ChainRow
	run   int
}

// tmpChainCheckRuns collects the unsettled check rows of the chains that have
// not ended, under one state lock. A terminal chain is left out on purpose:
// the stop and the abandon kill their runs' processes but never pass the gate
// runner's advance, so the directory a killed chain check left behind is not
// anything's to keep -- removing it is this sweep's whole reason to walk the
// chains. Runs are allocated contiguously, so the walk stops at the first run
// the chain has not recorded.
func tmpChainCheckRuns(rt Runtime) ([]tmpOpenChainCheck, error) {
	chains, err := rt.Store.Chains()
	if err != nil {
		return nil, err
	}
	var runs []tmpOpenChainCheck
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, c := range chains {
			if chainFlowTerminal(workflow.Status(c.Status)) {
				continue
			}
			for run := 1; ; run++ {
				row, err := tx.ChainCheck(c.Name, run)
				if errors.Is(err, store.ErrNotFound) {
					break
				}
				if err != nil {
					return err
				}
				if chainCheckSettled(row) {
					continue
				}
				runs = append(runs, tmpOpenChainCheck{chain: c, run: run})
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return runs, nil
}

// tmpOpenCheckUnits is the keep-set for checks/ in both spellings, built
// from the same spec helpers that make the directories: a binding's running
// gate process, its unsettled served check, and each unsettled run of a
// chain that has not ended. Each unit contributes the bare unit -- the
// pre-change leaf -- alongside its ShortCheckID hash -- the leaf prepareTmp
// and removeCheckTmp use -- so a live process's leftover is kept by either
// name, while a settled row contributes neither spelling and still
// self-cleans.
func tmpOpenCheckUnits(rt Runtime, bindings []store.Binding, chainChecks []tmpOpenChainCheck) map[string]bool {
	units := make(map[string]bool, 2*(len(bindings)+len(chainChecks)))
	for _, b := range bindings {
		if b.GateRun != nil {
			unit := scopeUnitNameFor(scopeGate, b.Owner, b.Name, b.Round, "")
			units[unit] = true
			units[housekeep.ShortCheckID(unit)] = true
		}
		if b.CheckRun != nil && !b.CheckRun.Settled() {
			unit := servedCheckSpec(rt, b, *b.CheckRun).UnitName
			units[unit] = true
			units[housekeep.ShortCheckID(unit)] = true
		}
	}
	for _, cc := range chainChecks {
		unit := chainCheckSpec(rt, cc.chain, cc.run, "", "").UnitName
		units[unit] = true
		units[housekeep.ShortCheckID(unit)] = true
	}
	return units
}

// tmpLooseRemovable is the rule for one direct child of the root: only a
// directory, never the two subdirectories the other rules own, and only when
// nothing between the directory itself and its direct children has been
// touched for the whole age window. Regular files are never touched: the root
// holds live transport bundles, request and plan bodies, and the database
// library's own temp files, and anything else is nobody's to remove.
func tmpLooseRemovable(isDir bool, name string, newest, now time.Time) bool {
	if !isDir || name == tmpSubRounds || name == tmpSubChecks {
		return false
	}
	return !now.Before(newest.Add(tmpLooseAge))
}

// sweepLooseDirs walks the root itself and removes the stale directories, at
// most max of them: a root that aged out entirely is emptied over several
// sweeps rather than in the one tick that noticed it. The newest touch is read
// before the rule is asked, so a directory whose children cannot be listed is
// left alone rather than judged by its own mtime alone.
func sweepLooseDirs(rt Runtime, now time.Time, max int) ([]string, error) {
	entries, err := os.ReadDir(rt.TmpRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("tmp sweep: list root: %w", err)
	}

	var removed []string
	var errs []error
	for _, e := range entries {
		if len(removed) >= max {
			break
		}
		if !e.IsDir() {
			continue
		}
		newest, merr := tmpNewestTouch(filepath.Join(rt.TmpRoot, e.Name()), e)
		if merr != nil {
			errs = append(errs, fmt.Errorf("tmp sweep: %s: %w", e.Name(), merr))
			continue
		}
		if !tmpLooseRemovable(true, e.Name(), newest, now) {
			continue
		}
		path, err := removeTmpEntry(rt, e.Name())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, path)
	}
	return removed, errors.Join(errs...)
}

// tmpNewestTouch is the newest mtime among the directory and its direct
// children: a process that holds a file open and keeps writing shows up as a
// fresh child, not as a fresh directory. A child's own info is its lstat, so a
// planted symlink counts as itself and never as its target's clock.
func tmpNewestTouch(dir string, self fs.DirEntry) (time.Time, error) {
	info, err := self.Info()
	if err != nil {
		return time.Time{}, err
	}
	newest := info.ModTime()
	children, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}, err
	}
	for _, c := range children {
		ci, cerr := c.Info()
		if cerr != nil {
			return time.Time{}, cerr
		}
		if ci.ModTime().After(newest) {
			newest = ci.ModTime()
		}
	}
	return newest, nil
}

// removeTmpEntry takes one entry away through the store's confined root -- a
// tenant-planted symlink leads nowhere, and every path the sweep can name is
// inside TmpRoot by construction -- and answers the absolute path it removed.
func removeTmpEntry(rt Runtime, rel string) (string, error) {
	if err := rt.Store.RemoveTmpEntry(rt.TmpRoot, rel); err != nil {
		return "", fmt.Errorf("tmp sweep: remove %s: %w", rel, err)
	}
	return filepath.Join(rt.TmpRoot, rel), nil
}
