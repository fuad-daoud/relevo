//go:build unix

package main

import (
	"strings"
	"sync/atomic"
	"testing"
)

// TestDBQueryReadsThroughTheOwnerWhenItIsUp pins the route: when an owner is
// already serving the state root's socket, the query reaches it rather than
// opening the file itself. The counting listener is what proves the difference;
// both routes read the same rows.
func TestDBQueryReadsThroughTheOwnerWhenItIsUp(t *testing.T) {
	stateHome := seedQueryRoot(t)
	served := startTestOwner(t, stateHome)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT label FROM probe ORDER BY n`, "--json"})
	})
	if err != nil {
		t.Fatalf("db query through the owner: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), `"one"`) || !strings.Contains(string(stdout), `"two"`) {
		t.Errorf("stdout = %q, want the owner's two rows", stdout)
	}
	if got := atomic.LoadInt32(&served.ln.accepts); got == 0 {
		t.Error("the query read no connection through the owner")
	}
}
