package board

import (
	"os"
	"testing"
)

// TestMain unsets RELEVO_MASTERMIND so no test resolves the MasterMind of
// whoever runs `go test`. A test that needs the variable sets it with
// t.Setenv, which restores the unset state.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("RELEVO_MASTERMIND")
	os.Exit(m.Run())
}
