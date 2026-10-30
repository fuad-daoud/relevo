package relevo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// The tests here drive the tmp sweep: the rounds an open record still owns,
// the checks a live row still names, the loose directories that have outlived
// any process, and the confined root that makes every removal safe.

// tmpSweepSetup is a runtime naming an empty tmp root -- the root the sweep
// walks but the store never made, which is exactly what a crashed process
// leaves for it.
func tmpSweepSetup(t *testing.T) Runtime {
	t.Helper()
	rt := newRuntime(t)
	rt.TmpRoot = t.TempDir()
	return rt
}

// tmpMkDir plants one directory with its own mtime, the way a leftover looks
// to the sweep: the name and the clock are all it reads.
func tmpMkDir(t *testing.T, path string, mod time.Time) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	return path
}

func tmpMkFile(t *testing.T, path string, mod time.Time) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	return path
}

// tmpMustBeThere pins that the sweep passed a path by: files and planted
// links alike, whatever survives has to still be where it was.
func tmpMustBeThere(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("%s is gone: %v", path, err)
	}
}

// tmpSweep runs the sweep on the store's own binding list, fixed to baseTime.
func tmpSweep(t *testing.T, rt Runtime) ([]string, error) {
	t.Helper()
	bindings, err := rt.Store.List()
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	return SweepTmp(context.Background(), rt, bindings, baseTime)
}

func tmpRoundDir(t *testing.T, rt Runtime, name string, round int, mod time.Time) string {
	t.Helper()
	return tmpMkDir(t, filepath.Join(rt.TmpRoot, roundTmpRel(name, round)), mod)
}

func tmpCheckDir(t *testing.T, rt Runtime, unit string, mod time.Time) string {
	t.Helper()
	return tmpMkDir(t, filepath.Join(rt.TmpRoot, checkTmpRel(unit)), mod)
}

func tmpSaveBinding(t *testing.T, rt Runtime, b store.Binding) {
	t.Helper()
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save binding %s: %v", b.Name, err)
	}
}

func tmpAppendLog(t *testing.T, rt Runtime, name string, e store.LogEntry) {
	t.Helper()
	if err := rt.Store.AppendLog(name, e); err != nil {
		t.Fatalf("append log for %s: %v", name, err)
	}
}

func tmpPrompt(t *testing.T, rt Runtime, name string, round int) {
	t.Helper()
	tmpAppendLog(t, rt, name, store.LogEntry{TS: baseTime, Round: round, Direction: store.DirToBuilder, Kind: store.KindPrompt})
}

func tmpReport(t *testing.T, rt Runtime, name string, round int) {
	t.Helper()
	tmpAppendLog(t, rt, name, store.LogEntry{TS: baseTime, Round: round, Direction: store.DirToMasterMind, Kind: store.KindReport})
}

// tmpWantRemoved pins the sweep's report: exactly these paths, nothing else.
func tmpWantRemoved(t *testing.T, rt Runtime, removed []string, rels ...string) {
	t.Helper()
	want := make([]string, 0, len(rels))
	for _, rel := range rels {
		want = append(want, filepath.Join(rt.TmpRoot, rel))
	}
	slices.Sort(want)
	if !slices.Equal(removed, want) {
		t.Errorf("removed = %v, want %v", removed, want)
	}
}

