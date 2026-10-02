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
		return run([]string{"status", "--chains", "--json"})
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
		return run([]string{"status", "--chains"})
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
