package housekeep

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// The tests here drive the tmp sweep: the rounds an open record still owns,
// the checks a live row still names, the loose directories that have outlived
// any process, and the confined root that makes every removal safe.

// testSafeUnitPart replaces every rune outside [A-Za-z0-9:_.-] with '-',
// matching the engine's scope unit sanitising so stub units are byte-identical
// to the ones the sweep meets in production.
func testSafeUnitPart(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ':', r == '_', r == '.', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	return sb.String()
}

func testScopeOwner8(owner string) string {
	if owner == "" {
		return "local"
	}
	if dir, ok := remote.ClientID(owner).Dir(); ok {
		return dir[:8]
	}
	return "unknown"
}

func testScopeUnit(owner, name string, round int) string {
	return "relevo-gate-" + testScopeOwner8(owner) + "-" + testSafeUnitPart(name) + "-" + strconv.Itoa(round)
}

func testParseScratchName(entry string) (string, int, bool) {
	i := strings.LastIndex(entry, "-")
	if i <= 0 || i == len(entry)-1 {
		return "", 0, false
	}
	name := entry[:i]
	digits := entry[i+1:]
	for j := 0; j < len(digits); j++ {
		if digits[j] < '0' || digits[j] > '9' {
			return "", 0, false
		}
	}
	round, err := strconv.Atoi(digits)
	if err != nil {
		return "", 0, false
	}
	return name, round, true
}

func testSweepDeps(st *store.Store, root string) Deps {
	return Deps{
		Store:             st,
		TmpRoot:           root,
		ChainCheckSettled: func(r db.ChainCheckRow) bool { return r.Result != "" && r.Result != "starting" },
		ChainFlowTerminal: func(s workflow.Status) bool {
			return s == workflow.StatusHalted || s == workflow.StatusStopped || s == workflow.StatusDone
		},
		GateUnit: func(owner, name string, round int) string {
			return testScopeUnit(owner, name, round)
		},
		ServedCheckUnit: func(b store.Binding, run store.CheckRun) string {
			return testScopeUnit(b.Owner, b.Name+"-check", run.Round)
		},
		ChainCheckUnit: func(c db.ChainRow, run int) string {
			return testScopeUnit(c.Owner, c.Name+"-check", run)
		},
		ParseScratchName: testParseScratchName,
	}
}

// tmpSweepSetup is a deps naming an empty tmp root -- the root the sweep
// walks but the store never made, which is exactly what a crashed process
// leaves for it.
func tmpSweepSetup(t *testing.T) Deps {
	t.Helper()
	return testSweepDeps(store.New(t.TempDir()), t.TempDir())
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

// tmpMustBeThere pins that the sweep passed a path by: files and planted
// links alike, whatever survives has to still be where it was.
func tmpMustBeThere(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("%s is gone: %v", path, err)
	}
}

func mustExist(t *testing.T, dir string) {
	t.Helper()
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Errorf("want directory %s to exist (stat err %v)", dir, err)
	}
}

func mustBeGone(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("want %s gone, stat err = %v", dir, err)
	}
}

// tmpSweep runs the sweep on the store's own binding list, fixed to baseTime.
func tmpSweep(t *testing.T, deps Deps) ([]string, error) {
	t.Helper()
	bindings, err := deps.Store.List()
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	return SweepTmp(context.Background(), deps, bindings, baseTime)
}

func tmpRoundDir(t *testing.T, deps Deps, name string, round int, mod time.Time) string {
	t.Helper()
	return tmpMkDir(t, filepath.Join(deps.TmpRoot, RoundTmpRel(name, round)), mod)
}

func tmpCheckDir(t *testing.T, deps Deps, unit string, mod time.Time) string {
	t.Helper()
	return tmpMkDir(t, filepath.Join(deps.TmpRoot, SubChecks, ShortCheckID(unit)), mod)
}

func tmpSaveBinding(t *testing.T, deps Deps, b store.Binding) {
	t.Helper()
	if err := deps.Store.Save(b); err != nil {
		t.Fatalf("save binding %s: %v", b.Name, err)
	}
}

func tmpAppendLog(t *testing.T, deps Deps, name string, e store.LogEntry) {
	t.Helper()
	if err := deps.Store.AppendLog(name, e); err != nil {
		t.Fatalf("append log for %s: %v", name, err)
	}
}

func tmpPrompt(t *testing.T, deps Deps, name string, round int) {
	t.Helper()
	tmpAppendLog(t, deps, name, store.LogEntry{TS: baseTime, Round: round, Direction: store.DirToBuilder, Kind: store.KindPrompt})
}

