package relevo

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainCheckFixture plants a running chain with one writer member whose round
// 1 has closed, so a check's log has a round to key to. The chain's builder is
// that writer.
func chainCheckFixture(t *testing.T) (Runtime, *fakeRunner, db.ChainRow) {
	t.Helper()
	rt := newRuntime(t)
	fr := newFakeRunner()
	rt.Runner = fr

	member := store.Binding{Name: "shop", CWD: "/repo", Round: 1, State: store.StateActive}
	if err := rt.Store.Save(member); err != nil {
		t.Fatalf("save member: %v", err)
	}
	c := db.ChainRow{
		ID: db.NewID(), Name: "shop", Status: "running", Phase: "build", Step: "building",
		Plan: 1, Plans: 1, Builder: "shop", Worktree: "/repo",
		CreatedAt: baseTime, UpdatedAt: baseTime,
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.ChainPut(c); err != nil {
			return err
		}
		return tx.AppendLog("shop", store.LogEntry{
			TS: baseTime, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Path: "/x/001-report.md",
		})
	}); err != nil {
		t.Fatalf("plant chain: %v", err)
	}
	return rt, fr, c
}

// chainStartCheckForTest starts a check under the state lock.
func chainStartCheckForTest(t *testing.T, rt Runtime, c db.ChainRow, step, command string) int {
	t.Helper()
	var run int
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		run, err = chainStartCheck(context.Background(), rt, tx, c, step, command)
		return err
	}); err != nil {
		t.Fatalf("chainStartCheck: %v", err)
	}
	return run
}

// chainAdvanceCheckForTest advances a check under the state lock.
func chainAdvanceCheckForTest(t *testing.T, rt Runtime, c db.ChainRow, run int) (string, string) {
	t.Helper()
	var result, logKey string
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		result, logKey, err = chainAdvanceCheck(context.Background(), rt, tx, c, run)
		return err
	}); err != nil {
		t.Fatalf("chainAdvanceCheck: %v", err)
	}
	return result, logKey
}

// loadChainCheck reads one chain_check row.
func loadChainCheck(t *testing.T, rt Runtime, run int) db.ChainCheckRow {
	t.Helper()
	var row db.ChainCheckRow
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		row, err = tx.ChainCheck("shop", run)
		return err
	}); err != nil {
		t.Fatalf("ChainCheck: %v", err)
	}
	return row
}

func TestChainCheckGreenOnExitZero(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	rt.Scope = &spawn.ScopeSpec{CPUQuota: "150%", GateCPUQuota: "300%", AllowedCPUs: "0-3"}
	run := chainStartCheckForTest(t, rt, c, "check", "make check")

	if len(fr.specs) != 1 {
		t.Fatalf("starts = %d, want 1", len(fr.specs))
	}
	spec := fr.specs[0]
	if spec.Dir != "/repo" {
		t.Errorf("Dir = %q, want the chain's tree", spec.Dir)
	}
	if len(spec.Argv) != 3 || spec.Argv[0] != "sh" || spec.Argv[1] != "-c" || spec.Argv[2] != "make check 2>&1" {
		t.Errorf("Argv = %v, want sh -c \"make check 2>&1\"", spec.Argv)
	}
	if spec.Scope == nil {
		t.Fatal("check ran with no scope template applied")
	}
	if want := "relevo-gate-local-shop-check-1"; spec.Scope.Unit != want {
		t.Errorf("Scope.Unit = %q, want %q", spec.Scope.Unit, want)
	}
	if spec.Scope.CPUQuota != "300%" {
		t.Errorf("Scope.CPUQuota = %q, want the gate quota 300%%", spec.Scope.CPUQuota)
	}
	if spec.Scope.GateCPUQuota != "" {
		t.Errorf("Scope.GateCPUQuota = %q, want it zeroed", spec.Scope.GateCPUQuota)
	}
	if spec.Scope.AllowedCPUs != "0-3" {
		t.Errorf("Scope.AllowedCPUs = %q, want the template's pool, not a pinned core", spec.Scope.AllowedCPUs)
	}

	pid := fr.handles[0].PID
	fr.script(pid, false)
	fr.exit(pid, 0)

	result, logKey := chainAdvanceCheckForTest(t, rt, c, run)
	if result != chainCheckGreen {
		t.Errorf("result = %q, want green", result)
	}
	if logKey == "" {
		t.Error("logKey is empty")
	}
	row := loadChainCheck(t, rt, run)
	if row.Result != "pass" || row.ExitCode != 0 {
		t.Errorf("row = %+v, want result pass and exit 0", row)
	}
	if row.Log != logKey {
		t.Errorf("row.Log = %q, want the returned key %q", row.Log, logKey)
	}
}

func TestChainCheckRedOnFail(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	pid := fr.handles[0].PID
	fr.script(pid, false)
	fr.exit(pid, 2)

	result, _ := chainAdvanceCheckForTest(t, rt, c, run)
	if result != chainCheckRed {
		t.Errorf("result = %q, want red", result)
	}
	row := loadChainCheck(t, rt, run)
	if row.Result != "fail" || row.ExitCode != 2 {
		t.Errorf("row = %+v, want result fail and exit 2", row)
	}
}

func TestChainCheckTimeoutIsRed(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	row := loadChainCheck(t, rt, run)

	clock := &fakeClock{now: time.Unix(row.StartedAt, 0)}
	rt = withClock(rt, clock)
	clock.Advance(2 * rt.Policy.GateTimeout())

	result, _ := chainAdvanceCheckForTest(t, rt, c, run)
	if result != chainCheckRed {
		t.Errorf("result = %q, want red", result)
	}
	if len(fr.kills) != 1 || fr.kills[0].PID != row.PID {
		t.Errorf("kills = %+v, want the timed-out run killed", fr.kills)
	}
	got := loadChainCheck(t, rt, run)
	if got.Result != "timeout" {
		t.Errorf("row.Result = %q, want timeout", got.Result)
	}
}

