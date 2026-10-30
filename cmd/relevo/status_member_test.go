package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// seedDoneChainMemberFixture seeds one finished chain whose planner member is
// still NEEDS YOU locally: the stale-row shape the waiting notice reads. The
// notice scans raw records, so the member is listed there, while every status
// surface collapses members into the chain's own row -- which is hidden, being
// DONE. The planner's builder points at a remote server that is never
// contacted (Remote stays nil, no harness, no network) and a plain binding
// beside them is what a `relevo done` fires its notice on.
func seedDoneChainMemberFixture(t *testing.T) statusFixture {
	t.Helper()
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	root, err := tmpdir.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)

	reg := mastermindRegistryAt(t, stateHome)
	rec, err := reg.Create(mastermind.Record{
		ID: "pl_aaaabbbbcccc", Name: "architect-1", HarnessKind: "claude", SessionID: "sess-member",
		CWD: filepath.Join(root, "mastermind"),
	})
	if err != nil {
		t.Fatalf("mastermind Create: %v", err)
	}

	fixedTS := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	chain := db.ChainRow{
		ID:            db.NewID(),
		Name:          "ch-transcr",
		Status:        "done",
		Phase:         "build",
		Step:          "reviewing",
		Plan:          1,
		Plans:         1,
		PlanPathsJSON: []byte(`["/plans/001.md"]`),
		SettingsJSON:  []byte(`{"max_corrections":1}`),
		Builder:       "ch-transcr",
		Planner:       "ch-transcr-plan",
		Worktree:      filepath.Join(root, "work", "ch-transcr"),
		MasterMindID:  rec.ID,
		CreatedAt:     fixedTS,
		UpdatedAt:     fixedTS,
	}
	members := []store.Binding{
		{Name: "ch-transcr", CWD: filepath.Join(root, "work", "ch-transcr"), Round: 1, State: store.StateDone, MasterMindID: rec.ID},
		{
			Name: "ch-transcr-plan", CWD: filepath.Join(root, "work", "ch-transcr-plan"), Round: 2,
			State: store.StateNeedsYou,
			Halt:  "round 2 has run past 2h0m0s", HaltAt: fixedTS,
			Role:  "lite-planner",
			Shape: store.ShapeReader, MasterMindID: rec.ID,
			Builder: store.Endpoint{Mode: store.ModeRemote, Server: "relevo.example.test"},
		},
		{Name: "plain", CWD: filepath.Join(root, "work", "plain"), Round: 1, State: store.StateActive, MasterMindID: rec.ID},
	}
	if err := s.WithLock(func(tx *store.Tx) error { return tx.CreateChain(chain, members) }); err != nil {
		t.Fatalf("CreateChain: %v", err)
	}

	return statusFixture{roots: []string{root}, mastermindID: rec.ID}
}

// TestStatusNameOnAChainMemberAnswersTheChain pins the named-member fix: a
// chain member's name -- the one the waiting notice names -- is answered with
// its chain's report, which lists the chain row with its members under it, in
// the --name and the positional spelling alike. No harness spawns and nothing
// reaches the network: Remote stays nil throughout.
func TestStatusNameOnAChainMemberAnswersTheChain(t *testing.T) {
	fx := seedDoneChainMemberFixture(t)

	for _, args := range [][]string{
		{"status", "--name", "ch-transcr-plan", "--json"},
		{"status", "ch-transcr-plan"},
	} {
		stdout, stderr, err := captureOutput(t, func() error { return run(args) })
		if err != nil {
			t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
		}
		if got := string(normalize(stdout, fx.roots...)); !strings.Contains(got, "ch-transcr-plan") {
			t.Errorf("%v: status output =\n%s\nwant the member named", args, got)
		}
	}
}

// TestWaitingNoticeHintsAClearableChainMember pins the amended notice: a row
// whose owning chain is finished hints both the look and the clear, in the
// order a person runs them. A plain sibling's done fires the notice without
// spawning any process it does not already own.
func TestWaitingNoticeHintsAClearableChainMember(t *testing.T) {
	seedDoneChainMemberFixture(t)

	_, stderr, err := captureOutput(t, func() error { return run([]string{"done", "plain"}) })
	if err != nil {
		t.Fatalf("done plain: %v (stderr: %s)", err, stderr)
	}
	want := "(relevo status --name ch-transcr-plan; relevo done ch-transcr-plan clears it)"
	if got := string(stderr); !strings.Contains(got, want) {
		t.Errorf("notice = %q\nwant it to contain %q", got, want)
	}
}
