package serve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// TestKHistoryAppendReloadRoundTrip pins the file as the estimate's state: a
// missing file reads as empty history, and appended samples survive a fresh
// load with no in-memory state, which is what a restarted server reads.
func TestKHistoryAppendReloadRoundTrip(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if got, err := loadKHistory(root); err != nil || len(got) != 0 {
		t.Fatalf("load missing = %v, %v; want empty, nil", got, err)
	}
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if err := appendKSample(root, 3, at); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := appendKSample(root, 1, at.Add(time.Hour)); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := loadKHistory(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	want := []kSample{{Count: 3, At: at}, {Count: 1, At: at.Add(time.Hour)}}
	if len(got) != len(want) {
		t.Fatalf("reload = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reload = %v, want %v", got, want)
		}
	}
}

// TestKHistoryPrunesByAge pins the 24-hour window with an injected now: a
// sample exactly 24 hours old survives the next append at the boundary while
// one 25 hours old drops, so pruning keys on timestamps and never on the
// count.
func TestKHistoryPrunesByAge(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		count int
		at    time.Time
	}{{7, now.Add(-25 * time.Hour)}, {3, now.Add(-24 * time.Hour)}, {5, now.Add(-23 * time.Hour)}, {1, now}} {
		if err := appendKSample(root, tc.count, tc.at); err != nil {
			t.Fatalf("append %+v: %v", tc, err)
		}
	}
	got, err := loadKHistory(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got) != 3 || got[0].Count != 3 || got[1].Count != 5 || got[2].Count != 1 {
		t.Fatalf("reload counts = %v, want [{3} {5} {1}]: 25h drops, 24h stays at the boundary, 23h stays", got)
	}
}

// TestKHistoryMalformedReadsEmptyPlusError pins the corrupt-file read: the
// caller logs the error and treats the history as empty, never as a reset.
func TestKHistoryMalformedReadsEmptyPlusError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "costs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kHistoryPath(root), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadKHistory(root)
	if err == nil {
		t.Fatal("load malformed = nil error, want an error")
	}
	if len(got) != 0 {
		t.Fatalf("load malformed = %v, want empty", got)
	}
}

// TestKHistoryAppendMalformedKeepsBytes pins the append load-error arm: a
// corrupt file fails the append and the bytes survive untouched, so a bad
// tick never resets the history the way a successful write would.
func TestKHistoryAppendMalformedKeepsBytes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "costs"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := []byte("{nope")
	if err := os.WriteFile(kHistoryPath(root), before, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := appendKSample(root, 1, time.Now()); err == nil {
		t.Error("append malformed = nil error, want an error")
	}
	after, err := os.ReadFile(kHistoryPath(root))
	if err != nil {
		t.Fatalf("reread malformed file: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("malformed file = %q, want unchanged %q", after, before)
	}
}

// liveRoundDir writes a fake cgroup dir holding an anon and swap reading.
func liveRoundDir(t *testing.T, anon, swap string) string {
	t.Helper()
	return writeCgroupTree(t, map[string]string{
		"memory.stat":         "anon " + anon + "\n",
		"memory.swap.current": swap + "\n",
	})
}

// TestTickSamplesBursting pins the per-tick sampling: a round above half its
// default burst counts, one below does not, and a round whose dir is
// unreadable — a container round has no scope, a pid may vanish — counts as
// not bursting without failing the tick. Removing the bursting round's dir
// before the next tick drops the count to zero and the tick stays clean.
func TestTickSamplesBursting(t *testing.T) {
	const gb = int64(1 << 30)
	env := setupTestEnv(t)
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")

	pidOf := func(id remote.ClientID) int {
		t.Helper()
		b, err := testRuntime(t, env.srv, id).Store.Load("api")
		if err != nil {
			t.Fatalf("load api: %v", err)
		}
		if b.Builder.PID == 0 {
			t.Fatal("api PID = 0, want the started round's pid")
		}
		return b.Builder.PID
	}
	pidA, pidB, pidC := pidOf(env.id), pidOf(ownerB.id), pidOf(ownerC.id)

	// A holds 3GiB against a 5GiB default burst, so it bursts; B holds 1GiB
	// and does not; C's pid resolves nowhere, like a container round's.
	dirA := liveRoundDir(t, "3221225472", "0")
	dirB := liveRoundDir(t, "1073741824", "0")
	byPID := map[int]string{pidA: dirA, pidB: dirB}
	budgetDir := writeCgroupTree(t, map[string]string{"memory.max": "max\n"})
	fakeLiveInputs(t,
		func() (string, error) { return budgetDir, nil },
		func(pid int) (string, error) {
			if d, ok := byPID[pid]; ok {
				return d, nil
			}
			return "", errors.New("no cgroup")
		},
		func() (int64, error) { return 16 * gb, nil })
	if pidC == pidA || pidC == pidB {
		t.Fatalf("pids collide: %d %d %d", pidA, pidB, pidC)
	}

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	got, err := loadKHistory(env.srv.cfg.Root)
	if err != nil {
		t.Fatalf("reload k-history: %v", err)
	}
	if len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("k-history = %v, want one sample with count 1", got)
	}

	if err := os.RemoveAll(dirA); err != nil {
		t.Fatal(err)
	}
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick with a vanished dir: %v", err)
	}
	got, err = loadKHistory(env.srv.cfg.Root)
	if err != nil {
		t.Fatalf("reload k-history: %v", err)
	}
	if len(got) != 2 || got[1].Count != 0 {
		t.Fatalf("k-history = %v, want a second sample with count 0", got)
	}
}

