package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestRunningPID pins the seat's process: a gate in flight answers the
// gate's pid with no builder process, a live builder answers its own, both
// answers the gate's, and DONE or PAUSED bindings answer none.
func TestRunningPID(t *testing.T) {
	t.Parallel()

	headless := func(pid int) store.Endpoint {
		return store.Endpoint{Mode: store.ModeHeadless, PID: pid}
	}
	gate := func(pid int) *store.GateRun {
		return &store.GateRun{PID: pid, StartedAt: 1, Command: "make check"}
	}
	tests := []struct {
		name    string
		b       store.Binding
		wantPID int
		wantOK  bool
	}{
		{
			name:    "gate only with no builder process answers the gate",
			b:       store.Binding{Builder: headless(0), GateRun: gate(9001)},
			wantPID: 9001, wantOK: true,
		},
		{
			name:    "builder only answers the builder",
			b:       store.Binding{Builder: headless(4001)},
			wantPID: 4001, wantOK: true,
		},
		{
			name:    "both answers the gate",
			b:       store.Binding{Builder: headless(4001), GateRun: gate(9001)},
			wantPID: 9001, wantOK: true,
		},
		{
			name:   "done answers none",
			b:      store.Binding{Builder: headless(0), GateRun: gate(9001), State: store.StateDone},
			wantOK: false,
		},
		{
			name:   "paused answers none",
			b:      store.Binding{Builder: headless(4001), State: store.StatePaused},
			wantOK: false,
		},
		{
			name:   "queued answers none",
			b:      store.Binding{Builder: headless(0)},
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pid, ok := RunningPID(tc.b)
			if ok != tc.wantOK || pid != tc.wantPID {
				t.Fatalf("RunningPID() = (%d, %v), want (%d, %v)", pid, ok, tc.wantPID, tc.wantOK)
			}
		})
	}
}