func tmpReport(t *testing.T, deps Deps, name string, round int) {
	t.Helper()
	tmpAppendLog(t, deps, name, store.LogEntry{TS: baseTime, Round: round, Direction: store.DirToMasterMind, Kind: store.KindReport})
}

// tmpWantRemoved pins the sweep's report: exactly these paths, nothing else.
func tmpWantRemoved(t *testing.T, deps Deps, removed []string, rels ...string) {
	t.Helper()
	want := make([]string, 0, len(rels))
	for _, rel := range rels {
		want = append(want, filepath.Join(deps.TmpRoot, rel))
	}
	slices.Sort(want)
	if !slices.Equal(removed, want) {
		t.Errorf("removed = %v, want %v", removed, want)
	}
}

func tmpPlantChain(t *testing.T, deps Deps, c db.ChainRow, rows ...db.ChainCheckRow) {
	t.Helper()
	if err := deps.Store.WithLock(func(tx *store.Tx) error {
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("plant chain %s: %v", c.Name, err)
	}
	if err := deps.Store.WithLock(func(tx *store.Tx) error {
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

	deps := tmpSweepSetup(t)
	bindings := []store.Binding{
		{Name: "gatey", Owner: "o", Round: 2, GateRun: &store.GateRun{PID: 5, Round: 2}},
		{Name: "chk", Owner: "o", Round: 1, CheckRun: &store.CheckRun{ID: "x", Command: "true", Round: 1}},
		{Name: "settled", Owner: "o", Round: 1, CheckRun: &store.CheckRun{ID: "y", Command: "true", Round: 1, Result: "pass"}},
		{Name: "idle", Owner: "o", Round: 1},
	}
	chainChecks := []tmpOpenChainCheck{{chain: db.ChainRow{Owner: "o", Name: "shop"}, run: 4}}

	units := tmpOpenCheckUnits(deps, bindings, chainChecks)
	live := []string{
		deps.GateUnit("o", "gatey", 2),
		deps.ServedCheckUnit(bindings[1], *bindings[1].CheckRun),
		deps.ChainCheckUnit(chainChecks[0].chain, 4),
	}
	for _, unit := range live {
		for _, want := range []string{unit, ShortCheckID(unit)} {
			if !units[want] {
				t.Errorf("unit %q missing from the open set", want)
			}
		}
	}
	settledUnit := deps.ServedCheckUnit(bindings[2], *bindings[2].CheckRun)
	for _, unit := range []string{
		settledUnit,
		deps.GateUnit("o", "idle", 1),
	} {
		for _, no := range []string{unit, ShortCheckID(unit)} {
			if units[no] {
				t.Errorf("unit %q in the open set, want it out", no)
			}
		}
	}
}

// TestSweepTmpKeepsLiveLongChecks pins the long spelling: a checks/
// entry under the bare unit for a live run is kept at any age, while an aged
// bare-unit entry for a settled run still goes.
func TestSweepTmpKeepsLiveLongChecks(t *testing.T) {
	t.Parallel()

	deps := tmpSweepSetup(t)
	gateB := store.Binding{Name: "gatey", CWD: "/repo/gatey", Owner: "o", Round: 3, State: store.StateActive,
		GateRun: &store.GateRun{PID: 7, StartedAt: baseTime.Unix(), Round: 3, Command: "make check"}}
	chkB := store.Binding{Name: "chk", CWD: "/repo/chk", Owner: "o", Round: 2, State: store.StateActive,
		CheckRun: &store.CheckRun{ID: "c1", Command: "make check", Round: 2}}
	doneB := store.Binding{Name: "done", CWD: "/repo/done", Owner: "o", Round: 1, State: store.StateDone,
		CheckRun: &store.CheckRun{ID: "c9", Command: "make check", Round: 1, Result: "pass"}}
	tmpSaveBinding(t, deps, gateB)
	tmpSaveBinding(t, deps, chkB)
	tmpSaveBinding(t, deps, doneB)
	chain := db.ChainRow{ID: db.NewID(), Name: "shop", Status: string(workflow.StatusRunning),
		Phase: "build", Step: "checking", Plan: 1, Plans: 1, Builder: "shop", Worktree: "/repo/shop", CreatedAt: baseTime, UpdatedAt: baseTime}
	tmpPlantChain(t, deps, chain, db.ChainCheckRow{Run: 1, Step: "check", Command: "make check", CreatedAt: baseTime})
	stopped := db.ChainRow{ID: db.NewID(), Name: "old", Status: string(workflow.StatusStopped),
		Phase: "build", Step: "checking", Plan: 1, Plans: 1, Builder: "old", Worktree: "/repo/old", CreatedAt: baseTime, UpdatedAt: baseTime}
	tmpPlantChain(t, deps, stopped, db.ChainCheckRow{Run: 1, Step: "check", Command: "make check", CreatedAt: baseTime})

	old := baseTime.Add(-20 * time.Minute)
	gateUnit := deps.GateUnit(gateB.Owner, gateB.Name, gateB.Round)
	gatePath := tmpMkDir(t, filepath.Join(deps.TmpRoot, SubChecks, gateUnit), old)
	chkUnit := deps.ServedCheckUnit(chkB, *chkB.CheckRun)
	chkPath := tmpMkDir(t, filepath.Join(deps.TmpRoot, SubChecks, chkUnit), old)
	chainUnit := deps.ChainCheckUnit(chain, 1)
	chainPath := tmpMkDir(t, filepath.Join(deps.TmpRoot, SubChecks, chainUnit), old)
	doneUnit := deps.ServedCheckUnit(doneB, *doneB.CheckRun)
	donePath := tmpMkDir(t, filepath.Join(deps.TmpRoot, SubChecks, doneUnit), old)
	stoppedUnit := deps.ChainCheckUnit(stopped, 1)
	stoppedPath := tmpMkDir(t, filepath.Join(deps.TmpRoot, SubChecks, stoppedUnit), old)

	removed, err := tmpSweep(t, deps)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	tmpWantRemoved(t, deps, removed, filepath.Join("checks", doneUnit), filepath.Join("checks", stoppedUnit))
	mustExist(t, gatePath)
	mustExist(t, chkPath)
	mustExist(t, chainPath)
	mustBeGone(t, donePath)
	mustBeGone(t, stoppedPath)
}

func TestSweepTmpKeepsOpenRoundRemovesClosed(t *testing.T) {
	t.Parallel()

	deps := tmpSweepSetup(t)
	tmpSaveBinding(t, deps, store.Binding{Name: "shop", CWD: "/repo/shop", Round: 2, State: store.StateActive})
	tmpPrompt(t, deps, "shop", 1)
	tmpReport(t, deps, "shop", 1)
	tmpPrompt(t, deps, "shop", 2)

	closed := tmpRoundDir(t, deps, "shop", 1, baseTime.Add(-time.Hour))
	open := tmpRoundDir(t, deps, "shop", 2, baseTime.Add(-time.Hour))

	removed, err := tmpSweep(t, deps)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	tmpWantRemoved(t, deps, removed, RoundTmpRel("shop", 1))
	mustBeGone(t, closed)
	mustExist(t, open)
}

func TestSweepTmpRemovesRoundOfDoneOrMissingBinding(t *testing.T) {
	t.Parallel()

	deps := tmpSweepSetup(t)
	tmpSaveBinding(t, deps, store.Binding{Name: "fin", CWD: "/repo/fin", Round: 1, State: store.StateDone})
	tmpPrompt(t, deps, "fin", 1)

	done := tmpRoundDir(t, deps, "fin", 1, baseTime.Add(-time.Hour))
	missing := tmpRoundDir(t, deps, "ghost", 1, baseTime.Add(-time.Hour))

	removed, err := tmpSweep(t, deps)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	// A DONE binding loses its round entry even while the log still says the
	// round is open: done is the record's last word.
	tmpWantRemoved(t, deps, removed, RoundTmpRel("fin", 1), RoundTmpRel("ghost", 1))
	mustBeGone(t, done)
	mustBeGone(t, missing)
}

func TestSweepTmpKeepsRoundAheadOfCurrent(t *testing.T) {
	t.Parallel()

	deps := tmpSweepSetup(t)
	tmpSaveBinding(t, deps, store.Binding{Name: "adv", CWD: "/repo/adv", Round: 1, State: store.StateActive})

	current := tmpRoundDir(t, deps, "adv", 1, baseTime) // no prompt logged yet: not open
	ahead := tmpRoundDir(t, deps, "adv", 2, baseTime)

	removed, err := tmpSweep(t, deps)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	// Send makes the directory before it logs the plan; a round ahead of the
	// binding's current one is that send in flight, so it is kept unread.
	tmpWantRemoved(t, deps, removed, RoundTmpRel("adv", 1))
	mustBeGone(t, current)
	mustExist(t, ahead)
}

func TestSweepTmpKeepsOpenChecksOfEachKind(t *testing.T) {
	t.Parallel()

	deps := tmpSweepSetup(t)
	gateB := store.Binding{Name: "gatey", CWD: "/repo/gatey", Owner: "o", Round: 3, State: store.StateActive,
		GateRun: &store.GateRun{PID: 7, StartedAt: baseTime.Unix(), Round: 3, Command: "make check"}}
	chkB := store.Binding{Name: "chk", CWD: "/repo/chk", Owner: "o", Round: 2, State: store.StateActive,
		CheckRun: &store.CheckRun{ID: "c1", Command: "make check", Round: 2}}
	tmpSaveBinding(t, deps, gateB)
	tmpSaveBinding(t, deps, chkB)
	tmpSaveBinding(t, deps, store.Binding{Name: "shop", CWD: "/repo/shop", Owner: "o", Round: 1, State: store.StateActive})
	chain := db.ChainRow{ID: db.NewID(), Name: "shop", Status: string(workflow.StatusRunning),
		Phase: "build", Step: "checking", Plan: 1, Plans: 1, Builder: "shop", Worktree: "/repo/shop", CreatedAt: baseTime, UpdatedAt: baseTime}
	tmpPlantChain(t, deps, chain, db.ChainCheckRow{Run: 1, Step: "check", Command: "make check", CreatedAt: baseTime})

	old := baseTime.Add(-20 * time.Minute)
	gateUnit := tmpCheckDir(t, deps, deps.GateUnit(gateB.Owner, gateB.Name, gateB.Round), old)
	chkUnit := tmpCheckDir(t, deps, deps.ServedCheckUnit(chkB, *chkB.CheckRun), old)
	chainUnit := tmpCheckDir(t, deps, deps.ChainCheckUnit(chain, 1), old)
	stale := tmpCheckDir(t, deps, testScopeUnit("o", "nobody", 9), old)

	removed, err := tmpSweep(t, deps)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	tmpWantRemoved(t, deps, removed, filepath.Join("checks", filepath.Base(stale)))
	mustExist(t, gateUnit)
	mustExist(t, chkUnit)
	mustExist(t, chainUnit)
	mustBeGone(t, stale)
}

func TestSweepTmpKeepsFreshUnopenedCheck(t *testing.T) {
	t.Parallel()

	deps := tmpSweepSetup(t)

	// The directory exists the moment the process is prepared; its row lands
	// only after the start is recorded. A sweep reading inside that window
	// must keep the entry on its freshness alone.
	fresh := tmpCheckDir(t, deps, testScopeUnit("o", "early", 1), baseTime.Add(-time.Minute))
	aged := tmpCheckDir(t, deps, testScopeUnit("o", "late", 1), baseTime.Add(-11*time.Minute))

	removed, err := tmpSweep(t, deps)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	tmpWantRemoved(t, deps, removed, filepath.Join("checks", filepath.Base(aged)))
	mustExist(t, fresh)
	mustBeGone(t, aged)
}

// TestSweepTmpRemovesCheckOfStoppedChain pins the killed-chain leftover: a
// check a stop or an abandon killed never passes the gate runner's advance,
// so its directory outlived the run and no open record names it any more.
func TestSweepTmpRemovesCheckOfStoppedChain(t *testing.T) {
	t.Parallel()

	deps := tmpSweepSetup(t)
	tmpSaveBinding(t, deps, store.Binding{Name: "shop", CWD: "/repo/shop", Owner: "o", Round: 1, State: store.StateActive})
	chain := db.ChainRow{ID: db.NewID(), Name: "shop", Status: string(workflow.StatusStopped),
		Phase: "build", Step: "checking", Plan: 1, Plans: 1, Builder: "shop", Worktree: "/repo/shop", CreatedAt: baseTime, UpdatedAt: baseTime}
	tmpPlantChain(t, deps, chain, db.ChainCheckRow{Run: 1, Step: "check", Command: "make check", CreatedAt: baseTime})

	killed := tmpCheckDir(t, deps, deps.ChainCheckUnit(chain, 1), baseTime.Add(-20*time.Minute))

	removed, err := tmpSweep(t, deps)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	tmpWantRemoved(t, deps, removed, filepath.Join("checks", filepath.Base(killed)))
	mustBeGone(t, killed)
}

func TestSweepTmpLooseOnlyOldDirectories(t *testing.T) {
	t.Parallel()

	deps := tmpSweepSetup(t)
	old := baseTime.Add(-25 * time.Hour)

	stale := tmpMkDir(t, filepath.Join(deps.TmpRoot, "olddir"), old)
	file := tmpMkFile(t, filepath.Join(deps.TmpRoot, "oldfile.txt"), old)
	bundle := tmpMkFile(t, filepath.Join(deps.TmpRoot, "req-body-7"), old)

	// A directory whose newest child is fresh was touched as recently as that
	// child: whatever works in there, works now.
	freshKid := tmpMkDir(t, filepath.Join(deps.TmpRoot, "freshkid"), old)
	child := filepath.Join(freshKid, "out.log")
	if err := os.WriteFile(child, []byte("still writing"), 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}
	if err := os.Chtimes(freshKid, old, old); err != nil {
		t.Fatalf("chtimes parent: %v", err)
	}

	almost := tmpMkDir(t, filepath.Join(deps.TmpRoot, "almost"), baseTime.Add(-23*time.Hour-59*time.Minute))
	rounds := tmpMkDir(t, filepath.Join(deps.TmpRoot, "rounds"), old)

	removed, err := tmpSweep(t, deps)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	tmpWantRemoved(t, deps, removed, "olddir")
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

	deps := tmpSweepSetup(t)
	old := baseTime.Add(-25 * time.Hour)

	const planted = 300
	dirs := make([]string, 0, planted)
	for i := range planted {
		dirs = append(dirs, tmpMkDir(t, filepath.Join(deps.TmpRoot, fmt.Sprintf("stale-%03d", i)), old))
	}

	removed, err := tmpSweep(t, deps)
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	if len(removed) != SweepMaxRemovals {
		t.Fatalf("first sweep removed %d entries, want the cap of %d", len(removed), SweepMaxRemovals)
	}
	for _, path := range removed {
		mustBeGone(t, path)
	}
	left := plantedDirs(t, deps.TmpRoot)
	if left != planted-SweepMaxRemovals {
		t.Fatalf("%d loose directories left, want %d", left, planted-SweepMaxRemovals)
	}

	removed, err = tmpSweep(t, deps)
	if err != nil {
		t.Fatalf("second SweepTmp: %v", err)
	}
	if len(removed) != planted-SweepMaxRemovals {
		t.Errorf("second sweep removed %d entries, want the remaining %d", len(removed), planted-SweepMaxRemovals)
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

	deps := tmpSweepSetup(t)
	outside := t.TempDir()

	victim := filepath.Join(outside, "victim")
	victimFile := tmpMkFile(t, filepath.Join(victim, "data.txt"), baseTime.Add(-time.Hour))
	if err := os.MkdirAll(filepath.Join(deps.TmpRoot, "rounds"), 0o700); err != nil {
		t.Fatalf("mkdir rounds: %v", err)
	}
	if err := os.Symlink(victim, filepath.Join(deps.TmpRoot, "rounds", "ghost-001")); err != nil {
		t.Fatalf("plant round symlink: %v", err)
	}

	escaped := filepath.Join(outside, "escaped")
	escapedCheck := tmpMkDir(t, filepath.Join(escaped, "relevo-gate-o-gone-9"), baseTime.Add(-time.Hour))
	if err := os.Symlink(escaped, filepath.Join(deps.TmpRoot, "checks")); err != nil {
		t.Fatalf("plant checks symlink: %v", err)
	}

	_, err := tmpSweep(t, deps)
	if err == nil {
		t.Fatal("SweepTmp = nil error, want the escaped checks listing refused")
	}
	tmpMustBeThere(t, victimFile)
	mustExist(t, escapedCheck)
}

func TestSweepTmpOneFailureDoesNotStopOthers(t *testing.T) {
	t.Parallel()

	deps := tmpSweepSetup(t)
	old := baseTime.Add(-25 * time.Hour)

	bad := tmpMkDir(t, filepath.Join(deps.TmpRoot, "oldbad"), old)
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

	good := tmpMkDir(t, filepath.Join(deps.TmpRoot, "oldgood"), old)
	ghost := tmpRoundDir(t, deps, "ghost", 1, old)

	removed, err := tmpSweep(t, deps)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("SweepTmp err = %v, want it to join the permission refusal", err)
	}
	tmpWantRemoved(t, deps, removed, RoundTmpRel("ghost", 1), "oldgood")
	mustBeGone(t, ghost)
	mustBeGone(t, good)
	mustExist(t, bad)
}

func TestSweepDuePacesWalks(t *testing.T) {
	t.Parallel()

	if SweepDue(time.Time{}, baseTime) != true {
		t.Error("a zero last run sweeps: a daemon's first tick walks")
	}
	last := baseTime
	if SweepDue(last, baseTime.Add(SweepEvery-time.Minute)) {
		t.Error("a tick inside the interval does not sweep")
	}
	if !SweepDue(last, baseTime.Add(SweepEvery)) {
		t.Error("a tick past the interval sweeps again")
	}
}
