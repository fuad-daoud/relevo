package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestStatusChainsRejectsLineAndName(t *testing.T) {
	cases := [][]string{
		{"status", "--chains", "--line"},
		{"status", "--chains", "--name", "mychain"},
		{"status", "--chains", "positional-target"},
		{"status", "--line", "--chains"},
	}

	for _, args := range cases {
		err := run(args)
		if err == nil {
			t.Errorf("run(%v) = nil, want usage error", args)
			continue
		}
		requireCLIError(t, err, codeUsage, "")
	}
}

func TestStatusChainsJSONRoundTrips(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)

	fixedTS := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	rootChain := db.ChainRow{
		ID:        db.NewID(),
		Name:      "parent-chain",
		Status:    "halted",
		Reason:    "reviewer requested changes",
		Phase:     "build",
		Step:      "reviewing",
		Plan:      1,
		Plans:     3,
		Builder:   "parent-b",
		CreatedAt: fixedTS,
		UpdatedAt: fixedTS,
	}
	childChain := db.ChainRow{
		ID:        db.NewID(),
		Name:      "child-chain",
		Parent:    "parent-chain",
		Status:    "running",
		Phase:     "build",
		Step:      "building",
		Plan:      1,
		Plans:     2,
		Builder:   "child-b",
		CreatedAt: fixedTS,
		UpdatedAt: fixedTS,
	}

	if err := s.WithLock(func(tx *store.Tx) error {
		if err := tx.ChainPut(rootChain); err != nil {
			return err
		}
		return tx.ChainPut(childChain)
	}); err != nil {
		t.Fatalf("seed chains: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"status", "--all-masterminds", "--chains", "--json"})
	})
	if err != nil {
		t.Fatalf("run status --chains --json: %v (stderr: %s)", err, stderr)
	}

	var doc relevo.ChainsDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("json.Unmarshal: %v\nstdout was:\n%s", err, stdout)
	}

	if len(doc.Chains) != 2 {
		t.Fatalf("got %d chains, want 2", len(doc.Chains))
	}
	if doc.Chains[0].Name != "parent-chain" || doc.Chains[0].Depth != 0 {
		t.Errorf("chain[0] = %+v, want parent-chain at depth 0", doc.Chains[0])
	}
	if doc.Chains[1].Name != "child-chain" || doc.Chains[1].Depth != 1 || doc.Chains[1].Parent != "parent-chain" {
		t.Errorf("chain[1] = %+v, want child-chain at depth 1 under parent-chain", doc.Chains[1])
	}

	// Text rendering check
	textOut, textErr, err := captureOutput(t, func() error {
		return run([]string{"status", "--all-masterminds", "--chains"})
	})
	if err != nil {
		t.Fatalf("run status --chains: %v (stderr: %s)", err, textErr)
	}

	outStr := string(textOut)
	if !strings.Contains(outStr, "parent-chain  halted  reviewing · plans 1/3") {
		t.Errorf("textOut missing parent row:\n%s", outStr)
	}
	if !strings.Contains(outStr, "  reviewer requested changes") {
		t.Errorf("textOut missing halt reason on following line:\n%s", outStr)
	}
	if !strings.Contains(outStr, "  child-chain  running  building · plans 1/2") {
		t.Errorf("textOut missing indented child row:\n%s", outStr)
	}
}