// TestSampleBurstingHalfBoundaries pins the bursting threshold against a 4GiB
// burst, so half is exactly 2GiB: swap joins anon past half, the exact half
// does not count, and a missing swap reading drops the round instead of
// counting anon alone.
func TestSampleBurstingHalfBoundaries(t *testing.T) {
	const gib = int64(1 << 30)
	for _, tc := range []struct {
		name string
		anon string
		swap string
		want int
	}{
		// 1.5GiB anon plus 1GiB swap clears half.
		{"swap pushes over half", "1610612736", "1073741824", 1},
		// 1.5GiB anon plus 0.5GiB swap lands exactly on half.
		{"exact half does not count", "1610612736", "536870912", 0},
		// No swap file: 3GiB anon alone would clear half, but the
		// unreadable swap drops the round.
		{"missing swap drops the round", "3221225472", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dir string
			if tc.swap == "" {
				dir = writeCgroupTree(t, map[string]string{"memory.stat": "anon " + tc.anon + "\n"})
			} else {
				dir = liveRoundDir(t, tc.anon, tc.swap)
			}
			fakeLiveInputs(t, nil, func(int) (string, error) { return dir, nil }, nil)
			got := (&Server{}).sampleBursting(census{RunningRounds: []runningRound{{PID: 4242, Burst: 4 * gib}}})
			if got != tc.want {
				t.Errorf("sampleBursting = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestAdmitKHistoryAdmitsAtOne pins the K feed: two small-seat builders run
// while a default-seat candidate waits under the cold-start K=2, and a seeded
// bursting history with p95 1 starts it on the next tick.
func TestAdmitKHistoryAdmitsAtOne(t *testing.T) {
	const gb = int64(1 << 30)
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Policy = policy.Policy{Serve: &policy.ServePolicy{MemoryBudget: "10G"}}
	})
	ownerB := addOwner(t, env, "bob")
	ownerC := addOwner(t, env, "carol")
	// Small seats for the first two repos, so both run under the cold-start
	// K=2: two bases plus twice the peak is 4GiB against the 10GiB budget.
	seedCostHistory(t, env.srv.cfg.Root, env.repoID, "builder", gb, gb, 6)
	seedCostHistory(t, env.srv.cfg.Root, ownerB.repoID, "builder", gb, gb, 6)

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundRunning {
		t.Fatalf("B round_state = %q, want running: two small seats fit under K=2", viewB.RoundState)
	}
	respC, bodyC := sendRound(t, env, ownerC.kp, ownerC.clientDir, ownerC.repoID, ownerC.headSHA, "api", "# Plan C")
	requireCreated(t, respC, bodyC, "C")
	viewC := decodeView(t, bodyC)
	if viewC.RoundState != remote.RoundQueued {
		t.Fatalf("C round_state = %q, want queued: a default seat needs 13GiB under K=2", viewC.RoundState)
	}
	if viewC.Queue == nil || viewC.Queue.Wait != waitMemory {
		t.Fatalf("C queue = %+v, want the memory hold under K=2", viewC.Queue)
	}

	// Six count-1 samples put the p95 at 1 however the tick's own quiet
	// sample lands.
	now := time.Now()
	for i := range 6 {
		if err := appendKSample(env.srv.cfg.Root, 1, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("seed k sample %d: %v", i, err)
		}
	}
	pidOf := func(id remote.ClientID) int {
		t.Helper()
		b, err := testRuntime(t, env.srv, id).Store.Load("api")
		if err != nil {
			t.Fatalf("load api: %v", err)
		}
		return b.Builder.PID
	}
	// Both live rounds hold far below half their 1GiB burst, so the tick's
	// own sample is quiet and the p95 stays at 1.
	byPID := map[int]string{pidOf(env.id): liveRoundDir(t, "104857600", "0"), pidOf(ownerB.id): liveRoundDir(t, "104857600", "0")}
	budgetDir := writeCgroupTree(t, map[string]string{"memory.max": "max\n"})
	fakeLiveInputs(t,
		func() (string, error) { return budgetDir, nil },
		func(pid int) (string, error) {
			if d, ok := byPID[pid]; ok {
				return d, nil
			}
			return "", errors.New("no cgroup")
		},
		func() (int64, error) { return 16 * gb, nil })

	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	requireRoundState(t, testRuntime(t, env.srv, ownerC.id), "api", remote.RoundRunning)
}

// TestAdmissionInputsKHistoryUnreadableKeepsColdStart pins the history-read
// fallback: a corrupt K file warns and the pass keeps the cold-start K=2
// instead of failing.
func TestAdmissionInputsKHistoryUnreadableKeepsColdStart(t *testing.T) {
	const gb = int64(1 << 30)
	env := setupTestEnv(t)
	admitZeroBudget(t, env)
	if err := os.MkdirAll(filepath.Join(env.srv.cfg.Root, "costs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kHistoryPath(env.srv.cfg.Root), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := writeCgroupTree(t, map[string]string{
		"memory.max":      "max\n",
		"memory.pressure": pressureBody("1.0"),
	})
	fakeLiveInputs(t, func() (string, error) { return dir, nil }, nil, func() (int64, error) { return 16 * gb, nil })
	_, k, _ := env.srv.admissionInputs(nil)
	if k != 2 {
		t.Errorf("k = %d, want the cold-start 2 with an unreadable history", k)
	}
}

// TestAdmissionInputsThinKHistoryKeepsColdStart pins the depth rule: a
// history below seatMinSamples reads as no history, so early quiet ticks keep
// the cold-start K=2 instead of collapsing it to 1.
func TestAdmissionInputsThinKHistoryKeepsColdStart(t *testing.T) {
	const gb = int64(1 << 30)
	env := setupTestEnv(t)
	admitZeroBudget(t, env)
	now := time.Now()
	for i := range seatMinSamples - 1 {
		if err := appendKSample(env.srv.cfg.Root, 0, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("seed k sample %d: %v", i, err)
		}
	}
	dir := writeCgroupTree(t, map[string]string{
		"memory.max":      "max\n",
		"memory.pressure": pressureBody("1.0"),
	})
	fakeLiveInputs(t, func() (string, error) { return dir, nil }, nil, func() (int64, error) { return 16 * gb, nil })
	_, k, _ := env.srv.admissionInputs(nil)
	if k != 2 {
		t.Errorf("k = %d, want the cold-start 2 with a thin history", k)
	}
}

// TestAdmissionInputsFullKHistoryYieldsOne pins the depth rule at equality:
// seatMinSamples quiet samples are enough history to leave the cold-start
// K=2 for the estimate's floor of 1.
func TestAdmissionInputsFullKHistoryYieldsOne(t *testing.T) {
	const gb = int64(1 << 30)
	env := setupTestEnv(t)
	admitZeroBudget(t, env)
	now := time.Now()
	for i := range seatMinSamples {
		if err := appendKSample(env.srv.cfg.Root, 0, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("seed k sample %d: %v", i, err)
		}
	}
	dir := writeCgroupTree(t, map[string]string{
		"memory.max":      "max\n",
		"memory.pressure": pressureBody("1.0"),
	})
	fakeLiveInputs(t, func() (string, error) { return dir, nil }, nil, func() (int64, error) { return 16 * gb, nil })
	_, k, _ := env.srv.admissionInputs(nil)
	if k != 1 {
		t.Errorf("k = %d, want 1 with a full quiet history", k)
	}
}
