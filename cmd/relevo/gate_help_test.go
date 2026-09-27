package main

import (
	"errors"
	"strings"
	"testing"
)

// TestGateHelpNamesTheForPlaceholder pins the name the flag package gives
// --for's value. flag.UnquoteUsage takes the first back-quoted word in a usage
// string as the value's placeholder, so a quoted phrase in the text is printed
// as if it named the value; the test drives the verb's own help path, which is
// what a reader sees. -h returns errHelpShown before cmdGate builds a runtime,
// so no harness is reached and no state is written.
func TestGateHelpNamesTheForPlaceholder(t *testing.T) {
	_, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"gate", "-h"})
	})
	if !errors.Is(runErr, errHelpShown) {
		t.Fatalf("run(gate -h) = %v, want errHelpShown", runErr)
	}

	if !strings.Contains(string(stderr), "-for duration") {
		t.Errorf("gate -h stderr = %q, want it to contain %q", string(stderr), "-for duration")
	}
	if strings.Contains(string(stderr), "-for relevo") {
		t.Errorf("gate -h stderr = %q: --for's value must be named by a placeholder, not by the usage sentence", string(stderr))
	}
}
