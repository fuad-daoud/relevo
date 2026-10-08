package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestEnableContinuesInDaemonIsTheClientBound pins the decision an enable's
// failure turns into: the client's own wait running out is a join still running
// in the daemon, and anything else is the daemon's refusal. The line it prints
// says where to watch the join and how to resume it.
func TestEnableContinuesInDaemonIsTheClientBound(t *testing.T) {
	if !enableContinuesInDaemon(context.DeadlineExceeded) {
		t.Error("the client's own deadline was read as a refusal")
	}
	wrapped := fmt.Errorf("relevo db sync: the owner did not answer: %w", context.DeadlineExceeded)
	if !enableContinuesInDaemon(wrapped) {
		t.Error("a wrapped deadline was not read as the client's own bound")
	}
	if enableContinuesInDaemon(errors.New("the daemon refused the join")) {
		t.Error("a refusal was read as a join still running")
	}
	if enableContinuesInDaemon(context.Canceled) {
		t.Error("a cancelled caller was read as a join still running")
	}
	for _, want := range []string{"daemon", "status"} {
		if !strings.Contains(enableContinuesLine, want) {
			t.Errorf("the continue line %q does not name %q", enableContinuesLine, want)
		}
	}
}
