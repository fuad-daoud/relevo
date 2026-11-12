package relevo

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// RunningPID answers the process holding b's admission seat: a gate in flight
// answers the gate's pid -- the builder has exited by the time the gate
// starts, and its runner can linger holding the round's resting memory --
// else a live builder answers its own. A DONE or PAUSED binding holds no
// seat either way.
func RunningPID(b store.Binding) (int, bool) {
	if b.Builder.Headless() && b.GateRun != nil && b.State != store.StateDone && b.State != store.StatePaused {
		return b.GateRun.PID, true
	}
	if LiveBuilder(b) {
		return b.Builder.PID, true
	}
	return 0, false
}
