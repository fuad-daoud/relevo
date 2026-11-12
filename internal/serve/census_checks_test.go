package serve

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// stubCheckLister is an openCheckLister with a scripted answer: chains and
// rows for the counting paths, an error for the unreadable path.
type stubCheckLister struct {
	chains []db.ChainRow
	rows   []db.ChainCheckRow
	err    error
}

func (s stubCheckLister) Chains() ([]db.ChainRow, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.chains, nil
}

func (s stubCheckLister) ChainCheckOpen() ([]db.ChainCheckRow, error) {
	return s.rows, nil
}

// TestCountOpenChecksSkipsUnreadableList pins the census's fallback: an
// unreadable open-check list counts no check seat rather than failing the
// pass.
func TestCountOpenChecksSkipsUnreadableList(t *testing.T) {
	t.Parallel()

	var c census
	countOpenChecks(&c, t.TempDir(), nil, stubCheckLister{err: errors.New("db locked")}, nil, map[string]bool{})
	if c.Running != 0 || len(c.RunningRounds) != 0 {
		t.Fatalf("census = %+v, want no running seats on an unreadable list", c.RunningRounds)
	}
}

// plantOpenCheck plants a chain with one running check row, the shape a live
// check holds while its member's round is closed.
func plantOpenCheck(t *testing.T, rt relevo.Runtime, chain, member string, pid int) {
	t.Helper()
	now := time.Now()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.ChainSaveWithEvent(db.ChainRow{
			ID: "chain-" + chain, Name: chain, Owner: rt.Store.Owner(), Status: "running",
			Phase: "build", Step: "building", Plan: 1, Plans: 1, Builder: member,
			Worktree:  rt.Store.WorktreePath(member),
			CreatedAt: now, UpdatedAt: now,
		}, db.ChainEventRow{
			TS: now, Phase: "build", Step: "building", Member: member,
			Round: 1, Plan: 1, Event: "{}", Action: "{}",
		}); err != nil {
			return err
		}
		return tx.ChainCheckPut(chain, db.ChainCheckRow{
			Run: 1, Step: "verify", Visit: 1, Command: "make check",
			PID: pid, StartedAt: now.Unix(),
		})
	}); err != nil {
		t.Fatalf("plant the open check on %s: %v", chain, err)
	}
}

