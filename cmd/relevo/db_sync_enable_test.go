package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// TestEnableContinuesInDaemonIsTheClientBound pins the decision an enable's
// failure turns into: the client's own bound running out while it waited for
// the daemon's reply is a join still running, and anything that failed before
// the frame was fully sent is the daemon's refusal. The line it prints says
// where to watch the join and how to resume it.
func TestEnableContinuesInDaemonIsTheClientBound(t *testing.T) {
	// The client's own error after the frame reached the daemon: the verb was
	// written and the reply did not arrive before the caller's deadline.
	awaiting := fmt.Errorf("db: sync verb enable: %w: %w", client.ErrAwaitingReply, context.DeadlineExceeded)

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"the frame was sent and the reply wait timed out", awaiting, true},
		{"a reply-wait deadline wrapped once more", fmt.Errorf("relevo db sync: %w", awaiting), true},
		{"a connect stall before the frame was sent", fmt.Errorf("db: dial the owner: %w", context.DeadlineExceeded), false},
		{"a handshake that timed out", fmt.Errorf("db: sync verb enable: %w", context.DeadlineExceeded), false},
		{"a partially written frame", fmt.Errorf("db: sync verb enable: %w", errors.New("write: broken pipe")), false},
		{"a cancelled caller waiting for the reply", fmt.Errorf("db: sync verb enable: %w: %w", client.ErrAwaitingReply, context.Canceled), false},
		{"a refusal", errors.New("the daemon refused the join"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := enableContinuesInDaemon(tc.err); got != tc.want {
				t.Errorf("enableContinuesInDaemon(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}

	for _, want := range []string{"daemon", "status"} {
		if !strings.Contains(enableContinuesLine, want) {
			t.Errorf("the continue line %q does not name %q", enableContinuesLine, want)
		}
	}
}
