package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// testGateKV is a real t.TempDir() database for a Runtime literal's Gates field.
func testGateKV(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// mcpTestPlannerA and mcpTestPlannerB are valid planner ids; the status filter keys on them.
const (
	mcpTestPlannerA = "pl_aaaaaaaabbbb"
	mcpTestPlannerB = "pl_ccccccccdddd"
)

// stubRunner implements spawn.Runner with no-op stubs: a headless binding's
// PID is 0, but Runtime.Runner must still be non-nil or sendPreflight refuses first.
type stubRunner struct{}

func (stubRunner) Start(ctx context.Context, spec spawn.ProcSpec) (spawn.ProcHandle, error) {
	return spawn.ProcHandle{}, nil
}
func (stubRunner) Alive(ctx context.Context, h spawn.ProcHandle) (bool, error) { return false, nil }
func (stubRunner) ExitCode(ctx context.Context, h spawn.ProcHandle, logPath string) (int, bool) {
	return 0, false
}
func (stubRunner) Kill(ctx context.Context, h spawn.ProcHandle, _ string) error { return nil }
func (stubRunner) Rusage(context.Context, spawn.ProcHandle, string) (spawn.ProcRusage, bool) {
	return spawn.ProcRusage{}, false
}

func writeCandidates(t *testing.T, body string) *candidate.Set {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write candidates fixture: %v", err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("load candidates fixture: %v", err)
	}
	return set
}

