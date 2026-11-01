package housekeep

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
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// The tmp root is where a crashed or killed process leaves its private temp
// directory: every route that ends a process removes its own, so only a kill
// the route never reaches leaves one. The sweep is the backstop, deciding from
// the records alone: it never opens a leftover's contents or asks a process
// whether it is done.

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

// SweepEvery paces the sweep: one walk of a full root costs thousands of syscalls, too many for every tick to pay.
const SweepEvery = 10 * time.Minute

// SweepMaxRemovals bounds one sweep's unlinking so a first sweep over an aged root cannot hog a single tick.
const SweepMaxRemovals = 256

// SweepTmp removes the leftovers under deps.TmpRoot: round directories whose
// binding is gone, DONE, or whose round is not open; check directories no live
// record names; and up to SweepMaxRemovals loose directories untouched for the
// whole age window. bindings is the caller's own binding list, so the sweep
// decides from the same snapshot its tick reconciles. now is the clock, passed
// in so the rules are testable without one. Removed paths come back sorted;
// every failure is per entry and joined after the whole walk.
func SweepTmp(_ context.Context, deps Deps, bindings []store.Binding, now time.Time) ([]string, error) {
	if deps.TmpRoot == "" {
		return nil, nil
	}
	var errs []error
	removed, err := sweepRoundDirs(deps, bindings)
	if err != nil {
		errs = append(errs, err)
	}
	checked, err := sweepCheckDirs(deps, bindings, now)
	if err != nil {
		errs = append(errs, err)
	}
	loose, err := sweepLooseDirs(deps, now, SweepMaxRemovals)
	if err != nil {
		errs = append(errs, err)
	}
	removed = append(append(removed, checked...), loose...)
	sort.Strings(removed)
	return removed, errors.Join(errs...)
}

// SweepDue is the pacing decision: a zero last run means this daemon has
// never swept, and after that the walk is due once SweepEvery has passed
// on the runtime's own clock.
func SweepDue(last, now time.Time) bool {
	return last.IsZero() || !now.Before(last.Add(SweepEvery))
}

// SweepTick is the sweep on the tick's own binding list and clock. A deps with
// no tmp root has nothing to sweep and is skipped. The walk reads the whole
// root, so the hook runs it at most once per SweepEvery per daemon, on the
// daemon's own first tick and never on a package-wide timer. last carries the
// pace across ticks: the caller hands its own last sweep in and reads the
// attempt back.
func SweepTick(ctx context.Context, deps Deps, bindings []store.Binding, last *time.Time, now time.Time) {
	if deps.TmpRoot == "" {
		return
	}
	if !SweepDue(*last, now) {
		return
	}
	// The pace records the attempt rather than a clean sweep: the walk logs
	// its failures per entry and returns, so a root that resists is walked
	// again on the interval, not every tick.
	*last = now
	removed, err := SweepTmp(ctx, deps, bindings, now)
	if err != nil {
		slog.Warn("tmp sweep", "err", err)
	}
	for _, path := range removed {
		slog.Info("removed tmp entry", "path", path)
	}
}

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
// store that may hold a hundred records. Short entries match the expected set
// derived
// through the same helpers the prepare and remove routes use; long entries
// still parse and keep the same rule, so they self-clean without a separate
// migration. An entry whose round cannot be decided is kept: the sweep removes
// what it is sure about, and the next tick reads again.
func sweepRoundDirs(deps Deps, bindings []store.Binding) ([]string, error) {
	entries, err := deps.Store.ListTmpEntries(deps.TmpRoot, SubRounds)
	if err != nil {
		return nil, fmt.Errorf("tmp sweep: list %s: %w", SubRounds, err)
	}
	byName := make(map[string]store.Binding, len(bindings))
	for _, b := range bindings {
		byName[b.Name] = b
	}
	logged := map[string][]store.LogEntry{}
	expected, experr := expectedRoundDirs(deps, bindings, logged)
	var errs []error
	if experr != nil {
		errs = append(errs, experr)
	}
	var removed []string
	for _, e := range entries {
		path, drop, rerr := sweepOneRoundDir(deps, byName, logged, expected, e)
		if rerr != nil {
			errs = append(errs, rerr)
			continue
		}
		if !drop {
			continue
		}
		removed = append(removed, path)
	}
	return removed, errors.Join(errs...)
}

// expectedRoundDirs holds the short entry names a live binding still owns:
// its open current round, and the next round a send may be preparing before
// it logs the plan.
func expectedRoundDirs(deps Deps, bindings []store.Binding, logged map[string][]store.LogEntry) (map[string]bool, error) {
	expected := make(map[string]bool, len(bindings)*2)
	var errs []error
	for _, b := range bindings {
		if b.State == store.StateDone {
			continue
		}
		next := RoundTmpRel(b.Name, b.Round+1)
		logEntries, seen := logged[b.Name]
		if !seen {
			var rerr error
			logEntries, rerr = deps.Store.ReadLog(b.Name)
			if rerr != nil {
				errs = append(errs, fmt.Errorf("tmp sweep: rounds/%s: %w", b.Name, rerr))
				expected[RoundTmpRel(b.Name, b.Round)] = true
				expected[next] = true
				continue
			}
			logged[b.Name] = logEntries
		}
		expected[next] = true
		if store.RoundOpen(logEntries, b.Round) {
			expected[RoundTmpRel(b.Name, b.Round)] = true
		}
	}
	return expected, errors.Join(errs...)
}