// TestStatusChainsTextSanitizesChainFields pins Finding 1 on the CLI side: the
// mirrored chain status, step and halt reason come from a peer server, and
// status --chains prints them straight to a terminal. The status word here is
// one the engine does not recognise, so it reaches printChainsStatus whole.
// Mutation: drop the three sanitize.Text calls in status.go.
// The run reads only the local store: no remote is configured in this fixture,
// so the test spawns no harness process and touches no network.
func TestStatusChainsTextSanitizesChainFields(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)

	fixedTS := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	row := db.ChainRow{
		ID:        db.NewID(),
		Name:      "hostile-chain",
		Status:    "run\ning\x1b[2J",
		Reason:    "reviewer said nobell",
		Phase:     "build",
		Step:      "review\rstep",
		Plan:      1,
		Plans:     1,
		Builder:   "hostile-b",
		CreatedAt: fixedTS,
		UpdatedAt: fixedTS,
	}
	if err := s.WithLock(func(tx *store.Tx) error { return tx.ChainPut(row) }); err != nil {
		t.Fatalf("seed chain: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"status", "--all-masterminds", "--chains"})
	})
	if err != nil {
		t.Fatalf("run status --chains: %v (stderr: %s)", err, stderr)
	}

	outStr := string(stdout)
	if !strings.Contains(outStr, "hostile-chain") {
		t.Fatalf("the chain row is not on stdout, so nothing was pinned:\n%s", outStr)
	}
	for _, r := range []rune{'\r', '\a', '\x1b'} {
		if strings.ContainsRune(outStr, r) {
			t.Errorf("status --chains stdout still carries %q:\n%q", r, outStr)
		}
	}

	// The JSON shape is untouched: the bytes stay raw there, since that output
	// is a machine contract rather than something a terminal draws.
	jsonOut, _, err := captureOutput(t, func() error {
		return run([]string{"status", "--all-masterminds", "--chains", "--json"})
	})
	if err != nil {
		t.Fatalf("run status --chains --json: %v", err)
	}
	if !strings.Contains(string(jsonOut), "review\\rstep") {
		t.Errorf("the JSON output must keep the raw bytes:\n%s", jsonOut)
	}
}

// TestStatusChainsExhaustedPlansReadsLastPlan pins the chains-doc reader at the
// CLI surface: a chain whose plans walk is exhausted reads its last plan, so
// `status --chains` prints 6/6 rather than the reset 1/6.
// Mutation: revert chainDocStepsAndFacts to the raw iterator.
// The run reads only the local store: no remote is configured in this fixture,
// so the test spawns no harness process and touches no network.
func TestStatusChainsExhaustedPlansReadsLastPlan(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)

	fixedTS := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	const workflowYAML = `name: exhausted
inputs: { plans: required }
start: plans
steps:
  plans: { for-each: plans, on: { next: scan, empty: done } }
  scan: { run: lite-planner, seed: "scan it", on: { done: done } }
`
	// The plans walk ran out: the same -1 index a walk that never started
	// carries, and only Done tells them apart.
	state, err := json.Marshal(map[string]any{
		"status": "done",
		"at":     "done",
		"iter": map[string]any{
			"plans": map[string]any{
				"index": -1,
				"done":  true,
				"items": []string{"p1", "p2", "p3", "p4", "p5", "p6"},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal the state: %v", err)
	}
	row := db.ChainRow{
		ID: db.NewID(), Name: "spent-chain", Status: "done",
		Phase: "build", Step: "reviewing", Plan: 6, Plans: 6,
		WorkflowJSON: []byte(workflowYAML), StateJSON: state,
		Builder: "spent-b", CreatedAt: fixedTS, UpdatedAt: fixedTS,
	}
	if err := s.WithLock(func(tx *store.Tx) error { return tx.ChainPut(row) }); err != nil {
		t.Fatalf("seed chain: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"status", "--all-masterminds", "--chains"})
	})
	if err != nil {
		t.Fatalf("run status --chains: %v (stderr: %s)", err, stderr)
	}
	outStr := string(stdout)
	if !strings.Contains(outStr, "spent-chain") {
		t.Fatalf("the chain row is not on stdout, so nothing was pinned:\n%s", outStr)
	}
	if !strings.Contains(outStr, "plans 6/6") {
		t.Errorf("an exhausted plans walk reads as its reset position:\n%s", outStr)
	}
	if strings.Contains(outStr, "plans 1/6") {
		t.Errorf("the reset position is still on stdout:\n%s", outStr)
	}
}
