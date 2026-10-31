package main

import "testing"

// TestRuntimeWiresCloseServedRound pins the served-close hook: a runtime built
// the way the daemon builds one carries the serve implementation, so a hook
// nobody fills can never reach production silently.
func TestRuntimeWiresCloseServedRound(t *testing.T) {
	rt, _ := localPairRuntime(t)
	if rt.CloseServedRound == nil {
		t.Fatal("CloseServedRound is nil: built runtimes must serve the close")
	}
}