func TestChainCheckRerunsOnceAfterRestartLoss(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	row := loadChainCheck(t, rt, run)

	// The daemon that started the run has gone: the recorded start predates
	// this one, and the watch set never saw the process.
	rt.StartedAt = time.Unix(row.StartedAt, 0).Add(time.Minute)
	rt.Watched = NewWatched()
	fr.script(row.PID, false) // exited, no exit trailer

	result, _ := chainAdvanceCheckForTest(t, rt, c, run)
	if result != "" {
		t.Errorf("result = %q, want empty while the re-run runs", result)
	}
	if len(fr.handles) != 2 {
		t.Fatalf("starts = %d, want the one re-run", len(fr.handles))
	}
	rerun := loadChainCheck(t, rt, run)
	if rerun.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1", rerun.Attempt)
	}
	if rerun.PID != fr.handles[1].PID {
		t.Errorf("PID = %d, want the re-run's %d", rerun.PID, fr.handles[1].PID)
	}

	// The one re-run is the last: a second loss is a failure, not a third run.
	fr.script(rerun.PID, false)
	result, _ = chainAdvanceCheckForTest(t, rt, c, run)
	if result != chainCheckRed {
		t.Errorf("result = %q, want red after a second loss", result)
	}
	if len(fr.handles) != 2 {
		t.Errorf("starts = %d, want no third run", len(fr.handles))
	}
	if final := loadChainCheck(t, rt, run); final.Result != "error" {
		t.Errorf("row.Result = %q, want error", final.Result)
	}
}

func TestChainCheckRecordsStepAndVisit(t *testing.T) {
	t.Parallel()

	rt, _, c := chainCheckFixture(t)
	first := chainStartCheckForTest(t, rt, c, "check", "make check")
	second := chainStartCheckForTest(t, rt, c, "check", "make check")
	other := chainStartCheckForTest(t, rt, c, "scan", "make scan")

	if first != 1 || second != 2 || other != 3 {
		t.Fatalf("run numbers = %d, %d, %d, want 1, 2, 3", first, second, other)
	}
	for _, tc := range []struct {
		run, visit int
		step       string
	}{
		{second, 2, "check"},
		{first, 1, "check"},
		{other, 1, "scan"},
	} {
		row := loadChainCheck(t, rt, tc.run)
		if row.Visit != tc.visit {
			t.Errorf("run %d (%s) visit = %d, want %d", tc.run, tc.step, row.Visit, tc.visit)
		}
		if row.Step != tc.step {
			t.Errorf("run %d step = %q, want %q", tc.run, row.Step, tc.step)
		}
	}
}

func TestChainCheckLogIsARoundFile(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	row := loadChainCheck(t, rt, run)

	body := []byte("check output\nsecond line\n")
	if err := os.WriteFile(row.Log, body, 0o644); err != nil {
		t.Fatalf("write check log: %v", err)
	}
	pid := fr.handles[0].PID
	fr.script(pid, false)
	fr.exit(pid, 0)

	result, logKey := chainAdvanceCheckForTest(t, rt, c, run)
	if result != chainCheckGreen {
		t.Fatalf("result = %q, want green", result)
	}
	got, err := rt.Store.ReadFile(logKey)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logKey, err)
	}
	if string(got) != string(body) {
		t.Errorf("ReadFile = %q, want %q", got, body)
	}
	if _, err := os.Stat(logKey); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the log is still on disk after sealing: %v", err)
	}
}

// TestChainCheckSealsUnderTheRoundItStartedWith pins the seal to the round the
// row was keyed with: a writer that closes a new round while the check runs
// must not make the seal recompute a different round and corrupt its own
// round-file key.
func TestChainCheckSealsUnderTheRoundItStartedWith(t *testing.T) {
	t.Parallel()

	rt, fr, c := chainCheckFixture(t)
	run := chainStartCheckForTest(t, rt, c, "check", "make check")
	row := loadChainCheck(t, rt, run)

	body := []byte("check output\nsecond line\n")
	if err := os.WriteFile(row.Log, body, 0o644); err != nil {
		t.Fatalf("write check log: %v", err)
	}

	// The writer closes a new round while the run is live.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("shop", store.LogEntry{
			TS: baseTime.Add(time.Minute), Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Path: "/x/002-report.md",
		})
	}); err != nil {
		t.Fatalf("close round 2: %v", err)
	}

	pid := fr.handles[0].PID
	fr.script(pid, false)
	fr.exit(pid, 0)

	result, logKey := chainAdvanceCheckForTest(t, rt, c, run)
	if result != chainCheckGreen {
		t.Fatalf("result = %q, want green", result)
	}
	if logKey != row.Log {
		t.Errorf("logKey = %q, want the captured row.Log %q", logKey, row.Log)
	}
	if sealed := loadChainCheck(t, rt, run); sealed.Result != "pass" {
		t.Errorf("row.Result = %q, want pass", sealed.Result)
	}
	got, err := rt.Store.ReadFile(row.Log)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", row.Log, err)
	}
	if string(got) != string(body) {
		t.Errorf("ReadFile = %q, want %q", got, body)
	}
	if _, err := os.Stat(row.Log); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the log is still on disk after sealing: %v", err)
	}
}
