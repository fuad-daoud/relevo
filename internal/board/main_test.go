package board

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain unsets RELEVO_MASTERMIND so no test resolves the MasterMind of
// whoever runs `go test`. A test that needs the variable sets it with
// t.Setenv, which restores the unset state.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("RELEVO_MASTERMIND")
	os.Exit(m.Run())
}

// realDir returns path with symlinks resolved, so a test compares against
// the canonical root the resolver computes.
func realDir(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	return real
}
