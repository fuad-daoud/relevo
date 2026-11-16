package main

// The verb runner's migrate wiring. The test constructs the runner the hook
// and the cockpit share, which starts no process, so opening the split handle
// is local-file only: no dial, no worker, no network. OpenMigrate is checked
// for presence and never invoked, since invoking it would start a worker.

import (
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestBuildVerbRunnerWiresOpenMigrate pins that the runner the daemon serves
// carries the migrator opener: without it a confirmed migration has no worker
// to run against. The runner is built, not called, so no worker starts here.
func TestBuildVerbRunnerWiresOpenMigrate(t *testing.T) {
	t.Parallel()

	shared, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })

	runner := buildVerbRunner(shared)
	if runner == nil {
		t.Fatal("buildVerbRunner = nil, want the runner the hook serves")
	}
	if runner.OpenMigrate == nil {
		t.Error("runner.OpenMigrate = nil, want the migrator opener")
	}
}
