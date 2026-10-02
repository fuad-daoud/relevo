package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
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

// seedVerbChain writes one chain row and its three member bindings into s: a
// store-only fixture, so the done-routing tests need no harness and no network.
func seedVerbChain(t *testing.T, s *store.Store, name, status string) {
	t.Helper()

	now := time.Unix(0, 0).UTC()
	c := db.ChainRow{
		ID: db.NewID(), Name: name, Status: status, Phase: "build", Step: "building",
		Plan: 1, Plans: 1, PlanPathsJSON: []byte(`["/p/plan-1.md"]`), SettingsJSON: []byte(`{}`),
		AwaitingMember: "builder", AwaitingRound: 1,
		Builder: name, Reviewer: name + "-rev", Planner: name + "-plan",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.WithLock(func(tx *store.Tx) error {
		for _, m := range []store.Binding{
			{Name: name, CWD: "/repo", Round: 1, State: store.StateActive},
			{Name: name + "-rev", CWD: "/repo", Round: 1, State: store.StateActive, Shape: store.ShapeReader},
			{Name: name + "-plan", CWD: "/repo", Round: 1, State: store.StateActive, Shape: store.ShapeReader},
		} {
			if err := tx.Save(m); err != nil {
				return err
			}
		}
		return tx.ChainPut(c)
	}); err != nil {
		t.Fatalf("seed chain %s: %v", name, err)
	}
	// A chain runs on the engine now: convert the seeded legacy row, so its own
	// verbs read an engine state rather than the old fixed machine.
	rt := relevo.Runtime{Store: s, Now: func() time.Time { return now }}
	if err := relevo.ConvertLegacyChains(rt); err != nil {
		t.Fatalf("convert chain %s: %v", name, err)
	}
}

// TestRelevoVerbsDoneOnAChainReleasesEveryMember pins the route: done on a name
// that is a chain calls relevo.ChainDone, so every member is released and the
// chain is marked done -- the same one row status shows for the chain.
func TestRelevoVerbsDoneOnAChainReleasesEveryMember(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{Store: s, Now: func() time.Time { return time.Unix(0, 0) }}
	seedVerbChain(t, s, "shop", "stopped")

	v := &RelevoVerbs{RT: rt, MasterMind: mcpTestMasterMindA}
	res, err := v.Done(context.Background(), "", DoneArgs{Name: "shop"})
	if err != nil {
		t.Fatalf("Done on a chain name: %v", err)
	}
	dr, ok := res.(doneResult)
	if !ok {
		t.Fatalf("result = %#v, want doneResult", res)
	}
	if dr.Text == "" {
		t.Error("Text must be set from relevo.DoneText")
	}

	for _, member := range []string{"shop", "shop-rev", "shop-plan"} {
		b, err := s.Load(member)
		if err != nil {
			t.Fatalf("Load %s: %v", member, err)
		}
		if b.State != store.StateDone {
			t.Errorf("member %s state = %q, want done", member, b.State)
		}
	}
	row, err := s.Chain("shop")
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if row.Status != "done" {
		t.Errorf("chain status = %q, want done", row.Status)
	}
}

// TestRelevoVerbsDoneRefusedOnARunningChain pins both refusals through the tool:
// done on a running chain's name is the chain refusal, done on one of its
// members is the running-chain-member refusal, and neither marks anything done.
func TestRelevoVerbsDoneRefusedOnARunningChain(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{Store: s, Now: func() time.Time { return time.Unix(0, 0) }}
	seedVerbChain(t, s, "shop", "running")

	v := &RelevoVerbs{RT: rt, MasterMind: mcpTestMasterMindA}

	if _, err := v.Done(context.Background(), "", DoneArgs{Name: "shop"}); err == nil {
		t.Fatal("Done on a running chain = nil, want a refusal")
	} else if !strings.Contains(err.Error(), "relevo stop shop first") {
		t.Errorf("err = %v, want it to name `relevo stop shop first`", err)
	}

	if _, err := v.Done(context.Background(), "", DoneArgs{Name: "shop-rev"}); !errors.Is(err, relevo.ErrRunningChainMember) {
		t.Errorf("Done on a running chain's member = %v, want ErrRunningChainMember", err)
	}

	for _, member := range []string{"shop", "shop-rev", "shop-plan"} {
		b, err := s.Load(member)
		if err != nil {
			t.Fatalf("Load %s: %v", member, err)
		}
		if b.State == store.StateDone {
			t.Errorf("member %s was marked done by a refused call", member)
		}
	}
}

// TestRelevoVerbsStatusResolvesSession pins opencode's path: the call's session
// resolves to that session's mastermind, a resolver error is the tool error, a
// resolver that yields "" with a nil error is an error naming the session, and
// --all needs no identity at all.
func TestRelevoVerbsStatusResolvesSession(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{
		Store: s,
		Now:   func() time.Time { return time.Unix(0, 0) },
	}

	saveVerbBinding(t, s, store.Binding{Name: "mine", CWD: "/repo/mine", MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateActive})
	saveVerbBinding(t, s, store.Binding{Name: "other", CWD: "/repo/other", MasterMindID: mcpTestMasterMindB, Round: 1, State: store.StateActive})

	v := &RelevoVerbs{RT: rt, ResolveSession: func(session string) (string, error) {
		switch session {
		case "ses_abc":
			return mcpTestMasterMindA, nil
		case "ses_empty":
			return "", nil
		default:
			return "", fmt.Errorf("no relevo MasterMind for opencode session %s", session)
		}
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

	if _, err := v.Status(context.Background(), "ses_empty", StatusArgs{}); err == nil || !strings.Contains(err.Error(), "ses_empty") {
		t.Fatalf("a session resolving to \"\" must error naming it, got %v", err)
	}

	if _, err := v.Status(context.Background(), "ses_other", StatusArgs{All: true}); err != nil {
		t.Fatalf("Status --all must not need a session, got %v", err)
	}
}

// TestRelevoVerbsStatusWithoutIdentityErrors pins the Claude server with no
// record and no session: the identity would be "", so status errors naming the
// fix instead of filtering every binding away, and returns no report. all:true
// still needs no identity.
func TestRelevoVerbsStatusWithoutIdentityErrors(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{Store: s, Now: func() time.Time { return time.Unix(0, 0) }}
	saveVerbBinding(t, s, store.Binding{Name: "mine", CWD: "/repo/mine", MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateActive})

	v := &RelevoVerbs{RT: rt}
	res, err := v.Status(context.Background(), "", StatusArgs{})
	if err == nil || !strings.Contains(err.Error(), "relevo mastermind init") {
		t.Fatalf("Status error = %v, want it to name relevo mastermind init", err)
	}
	if res != nil {
		t.Errorf("result = %#v, want nil when the identity is empty", res)
	}

	if _, err := v.Status(context.Background(), "", StatusArgs{All: true}); err != nil {
		t.Fatalf("Status --all must not need an identity, got %v", err)
	}
}

// newShowVerbStore seeds a live binding "webshop" with two rounds: round 1
// closed with a report, round 2 the open round (its prompt only). Round 1 is
// therefore the newest completed round.
func newShowVerbStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.New(t.TempDir())
	saveVerbBinding(t, s, store.Binding{Name: "webshop", CWD: "/repo", Round: 2, State: store.StateActive})

	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(s.PromptPath("webshop", 1), "# Round 1 plan\n")
	write(s.ReportPath("webshop", 1), "# Round 1 report\n")
	write(s.PromptPath("webshop", 2), "# Round 2 plan\n")

	for _, e := range []store.LogEntry{
		{TS: time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{TS: time.Date(2026, 9, 29, 9, 1, 0, 0, time.UTC), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true},
		{TS: time.Date(2026, 9, 29, 9, 2, 0, 0, time.UTC), Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
	} {
		if err := s.AppendLog("webshop", e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	return s
}

// TestRelevoVerbsShowSectionAndRound covers show's three shapes: the prompt
// default on the newest completed round, a named section and round, and an
// open round's prompt named explicitly.
func TestRelevoVerbsShowSectionAndRound(t *testing.T) {
	s := newShowVerbStore(t)
	v := &RelevoVerbs{RT: relevo.Runtime{Store: s, Now: func() time.Time { return time.Unix(0, 0) }}}

	res, err := v.Show(context.Background(), "", ShowArgs{Name: "webshop"})
	if err != nil {
		t.Fatalf("Show default: %v", err)
	}
	sr, ok := res.(relevo.ShowResult)
	if !ok {
		t.Fatalf("result = %#v, want relevo.ShowResult", res)
	}
	if sr.Round != 1 || sr.Section != relevo.ShowPrompt {
		t.Errorf("default = round %d section %q, want round 1 prompt", sr.Round, sr.Section)
	}
	if sr.Text != "# Round 1 plan\n" {
		t.Errorf("default text = %q, want round 1's plan", sr.Text)
	}

	res, err = v.Show(context.Background(), "", ShowArgs{Name: "webshop", Round: 1, Section: "report"})
	if err != nil {
		t.Fatalf("Show report: %v", err)
	}
	sr, ok = res.(relevo.ShowResult)
	if !ok {
		t.Fatalf("report result = %#v, want relevo.ShowResult", res)
	}
	if sr.Section != relevo.ShowReport || sr.Round != 1 || sr.Text != "# Round 1 report\n" {
		t.Errorf("report = round %d section %q text %q, want round 1's report", sr.Round, sr.Section, sr.Text)
	}

	res, err = v.Show(context.Background(), "", ShowArgs{Name: "webshop", Round: 2, Section: "prompt"})
	if err != nil {
		t.Fatalf("Show round 2 prompt: %v", err)
	}
	sr, ok = res.(relevo.ShowResult)
	if !ok {
		t.Fatalf("round 2 result = %#v, want relevo.ShowResult", res)
	}
	if sr.Round != 2 || sr.Text != "# Round 2 plan\n" {
		t.Errorf("round 2 = round %d text %q, want the open round's plan", sr.Round, sr.Text)
	}

	if _, err := v.Show(context.Background(), "", ShowArgs{Name: "webshop", Section: "bogus"}); err == nil {
		t.Error("Show with an invalid section must error")
	}
}

// assertGateSetDoc checks a set document: the provider, the candidate count,
// the mode, the expiry `until` names and the absence of removed.
func assertGateSetDoc(t *testing.T, doc gateDoc, provider string, candidates int, until time.Time) {
	t.Helper()
	if doc.Subject != provider || doc.Candidates != candidates || doc.Mode != "gated" {
		t.Errorf("set doc = %+v, want subject %s, %d candidates, mode gated", doc, provider, candidates)
	}
	if want := until.Format(time.RFC3339); doc.Until != want {
		t.Errorf("until = %q, want %q", doc.Until, want)
	}
	if doc.Removed != nil {
		t.Errorf("a set document omits removed, got %d", *doc.Removed)
	}
}

// TestRelevoVerbsGateSetAndClear drives both gate routes against a seeded
// candidate set and a real gates database: the ledger entry a set writes, and
// the removed count a clear reports and prints.
func TestRelevoVerbsGateSetAndClear(t *testing.T) {
	s := store.New(t.TempDir())
	set := writeCandidates(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]},
		{"harness":"claude","provider":"test","model":"n","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]}]`)
	kv := testGateKV(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	rt := relevo.Runtime{Store: s, Candidates: set, Gates: kv, Now: func() time.Time { return now }}
	v := &RelevoVerbs{RT: rt, MasterMind: mcpTestMasterMindA}

	res, err := v.Gate(context.Background(), "", GateArgs{Token: "agy/test/m", For: "2h", Reason: "429 from the provider"})
	if err != nil {
		t.Fatalf("Gate set: %v", err)
	}
	doc, ok := res.(gateDoc)
	if !ok {
		t.Fatalf("result = %#v, want gateDoc", res)
	}
	assertGateSetDoc(t, doc, "test", 2, now.Add(2*time.Hour))

	ledger, err := availability.LoadLedger(kv)
	if err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	if len(ledger.Entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1: %+v", len(ledger.Entries), ledger.Entries)
	}
	e := ledger.Entries[0]
	if e.Kind != availability.RateLimited || e.Subject != "test" {
		t.Errorf("ledger entry = %+v, want a rate_limited gate on test", e)
	}
	if !e.Until.Equal(now.Add(2 * time.Hour)) {
		t.Errorf("entry until = %v, want %v (for -> until)", e.Until, now.Add(2*time.Hour))
	}
	if e.Note != "429 from the provider" {
		t.Errorf("entry note = %q, want the reason", e.Note)
	}

	res, err = v.Gate(context.Background(), "", GateArgs{Token: "test", Clear: true})
	if err != nil {
		t.Fatalf("Gate clear: %v", err)
	}
	cleared, ok := res.(gateDoc)
	if !ok {
		t.Fatalf("clear result = %#v, want gateDoc", res)
	}
	if cleared.Removed == nil || *cleared.Removed != 1 {
		t.Fatalf("clear removed = %v, want 1", cleared.Removed)
	}
	if cleared.Subject != "test" || cleared.Until != "" || cleared.Candidates != 2 || cleared.Mode != "gated" {
		t.Errorf("clear doc = %+v, want subject test, no until, 2 candidates, mode gated", cleared)
	}
	raw, err := json.Marshal(cleared)
	if err != nil {
		t.Fatalf("marshal clear doc: %v", err)
	}
	if !strings.Contains(string(raw), `"removed"`) {
		t.Errorf("cleared JSON = %s, want a removed field", raw)
	}

	ledger, err = availability.LoadLedger(kv)
	if err != nil {
		t.Fatalf("LoadLedger after clear: %v", err)
	}
	if len(ledger.Entries) != 0 {
		t.Errorf("ledger after clear = %+v, want empty", ledger.Entries)
	}
}

// TestRelevoVerbsGateRejectsNonPositiveFor pins that a zero or negative
// duration is refused before anything is written.
func TestRelevoVerbsGateRejectsNonPositiveFor(t *testing.T) {
	s := store.New(t.TempDir())
	set := writeCandidates(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)
	kv := testGateKV(t)
	rt := relevo.Runtime{Store: s, Candidates: set, Gates: kv, Now: func() time.Time { return time.Unix(0, 0) }}
	v := &RelevoVerbs{RT: rt, MasterMind: mcpTestMasterMindA}

	for _, forFlag := range []string{"0s", "-1h"} {
		if _, err := v.Gate(context.Background(), "", GateArgs{Token: "agy/test/m", For: forFlag}); err == nil {
			t.Errorf("Gate with for %q must error", forFlag)
		}
	}

	ledger, err := availability.LoadLedger(kv)
	if err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	if len(ledger.Entries) != 0 {
		t.Errorf("a refused for must write nothing, got %+v", ledger.Entries)
	}
}