func tmpPlantChain(t *testing.T, rt Runtime, c db.ChainRow, rows ...db.ChainCheckRow) {
	t.Helper()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("plant chain %s: %v", c.Name, err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		for _, r := range rows {
			if err := tx.ChainCheckPut(c.Name, r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("plant chain checks for %s: %v", c.Name, err)
	}
}

func TestTmpRoundKeptRule(t *testing.T) {
	t.Parallel()

	live := store.Binding{Name: "shop", Round: 3, State: store.StateActive}
	done := store.Binding{Name: "shop", Round: 3, State: store.StateDone}
	cases := []struct {
		name  string
		b     *store.Binding
		open  bool
		round int
		want  bool
	}{
		{"a missing binding keeps nothing", nil, true, 3, false},
		{"a DONE binding keeps nothing", &done, true, 3, false},
		{"a DONE binding's later round is no exception", &done, false, 9, false},
		{"the open current round is kept", &live, true, 3, true},
		{"the closed current round goes", &live, false, 3, false},
		{"a round ahead of the current one is kept", &live, false, 4, true},
		{"a closed round behind the current one goes", &live, true, 2, false},
	}
	for _, tc := range cases {
		if got := tmpRoundKept(tc.b, tc.open, tc.round); got != tc.want {
			t.Errorf("%s: tmpRoundKept = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTmpCheckKeptRule(t *testing.T) {
	t.Parallel()

	if !tmpCheckKept(true, baseTime.Add(-time.Hour), baseTime) {
		t.Error("an open unit must be kept at any age")
	}
	if !tmpCheckKept(false, baseTime.Add(-9*time.Minute), baseTime) {
		t.Error("a fresh entry must survive the guard even with no row naming it")
	}
	if tmpCheckKept(false, baseTime.Add(-11*time.Minute), baseTime) {
		t.Error("an old entry no row names must go")
	}
}

func TestTmpLooseRemovableRule(t *testing.T) {
	t.Parallel()

	old := baseTime.Add(-25 * time.Hour)
	cases := []struct {
		name   string
		isDir  bool
		sub    string
		newest time.Time
		want   bool
	}{
		{"an old directory goes", true, "stale", old, true},
		{"at exactly the age it goes", true, "stale", baseTime.Add(-tmpLooseAge), true},
		{"a minute short of the age it stays", true, "stale", baseTime.Add(-tmpLooseAge + time.Minute), false},
		{"an old regular file is never touched", false, "req-body-7", old, false},
		{"the rounds name belongs to the round rule", true, "rounds", old, false},
		{"the checks name belongs to the check rule", true, "checks", old, false},
	}
	for _, tc := range cases {
		if got := tmpLooseRemovable(tc.isDir, tc.sub, tc.newest, baseTime); got != tc.want {
			t.Errorf("%s: tmpLooseRemovable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTmpOpenCheckUnits(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	bindings := []store.Binding{
		{Name: "gatey", Owner: "o", Round: 2, GateRun: &store.GateRun{PID: 5, Round: 2}},
		{Name: "chk", Owner: "o", Round: 1, CheckRun: &store.CheckRun{ID: "x", Command: "true", Round: 1}},
		{Name: "settled", Owner: "o", Round: 1, CheckRun: &store.CheckRun{ID: "y", Command: "true", Round: 1, Result: "pass"}},
		{Name: "idle", Owner: "o", Round: 1},
	}
	chainChecks := []tmpOpenChainCheck{{chain: db.ChainRow{Owner: "o", Name: "shop"}, run: 4}}

	units := tmpOpenCheckUnits(rt, bindings, chainChecks)
	live := []string{
		scopeUnitNameFor(scopeGate, "o", "gatey", 2, ""),
		scopeUnitNameFor(scopeGate, "o", "chk-check", 1, ""),
		scopeUnitNameFor(scopeGate, "o", "shop-check", 4, ""),
	}
	for _, unit := range live {
		for _, want := range []string{unit, housekeep.ShortCheckID(unit)} {
			if !units[want] {
				t.Errorf("unit %q missing from the open set", want)
			}
		}
	}
	for _, unit := range []string{
		scopeUnitNameFor(scopeGate, "o", "settled-check", 1, ""),
		scopeUnitNameFor(scopeGate, "o", "idle", 1, ""),
	} {
		for _, no := range []string{unit, housekeep.ShortCheckID(unit)} {
			if units[no] {
				t.Errorf("unit %q in the open set, want it out", no)
			}
		}
	}
}

// TestSweepTmpKeepsLiveLongChecks pins the pre-change spelling: a checks/
// entry under the bare unit for a live run is kept at any age, while an aged
// bare-unit entry for a settled run still goes.
func TestSweepTmpKeepsLiveLongChecks(t *testing.T) {
	t.Parallel()

	rt := tmpSweepSetup(t)
	gateB := store.Binding{Name: "gatey", CWD: "/repo/gatey", Owner: "o", Round: 3, State: store.StateActive,
		GateRun: &store.GateRun{PID: 7, StartedAt: baseTime.Unix(), Round: 3, Command: "make check"}}
	chkB := store.Binding{Name: "chk", CWD: "/repo/chk", Owner: "o", Round: 2, State: store.StateActive,
		CheckRun: &store.CheckRun{ID: "c1", Command: "make check", Round: 2}}
	doneB := store.Binding{Name: "done", CWD: "/repo/done", Owner: "o", Round: 1, State: store.StateDone,
		CheckRun: &store.CheckRun{ID: "c9", Command: "make check", Round: 1, Result: "pass"}}
	tmpSaveBinding(t, rt, gateB)
	tmpSaveBinding(t, rt, chkB)
	tmpSaveBinding(t, rt, doneB)
	chain := db.ChainRow{ID: db.NewID(), Name: "shop", Status: string(workflow.StatusRunning),
		Phase: "build", Step: "checking", Plan: 1, Plans: 1, Builder: "shop", Worktree: "/repo/shop", CreatedAt: baseTime, UpdatedAt: baseTime}
	tmpPlantChain(t, rt, chain, db.ChainCheckRow{Run: 1, Step: "check", Command: "make check", CreatedAt: baseTime})
	stopped := db.ChainRow{ID: db.NewID(), Name: "old", Status: string(workflow.StatusStopped),
		Phase: "build", Step: "checking", Plan: 1, Plans: 1, Builder: "old", Worktree: "/repo/old", CreatedAt: baseTime, UpdatedAt: baseTime}
	tmpPlantChain(t, rt, stopped, db.ChainCheckRow{Run: 1, Step: "check", Command: "make check", CreatedAt: baseTime})

	old := baseTime.Add(-20 * time.Minute)
	gateUnit := scopeUnitNameFor(scopeGate, gateB.Owner, gateB.Name, gateB.Round, "")
	gatePath := tmpMkDir(t, filepath.Join(rt.TmpRoot, tmpSubChecks, gateUnit), old)
	chkUnit := servedCheckSpec(rt, chkB, *chkB.CheckRun).UnitName
	chkPath := tmpMkDir(t, filepath.Join(rt.TmpRoot, tmpSubChecks, chkUnit), old)
	chainUnit := chainCheckSpec(rt, chain, 1, "", "").UnitName
	chainPath := tmpMkDir(t, filepath.Join(rt.TmpRoot, tmpSubChecks, chainUnit), old)
	doneUnit := servedCheckSpec(rt, doneB, *doneB.CheckRun).UnitName
	donePath := tmpMkDir(t, filepath.Join(rt.TmpRoot, tmpSubChecks, doneUnit), old)
	stoppedUnit := chainCheckSpec(rt, stopped, 1, "", "").UnitName
	stoppedPath := tmpMkDir(t, filepath.Join(rt.TmpRoot, tmpSubChecks, stoppedUnit), old)

	removed, err := tmpSweep(t, rt)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	tmpWantRemoved(t, rt, removed, filepath.Join("checks", doneUnit), filepath.Join("checks", stoppedUnit))
	mustExist(t, gatePath)
	mustExist(t, chkPath)
	mustExist(t, chainPath)
	mustBeGone(t, donePath)
	mustBeGone(t, stoppedPath)
}

func TestSweepTmpKeepsOpenRoundRemovesClosed(t *testing.T) {
	t.Parallel()

	rt := tmpSweepSetup(t)
	tmpSaveBinding(t, rt, store.Binding{Name: "shop", CWD: "/repo/shop", Round: 2, State: store.StateActive})
	tmpPrompt(t, rt, "shop", 1)
	tmpReport(t, rt, "shop", 1)
	tmpPrompt(t, rt, "shop", 2)

	closed := tmpRoundDir(t, rt, "shop", 1, baseTime.Add(-time.Hour))
	open := tmpRoundDir(t, rt, "shop", 2, baseTime.Add(-time.Hour))

	removed, err := tmpSweep(t, rt)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	tmpWantRemoved(t, rt, removed, roundTmpRel("shop", 1))
	mustBeGone(t, closed)
	mustExist(t, open)
}

func TestSweepTmpRemovesRoundOfDoneOrMissingBinding(t *testing.T) {
	t.Parallel()

	rt := tmpSweepSetup(t)
	tmpSaveBinding(t, rt, store.Binding{Name: "fin", CWD: "/repo/fin", Round: 1, State: store.StateDone})
	tmpPrompt(t, rt, "fin", 1)

	done := tmpRoundDir(t, rt, "fin", 1, baseTime.Add(-time.Hour))
	missing := tmpRoundDir(t, rt, "ghost", 1, baseTime.Add(-time.Hour))

	removed, err := tmpSweep(t, rt)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	// A DONE binding loses its round entry even while the log still says the
	// round is open: done is the record's last word.
	tmpWantRemoved(t, rt, removed, roundTmpRel("fin", 1), roundTmpRel("ghost", 1))
	mustBeGone(t, done)
	mustBeGone(t, missing)
}

func TestSweepTmpKeepsRoundAheadOfCurrent(t *testing.T) {
	t.Parallel()

	rt := tmpSweepSetup(t)
	tmpSaveBinding(t, rt, store.Binding{Name: "adv", CWD: "/repo/adv", Round: 1, State: store.StateActive})

	current := tmpRoundDir(t, rt, "adv", 1, baseTime) // no prompt logged yet: not open
	ahead := tmpRoundDir(t, rt, "adv", 2, baseTime)

	removed, err := tmpSweep(t, rt)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	// Send makes the directory before it logs the plan; a round ahead of the
	// binding's current one is that send in flight, so it is kept unread.
	tmpWantRemoved(t, rt, removed, roundTmpRel("adv", 1))
	mustBeGone(t, current)
	mustExist(t, ahead)
}

func TestSweepTmpKeepsOpenChecksOfEachKind(t *testing.T) {
	t.Parallel()

	rt := tmpSweepSetup(t)
	gateB := store.Binding{Name: "gatey", CWD: "/repo/gatey", Owner: "o", Round: 3, State: store.StateActive,
		GateRun: &store.GateRun{PID: 7, StartedAt: baseTime.Unix(), Round: 3, Command: "make check"}}
	chkB := store.Binding{Name: "chk", CWD: "/repo/chk", Owner: "o", Round: 2, State: store.StateActive,
		CheckRun: &store.CheckRun{ID: "c1", Command: "make check", Round: 2}}
	tmpSaveBinding(t, rt, gateB)
	tmpSaveBinding(t, rt, chkB)
	tmpSaveBinding(t, rt, store.Binding{Name: "shop", CWD: "/repo/shop", Owner: "o", Round: 1, State: store.StateActive})
	chain := db.ChainRow{ID: db.NewID(), Name: "shop", Status: string(workflow.StatusRunning),
		Phase: "build", Step: "checking", Plan: 1, Plans: 1, Builder: "shop", Worktree: "/repo/shop", CreatedAt: baseTime, UpdatedAt: baseTime}
	tmpPlantChain(t, rt, chain, db.ChainCheckRow{Run: 1, Step: "check", Command: "make check", CreatedAt: baseTime})

	old := baseTime.Add(-20 * time.Minute)
	gateUnit := tmpCheckDir(t, rt, scopeUnitNameFor(scopeGate, gateB.Owner, gateB.Name, gateB.Round, ""), old)
	chkUnit := tmpCheckDir(t, rt, servedCheckSpec(rt, chkB, *chkB.CheckRun).UnitName, old)
	chainUnit := tmpCheckDir(t, rt, chainCheckSpec(rt, chain, 1, "", "").UnitName, old)
	stale := tmpCheckDir(t, rt, scopeUnitNameFor(scopeGate, "o", "nobody", 9, ""), old)

	removed, err := tmpSweep(t, rt)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	tmpWantRemoved(t, rt, removed, filepath.Join("checks", filepath.Base(stale)))
	mustExist(t, gateUnit)
	mustExist(t, chkUnit)
	mustExist(t, chainUnit)
	mustBeGone(t, stale)
}

func TestSweepTmpKeepsFreshUnopenedCheck(t *testing.T) {
	t.Parallel()

	rt := tmpSweepSetup(t)

	// The directory exists the moment the process is prepared; its row lands
	// only after the start is recorded. A sweep reading inside that window
	// must keep the entry on its freshness alone.
	fresh := tmpCheckDir(t, rt, scopeUnitNameFor(scopeGate, "o", "early", 1, ""), baseTime.Add(-time.Minute))
	aged := tmpCheckDir(t, rt, scopeUnitNameFor(scopeGate, "o", "late", 1, ""), baseTime.Add(-11*time.Minute))

	removed, err := tmpSweep(t, rt)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	tmpWantRemoved(t, rt, removed, filepath.Join("checks", filepath.Base(aged)))
	mustExist(t, fresh)
	mustBeGone(t, aged)
}

// TestSweepTmpRemovesCheckOfStoppedChain pins the killed-chain leftover: a
// check a stop or an abandon killed never passes the gate runner's advance,
// so its directory outlived the run and no open record names it any more.
func TestSweepTmpRemovesCheckOfStoppedChain(t *testing.T) {
	t.Parallel()

	rt := tmpSweepSetup(t)
	tmpSaveBinding(t, rt, store.Binding{Name: "shop", CWD: "/repo/shop", Owner: "o", Round: 1, State: store.StateActive})
	chain := db.ChainRow{ID: db.NewID(), Name: "shop", Status: string(workflow.StatusStopped),
		Phase: "build", Step: "checking", Plan: 1, Plans: 1, Builder: "shop", Worktree: "/repo/shop", CreatedAt: baseTime, UpdatedAt: baseTime}
	tmpPlantChain(t, rt, chain, db.ChainCheckRow{Run: 1, Step: "check", Command: "make check", CreatedAt: baseTime})

	killed := tmpCheckDir(t, rt, chainCheckSpec(rt, chain, 1, "", "").UnitName, baseTime.Add(-20*time.Minute))

	removed, err := tmpSweep(t, rt)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	tmpWantRemoved(t, rt, removed, filepath.Join("checks", filepath.Base(killed)))
	mustBeGone(t, killed)
}

func TestSweepTmpLooseOnlyOldDirectories(t *testing.T) {
	t.Parallel()

	rt := tmpSweepSetup(t)
	old := baseTime.Add(-25 * time.Hour)

	stale := tmpMkDir(t, filepath.Join(rt.TmpRoot, "olddir"), old)
	file := tmpMkFile(t, filepath.Join(rt.TmpRoot, "oldfile.txt"), old)
	bundle := tmpMkFile(t, filepath.Join(rt.TmpRoot, "req-body-7"), old)

	// A directory whose newest child is fresh was touched as recently as that
	// child: whatever works in there, works now.
	freshKid := tmpMkDir(t, filepath.Join(rt.TmpRoot, "freshkid"), old)
	child := filepath.Join(freshKid, "out.log")
	if err := os.WriteFile(child, []byte("still writing"), 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}
	if err := os.Chtimes(freshKid, old, old); err != nil {
		t.Fatalf("chtimes parent: %v", err)
	}

	almost := tmpMkDir(t, filepath.Join(rt.TmpRoot, "almost"), baseTime.Add(-23*time.Hour-59*time.Minute))
	rounds := tmpMkDir(t, filepath.Join(rt.TmpRoot, "rounds"), old)

	removed, err := tmpSweep(t, rt)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	tmpWantRemoved(t, rt, removed, "olddir")
	mustBeGone(t, stale)
	tmpMustBeThere(t, file)
	tmpMustBeThere(t, bundle)
	mustExist(t, freshKid)
	mustExist(t, almost)
	mustExist(t, rounds)
}

// TestSweepTmpCapsLooseRemovals pins the per-sweep unlinking budget: a root
// that aged out entirely is emptied over several sweeps, not inside the one
// tick that noticed it.
func TestSweepTmpCapsLooseRemovals(t *testing.T) {
	t.Parallel()

	rt := tmpSweepSetup(t)
	old := baseTime.Add(-25 * time.Hour)

	const planted = 300
	dirs := make([]string, 0, planted)
	for i := range planted {
		dirs = append(dirs, tmpMkDir(t, filepath.Join(rt.TmpRoot, fmt.Sprintf("stale-%03d", i)), old))
	}

	removed, err := tmpSweep(t, rt)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	if len(removed) != tmpSweepMaxRemovals {
		t.Fatalf("first sweep removed %d entries, want the cap of %d", len(removed), tmpSweepMaxRemovals)
	}
	for _, path := range removed {
		mustBeGone(t, path)
	}
	left := plantedDirs(t, rt.TmpRoot)
	if left != planted-tmpSweepMaxRemovals {
		t.Fatalf("%d loose directories left, want %d", left, planted-tmpSweepMaxRemovals)
	}

	removed, err = tmpSweep(t, rt)
	if err != nil {
		t.Fatalf("second SweepTmp: %v", err)
	}
	if len(removed) != planted-tmpSweepMaxRemovals {
		t.Errorf("second sweep removed %d entries, want the remaining %d", len(removed), planted-tmpSweepMaxRemovals)
	}
	for _, path := range dirs {
		mustBeGone(t, path)
	}
}

// plantedDirs counts the directories sitting directly in the root: the test
// plants nothing under the two names the per-record rules own, so every
// directory there is a loose one.
func plantedDirs(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}

// TestSweepTmpNeverTouchesOutsideRoot pins the confinement a tenant depends
// on: an entry swapped for a symlink leads nowhere outside, and a subdirectory
// swapped for one cannot even be listed, so its leftovers survive untouched.
func TestSweepTmpNeverTouchesOutsideRoot(t *testing.T) {
	t.Parallel()

	rt := tmpSweepSetup(t)
	outside := t.TempDir()

	victim := filepath.Join(outside, "victim")
	victimFile := tmpMkFile(t, filepath.Join(victim, "data.txt"), baseTime.Add(-time.Hour))
	if err := os.MkdirAll(filepath.Join(rt.TmpRoot, "rounds"), 0o700); err != nil {
		t.Fatalf("mkdir rounds: %v", err)
	}
	if err := os.Symlink(victim, filepath.Join(rt.TmpRoot, "rounds", "ghost-001")); err != nil {
		t.Fatalf("plant round symlink: %v", err)
	}

	escaped := filepath.Join(outside, "escaped")
	escapedCheck := tmpMkDir(t, filepath.Join(escaped, "relevo-gate-o-gone-9"), baseTime.Add(-time.Hour))
	if err := os.Symlink(escaped, filepath.Join(rt.TmpRoot, "checks")); err != nil {
		t.Fatalf("plant checks symlink: %v", err)
	}

	_, err := tmpSweep(t, rt)
	if err == nil {
		t.Fatal("SweepTmp = nil error, want the escaped checks listing refused")
	}
	tmpMustBeThere(t, victimFile)
	mustExist(t, escapedCheck)
}

func TestSweepTmpOneFailureDoesNotStopOthers(t *testing.T) {
	t.Parallel()

	rt := tmpSweepSetup(t)
	old := baseTime.Add(-25 * time.Hour)

	bad := tmpMkDir(t, filepath.Join(rt.TmpRoot, "oldbad"), old)
	tmpMkFile(t, filepath.Join(bad, "child"), old)
	if err := os.Chtimes(bad, old, old); err != nil {
		t.Fatalf("chtimes bad: %v", err)
	}
	// A directory that cannot give up its children: the removal fails here and
	// only here.
	if err := os.Chmod(bad, 0o500); err != nil {
		t.Fatalf("chmod bad: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o700) })

	good := tmpMkDir(t, filepath.Join(rt.TmpRoot, "oldgood"), old)
	ghost := tmpRoundDir(t, rt, "ghost", 1, old)

	removed, err := tmpSweep(t, rt)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("SweepTmp err = %v, want it to join the permission refusal", err)
	}
	tmpWantRemoved(t, rt, removed, roundTmpRel("ghost", 1), "oldgood")
	mustBeGone(t, ghost)
	mustBeGone(t, good)
	mustExist(t, bad)
}

// TestTickRunsTmpSweep pins the hook: the sweep runs on a daemon's first tick,
// before Tick's no-bindings early return, off the tick's own binding list.
func TestTickRunsTmpSweep(t *testing.T) {
	t.Parallel()

	rt := tmpSweepSetup(t)
	ghost := tmpRoundDir(t, rt, "ghost", 1, baseTime.Add(-time.Hour))

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	mustBeGone(t, ghost)
}

// TestTickPacesTmpSweep pins the sweep's own clock: the first tick of a
// process walks the root, later ticks wait out tmpSweepEvery on the runtime's
// clock rather than paying for the walk every tick, and the next tick past the
// interval walks again.
func TestTickPacesTmpSweep(t *testing.T) {
	t.Parallel()

	rt := tmpSweepSetup(t)
	now := baseTime
	rt.Now = func() time.Time { return now }
	d := NewDaemon(rt, time.Second)

	old := baseTime.Add(-time.Hour)
	ghost := tmpRoundDir(t, rt, "ghost", 1, old)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	mustBeGone(t, ghost)

	// A second entry inside the interval: the tick that must not sweep also
	// must not remove.
	kept := tmpRoundDir(t, rt, "ghost", 1, old)
	now = baseTime.Add(tmpSweepEvery - time.Minute)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	mustExist(t, kept)

	now = baseTime.Add(tmpSweepEvery)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	mustBeGone(t, kept)
}

// TestTickSkipsTmpSweepWithEmptyRoot pins the empty-root skip: a runtime with
// no tmp root has nothing to sweep, and the tick touches nothing -- not even
// a same-named tree under its own working directory.
func TestTickSkipsTmpSweepWithEmptyRoot(t *testing.T) {
	sandbox := t.TempDir()
	decoy := tmpMkDir(t, filepath.Join(sandbox, "rounds", "here-001"), baseTime.Add(-time.Hour))

	rt := newRuntime(t)
	if rt.TmpRoot != "" {
		t.Fatalf("TmpRoot = %q, want the empty value a test runtime names", rt.TmpRoot)
	}
	t.Chdir(sandbox)

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	mustExist(t, decoy)
	if removed, err := SweepTmp(context.Background(), rt, nil, baseTime); removed != nil || err != nil {
		t.Errorf("SweepTmp with no tmp root = (%v, %v), want (nil, nil)", removed, err)
	}
}