func writeTempPlan(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

func saveVerbBinding(t *testing.T, s *store.Store, b store.Binding) {
	t.Helper()
	if err := s.Save(b); err != nil {
		t.Fatalf("save binding %q: %v", b.Name, err)
	}
}

func TestRelevoVerbsStatusFiltersByPlannerThenName(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{
		Store: s,
		Now:   func() time.Time { return time.Unix(0, 0) },
	}

	saveVerbBinding(t, s, store.Binding{Name: "mine-a", CWD: "/repo/mine-a", Planner: store.Endpoint{PaneID: "w2:p3"}, PlannerID: mcpTestPlannerA, Round: 1, State: store.StateActive})
	saveVerbBinding(t, s, store.Binding{Name: "mine-done", CWD: "/repo/mine-done", Planner: store.Endpoint{PaneID: "w2:p3"}, PlannerID: mcpTestPlannerA, Round: 1, State: store.StateDone})
	saveVerbBinding(t, s, store.Binding{Name: "other", CWD: "/repo/other", Planner: store.Endpoint{PaneID: "w9:p9"}, PlannerID: mcpTestPlannerB, Round: 1, State: store.StateActive})

	v := &RelevoVerbs{RT: rt, Planner: mcpTestPlannerA}

	res, err := v.Status(context.Background(), StatusArgs{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rep, ok := res.(view.Report)
	if !ok {
		t.Fatalf("result = %#v, want view.Report", res)
	}
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine-a" {
		t.Fatalf("default status = %+v, want only mine-a (this planner, DONE hidden)", rep.Bindings)
	}

	res, err = v.Status(context.Background(), StatusArgs{All: true})
	if err != nil {
		t.Fatalf("Status all: %v", err)
	}
	rep = res.(view.Report)
	if len(rep.Bindings) != 3 {
		t.Fatalf("all status = %d bindings, want 3", len(rep.Bindings))
	}

	res, err = v.Status(context.Background(), StatusArgs{Name: "mine-done"})
	if err != nil {
		t.Fatalf("Status by name: %v", err)
	}
	rep = res.(view.Report)
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine-done" {
		t.Fatalf("status by name = %+v, want only mine-done (DONE included when named)", rep.Bindings)
	}

	if _, err := v.Status(context.Background(), StatusArgs{Name: "other"}); err == nil {
		t.Fatal("Status naming a binding on a different planner must error")
	}
}

// TestMCPStatusFiltersByPlanner: the two bindings here share a pane, so only the planner id tells them apart.
func TestMCPStatusFiltersByPlanner(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{
		Store: s,
		Now:   func() time.Time { return time.Unix(0, 0) },
	}

	saveVerbBinding(t, s, store.Binding{Name: "mine", CWD: "/repo/mine", Planner: store.Endpoint{PaneID: "w2:p3"}, PlannerID: mcpTestPlannerA, Round: 1, State: store.StateActive})
	saveVerbBinding(t, s, store.Binding{Name: "cousin", CWD: "/repo/cousin", Planner: store.Endpoint{PaneID: "w2:p3"}, PlannerID: mcpTestPlannerB, Round: 1, State: store.StateActive})

	v := &RelevoVerbs{RT: rt, Planner: mcpTestPlannerA}
	res, err := v.Status(context.Background(), StatusArgs{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rep := res.(view.Report)
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine" {
		t.Fatalf("status = %+v, want only mine (the same pane's cousin is another planner)", rep.Bindings)
	}
	if rep.Bindings[0].PlannerID != mcpTestPlannerA {
		t.Errorf("row PlannerID = %q, want %q", rep.Bindings[0].PlannerID, mcpTestPlannerA)
	}
}

// newHeadlessSendFixture builds a RelevoVerbs and a plan file for a headless
// binding needing no harness or live pane, and the plan path to send.
func newHeadlessSendFixture(t *testing.T) (*RelevoVerbs, string) {
	t.Helper()
	s := store.New(t.TempDir())
	set := writeCandidates(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}]`)
	rt := relevo.Runtime{
		Store:      s,
		Candidates: set,
		Runner:     stubRunner{},
		Gates:      testGateKV(t),
		Now:        func() time.Time { return time.Unix(0, 0) },
	}
	saveVerbBinding(t, s, store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner:          store.Endpoint{PaneID: "w2:p3"},
		Builder:          store.Endpoint{Mode: store.ModeHeadless},
		BuilderCandidate: "agy/test/m",
		Round:            1, State: store.StateActive,
	})
	return &RelevoVerbs{RT: rt, Planner: mcpTestPlannerA}, writeTempPlan(t, "# do the thing")
}

// TestRelevoVerbsSendHeadless covers both of Send's headless paths, replacing
// TestRelevoVerbsSendDryRunHeadless and TestRelevoVerbsSendRealRunHeadless.
func TestRelevoVerbsSendHeadless(t *testing.T) {
	t.Run("dry run takes relevo.SendDryRun, not a seam", func(t *testing.T) {
		v, plan := newHeadlessSendFixture(t)
		res, err := v.Send(context.Background(), SendArgs{Name: "webshop", File: plan, DryRun: true})
		if err != nil {
			t.Fatalf("Send dry-run: %v", err)
		}
		d, ok := res.(relevo.DryRun)
		if !ok {
			t.Fatalf("result = %#v, want relevo.DryRun", res)
		}
		if d.Mode != "headless" {
			t.Errorf("Mode = %q, want headless", d.Mode)
		}
		if d.Name != "webshop" || d.Round != 1 {
			t.Errorf("DryRun = %+v, want Name webshop, Round 1", d)
		}
	})

	t.Run("real run takes relevo.Send, not SendDryRun", func(t *testing.T) {
		v, plan := newHeadlessSendFixture(t)
		res, err := v.Send(context.Background(), SendArgs{Name: "webshop", File: plan})
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		sr, ok := res.(sendResult)
		if !ok {
			t.Fatalf("result = %#v, want sendResult", res)
		}
		if sr.Round != 1 {
			t.Errorf("Round = %d, want 1", sr.Round)
		}
		// store filled in the 24h default, since the fixture set no --timeout.
		if sr.WaitBudget != "24h0m0s" {
			t.Errorf("WaitBudget = %q, want 24h0m0s", sr.WaitBudget)
		}
	})
}

func TestRelevoVerbsDoneForwardsAndReportsText(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{Store: s, Now: func() time.Time { return time.Unix(0, 0) }}
	saveVerbBinding(t, s, store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner: store.Endpoint{PaneID: "w2:p3"},
		Builder: store.Endpoint{Mode: store.ModeHeadless},
		Round:   1, State: store.StateActive,
	})

	v := &RelevoVerbs{RT: rt, Planner: mcpTestPlannerA}
	res, err := v.Done(context.Background(), DoneArgs{Name: "webshop"})
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	dr, ok := res.(doneResult)
	if !ok {
		t.Fatalf("result = %#v, want doneResult", res)
	}
	if dr.Text == "" {
		t.Error("Text must be set from relevo.DoneText")
	}

	updated, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if updated.State != store.StateDone {
		t.Errorf("State = %q, want done", updated.State)
	}
}

func TestRelevoVerbsDoneErrorPropagates(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{Store: s, Now: func() time.Time { return time.Unix(0, 0) }}
	v := &RelevoVerbs{RT: rt, Planner: mcpTestPlannerA}

	if _, err := v.Done(context.Background(), DoneArgs{Name: "nonexistent"}); err == nil {
		t.Fatal("Done on a binding that does not exist must error")
	}
}
