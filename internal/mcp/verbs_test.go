package mcp

import (
	"context"
	"fmt"
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

// mcpTestMasterMindA and mcpTestMasterMindB are valid mastermind ids; the status filter keys on them.
const (
	mcpTestMasterMindA = "pl_aaaaaaaabbbb"
	mcpTestMasterMindB = "pl_ccccccccdddd"
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

func TestRelevoVerbsStatusFiltersByMasterMindThenName(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{
		Store: s,
		Now:   func() time.Time { return time.Unix(0, 0) },
	}

	saveVerbBinding(t, s, store.Binding{Name: "mine-a", CWD: "/repo/mine-a", MasterMind: store.Endpoint{PaneID: "w2:p3"}, MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateActive})
	saveVerbBinding(t, s, store.Binding{Name: "mine-done", CWD: "/repo/mine-done", MasterMind: store.Endpoint{PaneID: "w2:p3"}, MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateDone})
	saveVerbBinding(t, s, store.Binding{Name: "other", CWD: "/repo/other", MasterMind: store.Endpoint{PaneID: "w9:p9"}, MasterMindID: mcpTestMasterMindB, Round: 1, State: store.StateActive})

	v := &RelevoVerbs{RT: rt, MasterMind: mcpTestMasterMindA}

	res, err := v.Status(context.Background(), "", StatusArgs{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rep, ok := res.(view.Report)
	if !ok {
		t.Fatalf("result = %#v, want view.Report", res)
	}
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine-a" {
		t.Fatalf("default status = %+v, want only mine-a (this mastermind, DONE hidden)", rep.Bindings)
	}

	res, err = v.Status(context.Background(), "", StatusArgs{All: true})
	if err != nil {
		t.Fatalf("Status all: %v", err)
	}
	rep = res.(view.Report)
	if len(rep.Bindings) != 3 {
		t.Fatalf("all status = %d bindings, want 3", len(rep.Bindings))
	}

	res, err = v.Status(context.Background(), "", StatusArgs{Name: "mine-done"})
	if err != nil {
		t.Fatalf("Status by name: %v", err)
	}
	rep = res.(view.Report)
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine-done" {
		t.Fatalf("status by name = %+v, want only mine-done (DONE included when named)", rep.Bindings)
	}

	if _, err := v.Status(context.Background(), "", StatusArgs{Name: "other"}); err == nil {
		t.Fatal("Status naming a binding on a different mastermind must error")
	}
}

// TestMCPStatusFiltersByMasterMind: the two bindings here share a pane, so only the mastermind id tells them apart.
func TestMCPStatusFiltersByMasterMind(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{
		Store: s,
		Now:   func() time.Time { return time.Unix(0, 0) },
	}

	saveVerbBinding(t, s, store.Binding{Name: "mine", CWD: "/repo/mine", MasterMind: store.Endpoint{PaneID: "w2:p3"}, MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateActive})
	saveVerbBinding(t, s, store.Binding{Name: "cousin", CWD: "/repo/cousin", MasterMind: store.Endpoint{PaneID: "w2:p3"}, MasterMindID: mcpTestMasterMindB, Round: 1, State: store.StateActive})

	v := &RelevoVerbs{RT: rt, MasterMind: mcpTestMasterMindA}
	res, err := v.Status(context.Background(), "", StatusArgs{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rep := res.(view.Report)
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine" {
		t.Fatalf("status = %+v, want only mine (the same pane's cousin is another mastermind)", rep.Bindings)
	}
	if rep.Bindings[0].MasterMindID != mcpTestMasterMindA {
		t.Errorf("row MasterMindID = %q, want %q", rep.Bindings[0].MasterMindID, mcpTestMasterMindA)
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
		MasterMind:       store.Endpoint{PaneID: "w2:p3"},
		Builder:          store.Endpoint{Mode: store.ModeHeadless},
		BuilderCandidate: "agy/test/m",
		Round:            1, State: store.StateActive,
	})
	return &RelevoVerbs{RT: rt, MasterMind: mcpTestMasterMindA}, writeTempPlan(t, "# do the thing")
}

// TestRelevoVerbsSendHeadless covers both of Send's headless paths, replacing
// TestRelevoVerbsSendDryRunHeadless and TestRelevoVerbsSendRealRunHeadless.
func TestRelevoVerbsSendHeadless(t *testing.T) {
	t.Run("dry run takes relevo.SendDryRun, not a seam", func(t *testing.T) {
		v, plan := newHeadlessSendFixture(t)
		res, err := v.Send(context.Background(), "", SendArgs{Name: "webshop", File: plan, DryRun: true})
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
		res, err := v.Send(context.Background(), "", SendArgs{Name: "webshop", File: plan})
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
		MasterMind: store.Endpoint{PaneID: "w2:p3"},
		Builder:    store.Endpoint{Mode: store.ModeHeadless},
		Round:      1, State: store.StateActive,
	})

	v := &RelevoVerbs{RT: rt, MasterMind: mcpTestMasterMindA}
	res, err := v.Done(context.Background(), "", DoneArgs{Name: "webshop"})
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
	v := &RelevoVerbs{RT: rt, MasterMind: mcpTestMasterMindA}

	if _, err := v.Done(context.Background(), "", DoneArgs{Name: "nonexistent"}); err == nil {
		t.Fatal("Done on a binding that does not exist must error")
	}
}

// TestRelevoVerbsStatusResolvesSession pins opencode's path: the call's session
// resolves to that session's mastermind, a resolver error is the tool error,
// and --all needs no identity at all.
func TestRelevoVerbsStatusResolvesSession(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{
		Store: s,
		Now:   func() time.Time { return time.Unix(0, 0) },
	}

	saveVerbBinding(t, s, store.Binding{Name: "mine", CWD: "/repo/mine", MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateActive})
	saveVerbBinding(t, s, store.Binding{Name: "other", CWD: "/repo/other", MasterMindID: mcpTestMasterMindB, Round: 1, State: store.StateActive})

	v := &RelevoVerbs{RT: rt, ResolveSession: func(session string) (string, error) {
		if session != "ses_abc" {
			return "", fmt.Errorf("no relevo MasterMind for opencode session %s", session)
		}
		return mcpTestMasterMindA, nil
	}}

	res, err := v.Status(context.Background(), "ses_abc", StatusArgs{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rep := res.(view.Report)
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "mine" {
		t.Fatalf("status = %+v, want only mine (the resolved session's mastermind)", rep.Bindings)
	}

	if _, err := v.Status(context.Background(), "ses_other", StatusArgs{}); err == nil {
		t.Fatal("a session with no mastermind must surface the resolver error")
	}

	if _, err := v.Status(context.Background(), "ses_other", StatusArgs{All: true}); err != nil {
		t.Fatalf("Status --all must not need a session, got %v", err)
	}
}