// sweepOneRoundDir decides one rounds/ entry against the expected set, parsing
// long names to their binding and round.
func sweepOneRoundDir(deps Deps, byName map[string]store.Binding, logged map[string][]store.LogEntry, expected map[string]bool, e store.TmpEntry) (string, bool, error) {
	if expected[filepath.Join(SubRounds, e.Name)] {
		return "", false, nil
	}
	if IsShortTmpID(e.Name) {
		path, rerr := removeTmpEntry(deps, filepath.Join(SubRounds, e.Name))
		if rerr != nil {
			return "", false, rerr
		}
		return path, true, nil
	}
	name, round, ok := parseScratchName(e.Name)
	if !ok {
		return "", false, nil
	}
	var b *store.Binding
	if found, foundOK := byName[name]; foundOK {
		copied := found
		b = &copied
	}
	open := false
	if b != nil && b.State != store.StateDone && round == b.Round {
		logEntries, seen := logged[name]
		if !seen {
			var rerr error
			logEntries, rerr = deps.Store.ReadLog(name)
			if rerr != nil {
				return "", false, fmt.Errorf("tmp sweep: rounds/%s: %w", e.Name, rerr)
			}
			logged[name] = logEntries
		}
		open = store.RoundOpen(logEntries, round)
	}
	if tmpRoundKept(b, open, round) {
		return "", false, nil
	}
	path, rerr := removeTmpEntry(deps, filepath.Join(SubRounds, e.Name))
	if rerr != nil {
		return "", false, rerr
	}
	return path, true, nil
}

// tmpCheckKept is the checks rule given the open set's answer and the entry's
// own mtime: a unit a live record still names is kept, and so is any entry
// touched inside the fresh guard.
func tmpCheckKept(unitOpen bool, modTime, now time.Time) bool {
	return unitOpen || now.Sub(modTime) < tmpFreshGuard
}

// sweepCheckDirs walks checks/ and removes every entry no live record names
// and the fresh guard does not cover.
func sweepCheckDirs(deps Deps, bindings []store.Binding, now time.Time) ([]string, error) {
	entries, err := deps.Store.ListTmpEntries(deps.TmpRoot, SubChecks)
	if err != nil {
		return nil, fmt.Errorf("tmp sweep: list %s: %w", SubChecks, err)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	// A chain walk that fails leaves the open set provably incomplete, and
	// every entry this rule would take is a check: the sweep removes nothing
	// on a partial answer and lets the next tick read again.
	chainChecks, err := tmpChainCheckRuns(deps)
	if err != nil {
		return nil, fmt.Errorf("tmp sweep: collect chain checks: %w", err)
	}
	open := tmpOpenCheckUnits(deps, bindings, chainChecks)

	var errs []error
	var removed []string
	for _, e := range entries {
		if tmpCheckKept(open[e.Name], e.ModTime, now) {
			continue
		}
		path, err := removeTmpEntry(deps, filepath.Join(SubChecks, e.Name))
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
// the stop and the abandon kill their runs' processes without passing the
// gate runner's advance, so the directories those checks left behind are
// nothing's to keep. Runs are allocated contiguously, so the walk stops at the
// first run the chain has not recorded.
func tmpChainCheckRuns(deps Deps) ([]tmpOpenChainCheck, error) {
	chains, err := deps.Store.Chains()
	if err != nil {
		return nil, err
	}
	var runs []tmpOpenChainCheck
	if err := deps.Store.WithLock(func(tx *store.Tx) error {
		for _, c := range chains {
			if deps.ChainFlowTerminal(workflow.Status(c.Status)) {
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
				if deps.ChainCheckSettled(row) {
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

// tmpOpenCheckUnits is the keep-set for checks/ in both spellings, built from
// the same spec helpers that make the directories. Each unit contributes the
// bare unit alongside its ShortCheckID hash, so a live process's leftover is
// kept by either name, while a settled row contributes neither spelling and
// still self-cleans.
func tmpOpenCheckUnits(deps Deps, bindings []store.Binding, chainChecks []tmpOpenChainCheck) map[string]bool {
	units := make(map[string]bool, 2*(len(bindings)+len(chainChecks)))
	for _, b := range bindings {
		if b.GateRun != nil {
			unit := deps.GateUnit(b.Owner, b.Name, b.Round)
			units[unit] = true
			units[ShortCheckID(unit)] = true
		}
		if b.CheckRun != nil && !b.CheckRun.Settled() {
			unit := deps.ServedCheckUnit(b, *b.CheckRun)
			units[unit] = true
			units[ShortCheckID(unit)] = true
		}
	}
	for _, cc := range chainChecks {
		unit := deps.ChainCheckUnit(cc.chain, cc.run)
		units[unit] = true
		units[ShortCheckID(unit)] = true
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
	if !isDir || name == SubRounds || name == SubChecks {
		return false
	}
	return !now.Before(newest.Add(tmpLooseAge))
}

// sweepLooseDirs walks the root itself and removes the stale directories, at
// most max of them: a root that aged out entirely is emptied over several
// sweeps rather than in the one tick that noticed it. The newest touch leads,
// so a directory whose children cannot be listed is left alone rather than
// judged by its own mtime alone.
func sweepLooseDirs(deps Deps, now time.Time, max int) ([]string, error) {
	entries, err := os.ReadDir(deps.TmpRoot)
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
		newest, merr := tmpNewestTouch(filepath.Join(deps.TmpRoot, e.Name()), e)
		if merr != nil {
			errs = append(errs, fmt.Errorf("tmp sweep: %s: %w", e.Name(), merr))
			continue
		}
		if !tmpLooseRemovable(true, e.Name(), newest, now) {
			continue
		}
		path, err := removeTmpEntry(deps, e.Name())
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
func removeTmpEntry(deps Deps, rel string) (string, error) {
	if err := deps.Store.RemoveTmpEntry(deps.TmpRoot, rel); err != nil {
		return "", fmt.Errorf("tmp sweep: remove %s: %w", rel, err)
	}
	return filepath.Join(deps.TmpRoot, rel), nil
}
