package hooks

import "time"

type EventType string

const (
	EventStateChanged EventType = "state_changed"
	EventRoundStarted EventType = "round_started"
	// EventForkCreated: OldState holds the source binding's name.
	EventForkCreated EventType = "fork_created"
	// EventBuilderStalled fires once per stall episode; clearing it emits
	// nothing, since this is an observation, not an action.
	EventBuilderStalled EventType = "builder_stalled"
	// EventBindingStale fires once per stale episode, for the same reason.
	EventBindingStale  EventType = "binding_stale"
	EventRoundQueued   EventType = "round_queued"
	EventRoundAdmitted EventType = "round_admitted"
	// EventBurstSecurity fires when the head's node check finds a
	// fingerprint mismatch: the node may be an impersonator.
	EventBurstSecurity EventType = "burst_security"
	// EventBurstNodeLost fires when a burst node stops answering with the
	// head's round still on it: the round may need placing again.
	EventBurstNodeLost EventType = "burst_node_lost"
	// EventBurstAckStuck fires when a node's close of a burst round stays
	// unacked past its window: the head is still waiting on the result.
	EventBurstAckStuck EventType = "burst_ack_stuck"
)

// Event is one state transition or action, with enough context for a hook
// or webhook to act on.
type Event struct {
	Type      EventType
	BindingID string
	State     string
	OldState  string
	Round     int
	Timestamp time.Time
}

// RunLog records one HookRun per hook run or delivery failure; a nil RunLog
// records nothing.
type RunLog interface {
	Append(HookRun) error
}

// HookRun is one recorded run: the event, argv, exit code, error and the
// run's combined output, capped.
type HookRun struct {
	At       time.Time `json:"at"`
	Event    string    `json:"event"`
	Argv     []string  `json:"argv"`
	ExitCode int       `json:"exit_code"`
	Error    string    `json:"error"`
	Output   string    `json:"output"`
}

// Config configures the hook dispatcher: argv lists per event type, and
// where their runs are recorded.
type Config struct {
	Hooks map[string][][]string
	Log   RunLog
}
