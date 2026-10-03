package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
)

// seedChainContractFixture seeds one mastermind-owned chain beside a binding
// that belongs to no chain. The chain is halted with one correction spent, its
// builder member is named for the chain (the design's `<n>` member) and its
// reviewer sits under it, so one fixture pins the replacement in the ordinary
// listing and the members the named view lists. Every path lives under the
// store's own root, so normalizing that one root cleans the output.
func seedChainContractFixture(t *testing.T) statusFixture {
	t.Helper()
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)

	reg := mastermindRegistryAt(t, stateHome)
	rec, err := reg.Create(mastermind.Record{
		ID: "pl_aaaabbbbcccc", Name: "architect-1", HarnessKind: "claude", SessionID: "sess-chain",
		CWD: filepath.Join(root, "mastermind"),
	})
	if err != nil {
		t.Fatalf("mastermind Create: %v", err)
	}

	fixedTS := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	chain := db.ChainRow{
		ID:             db.NewID(),
		Name:           "x",
		Status:         "halted",
		Reason:         "reviewer still wants changes after 1 corrections",
		Phase:          "build",
		Step:           "reviewing",
		Plan:           2,
		Plans:          4,
		Corrections:    1,
		PlanPathsJSON:  []byte(`["/plans/001.md","/plans/002.md","/plans/003.md","/plans/004.md"]`),
		SettingsJSON:   []byte(`{"max_corrections":1}`),
		AwaitingMember: "reviewer",
		AwaitingRound:  2,
		Builder:        "x",
		Reviewer:       "x-rev",
		Planner:        "x-plan",
		Security:       "x-sec",
		Worktree:       filepath.Join(root, "work", "x"),
		MasterMindID:   rec.ID,
		CreatedAt:      fixedTS,
		UpdatedAt:      fixedTS,
	}
	members := []store.Binding{
		{Name: "x", CWD: filepath.Join(root, "work", "x"), Round: 2, State: store.StateActive, MasterMindID: rec.ID},
		{
			Name: "x-rev", CWD: filepath.Join(root, "work", "x-rev"), Round: 2, State: store.StateActive,
			Role: "reviewer", Shape: store.ShapeReader, MasterMindID: rec.ID,
		},
		{Name: "plain", CWD: filepath.Join(root, "work", "plain"), Round: 1, State: store.StateActive, MasterMindID: rec.ID},
	}
	if err := s.WithLock(func(tx *store.Tx) error { return tx.CreateChain(chain, members) }); err != nil {
		t.Fatalf("CreateChain: %v", err)
	}

	return statusFixture{roots: []string{root}, mastermindID: rec.ID}
}

// TestStatusChainContract pins the chain surfaces through the CLI: the
// ordinary listing replaces the chain's member rows with one chain row, and
// the named chain view lists the chain row with its members under it.
func TestStatusChainContract(t *testing.T) {
	fx := seedChainContractFixture(t)

	for _, c := range []struct {
		golden string
		args   []string
	}{
		{"status-chain", []string{"status", "--all-masterminds", "--json"}},
		{"status-chain-name", []string{"status", "--name", "x", "--json"}},
	} {
		stdout, stderr, err := captureOutput(t, func() error { return run(c.args) })
		if err != nil {
			t.Fatalf("%v: %v (stderr: %s)", c.args, err, stderr)
		}
		assertGolden(t, c.golden, normalize(stdout, fx.roots...))
	}
}

// TestStatusChainLineContract pins the statusline path: the chain's mastermind
// reads one entry per chain, in place of the members' rows, with the chain
// field in the JSON document.
func TestStatusChainLineContract(t *testing.T) {
	fx := seedChainContractFixture(t)
	t.Setenv("RELEVO_MASTERMIND", fx.mastermindID)

	for _, c := range []struct {
		golden string
		args   []string
	}{
		{"statusline-chain", []string{"status", "--line"}},
		{"statusline-chain-json", []string{"status", "--line", "--json"}},
	} {
		stdout, stderr, err := captureOutput(t, func() error { return run(c.args) })
		if err != nil {
			t.Fatalf("%v: %v (stderr: %s)", c.args, err, stderr)
		}
		assertGolden(t, c.golden, normalize(stdout, fx.roots...))
	}
}