// TestCensusCountsAGateAndAChainCheck pins the round's seat through its gate
// and its member's chain check: a binding with no builder process but a live
// gate run counts with the gate's pid, a member with an open chain check on
// a real owner store counts with the check's pid at its history seat, and an
// open served check counts the same way. The K sample is asked for all three
// pids, and the reservation holds all three seats.
func TestCensusCountsAGateAndAChainCheck(t *testing.T) {
	env := setupTestEnv(t)
	rt := env.runtime(t)

	const gatePID, checkPID, servedPID = 9001, 4321, 7331
	now := time.Now()

	if err := rt.Store.Save(store.Binding{
		Name: "gated", Owner: string(env.id), CWD: "/build/gated", Round: 1,
		Builder: store.Endpoint{Mode: store.ModeHeadless},
		GateRun: &store.GateRun{PID: gatePID, StartedAt: now.Unix(), Command: "make check", Round: 1},
	}); err != nil {
		t.Fatalf("save the gated binding: %v", err)
	}
	if err := rt.Store.Save(store.Binding{
		Name: "served", Owner: string(env.id), CWD: "/build/served", Round: 2,
		Builder:  store.Endpoint{Mode: store.ModeHeadless},
		CheckRun: &store.CheckRun{ID: "c1", Command: "make check", PID: servedPID, StartedAt: now.Unix(), Round: 1},
	}); err != nil {
		t.Fatalf("save the served binding: %v", err)
	}
	const seedBase, seedBurst = int64(3 << 30), int64(7 << 30)
	seedCostHistory(t, env.srv.cfg.Root, env.repoID, "builder", seedBase, seedBurst, 6)
	if err := rt.Store.Save(store.Binding{
		Name: "member", Owner: string(env.id), CWD: "/build/member", Round: 2, Role: "builder",
		Builder: store.Endpoint{Mode: store.ModeHeadless},
		Serve:   &store.ServeFacts{RepoID: env.repoID},
	}); err != nil {
		t.Fatalf("save the member binding: %v", err)
	}
	plantOpenCheck(t, rt, "chain1", "member", checkPID)

	c, err := env.srv.census()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if c.Running != 3 || len(c.RunningRounds) != 3 {
		t.Fatalf("census running = %d rounds %v, want 3 rounds", c.Running, c.RunningRounds)
	}
	byPID := make(map[int]runningRound, len(c.RunningRounds))
	for _, r := range c.RunningRounds {
		if r.Planner {
			t.Errorf("running round pid %d reads planner, want the builder class", r.PID)
		}
		byPID[r.PID] = r
	}
	for pid, want := range map[int]runningRound{
		gatePID:   {PID: gatePID, Base: seatBuilderBase, Burst: seatBuilderBurst},
		checkPID:  {PID: checkPID, Base: seedBase, Burst: seedBurst},
		servedPID: {PID: servedPID, Base: seatBuilderBase, Burst: seatBuilderBurst},
	} {
		got, ok := byPID[pid]
		if !ok {
			t.Errorf("no running round with pid %d in %v", pid, c.RunningRounds)
			continue
		}
		if got.Base != want.Base || got.Burst != want.Burst {
			t.Errorf("pid %d seat = {%d %d}, want {%d %d}", pid, got.Base, got.Burst, want.Base, want.Burst)
		}
	}

	var seen []int
	fakeLiveInputs(t, nil, func(pid int) (string, error) {
		seen = append(seen, pid)
		return "", errors.New("no cgroup in test")
	}, nil)
	if n := env.srv.sampleBursting(c); n != 0 {
		t.Errorf("sampleBursting = %d, want 0 with unreadable cgroups", n)
	}
	for _, pid := range []int{gatePID, checkPID, servedPID} {
		if !slices.Contains(seen, pid) {
			t.Errorf("sampleBursting never asked for pid %d, asked %v", pid, seen)
		}
	}

	const k = 2
	wantReserved := (seatBuilderBase + seedBase + seatBuilderBase) + int64(k)*max(seedBurst, seatBuilderBurst)
	if got := reservedBytes(c.RunningRounds, k); got != wantReserved {
		t.Errorf("reservedBytes = %d, want %d", got, wantReserved)
	}
}

// TestCensusCountsACheckedBindingOnce pins the single-seat rule: a live
// builder with an open chain check against it counts once, with the
// builder's pid, and a check whose member binding is gone still counts at
// the builder defaults.
func TestCensusCountsACheckedBindingOnce(t *testing.T) {
	env := setupTestEnv(t)
	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, resp, body, "api")
	rt := env.runtime(t)
	live, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load api: %v", err)
	}
	if live.Builder.PID == 0 {
		t.Fatal("api builder PID = 0, want the started round's pid")
	}
	plantOpenCheck(t, rt, "chain1", "api", 5555)
	plantOpenCheck(t, rt, "chain2", "ghost", 7777)

	c, err := env.srv.census()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	if c.Running != 2 || len(c.RunningRounds) != 2 {
		t.Fatalf("census running = %d rounds %v, want 2 rounds", c.Running, c.RunningRounds)
	}
	byPID := make(map[int]runningRound, len(c.RunningRounds))
	for _, r := range c.RunningRounds {
		byPID[r.PID] = r
	}
	got, ok := byPID[live.Builder.PID]
	if !ok {
		t.Fatalf("no running round with the builder pid %d in %v", live.Builder.PID, c.RunningRounds)
	}
	if got.Planner {
		t.Errorf("api round reads planner, want the builder class")
	}
	if _, dup := byPID[5555]; dup {
		t.Errorf("the check's pid 5555 counts a second seat beside the builder's %d", live.Builder.PID)
	}
	ghost, ok := byPID[7777]
	if !ok {
		t.Fatalf("no running round with the ghost check pid 7777 in %v", c.RunningRounds)
	}
	if ghost.Base != seatBuilderBase || ghost.Burst != seatBuilderBurst {
		t.Errorf("ghost seat = {%d %d}, want the builder defaults {%d %d}", ghost.Base, ghost.Burst, seatBuilderBase, seatBuilderBurst)
	}
}
