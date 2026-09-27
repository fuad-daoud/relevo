package mastermind

// State is a mastermind record's host state: the `state` column `relevo mastermind
// list` renders. It is derived, never stored.
type State string

const (
	StateLive State = "live"
	// StateGone means a reused pid or an absent process: not live.
	StateGone     State = "gone"
	StateExplicit State = "-"
)

// RecordState reports rec's host state. A nil procStart, or an error from
// it, means the host cannot be matched, so a host-bearing record reads as
// StateGone.
func RecordState(rec Record, procStart func(pid int) (int64, error)) State {
	if rec.HostPID <= 0 {
		return StateExplicit
	}
	if procStart == nil {
		return StateGone
	}
	startedAt, err := procStart(rec.HostPID)
	if err != nil || startedAt != rec.HostStartedAt {
		return StateGone
	}
	return StateLive
}
