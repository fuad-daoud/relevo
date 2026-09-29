package store

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/usage"
)

// StreamSegment is one process's byte range in a round's builder stream.
type StreamSegment struct {
	// Start equals the stream's size when the process was spawned.
	Start int64 `json:"start"`
	// Kind is Endpoint.Kind at spawn.
	Kind string `json:"kind"`
}

// Endpoint is one side of a binding. SessionID is the durable identity; PaneID
// moves when a pane is moved between workspaces.
//
// A headless builder has no session: Mode is ModeHeadless, SessionID is "",
// and the process fields below describe the current round's process -- PID 0
// between rounds. Every one of them is omitempty so an endpoint written before
// these fields existed is byte-identical to what it was.
type Endpoint struct {
	AgentName string `json:"agent_name,omitempty"`
	PaneID    string `json:"pane_id"`
	SessionID string `json:"session_id,omitempty"`
	Kind      string `json:"kind"`

	Mode Mode `json:"mode,omitempty"`
	// PID is the headless supervisor's pid; 0 when no process is running.
	PID int `json:"pid,omitempty"`
	// StartedAt is the process's start time in Unix seconds, for pid-reuse
	// defence. Seconds, not time.Time: encoding/json never omits a struct.
	StartedAt int64 `json:"started_at,omitempty"`
	// LogPath is the file the current process's stderr is appended to; ""
	// between rounds.
	LogPath string `json:"log_path,omitempty"`
	// StreamRound and StreamOffset belong to the round's file, not to the
	// process or to Binding.Round: a mid-round switch and finishRound keep
	// them, and only startRound on a later round moves them.
	StreamRound  int   `json:"stream_round,omitempty"`
	StreamOffset int64 `json:"stream_offset,omitempty"`
	// StreamStart is the stream's byte length when this process was spawned; 0
	// for a round's first process.
	StreamStart int64 `json:"stream_start,omitempty"`
	// StreamSegments is one entry per process spawned in StreamRound, in spawn
	// order; the drain renders each line with the Kind of the last segment
	// whose Start <= the line's offset. Empty for a round started before
	// segments existed; the drain then uses Kind.
	StreamSegments []StreamSegment `json:"stream_segments,omitempty"`

	// StreamSessionID is set once per round by drainStream from the harness's
	// own event; startRound clears it, because a new process begins a new
	// session.
	StreamSessionID string `json:"stream_session_id,omitempty"`

	Server       string `json:"server,omitempty"`
	LastShipped  string `json:"last_shipped,omitempty"`
	LastKnown    string `json:"last_known,omitempty"`
	RemoteStatus string `json:"remote_status,omitempty"`
	// RemoteQueue is what the server's last GET said while the round was
	// queued; nil in every other round state.
	RemoteQueue *QueueFacts `json:"remote_queue,omitempty"`
	RemoteLive  *LiveFacts  `json:"remote_live,omitempty"`

	// TranscriptLocator is the harness's own transcript path for this
	// endpoint's session, resolved at bind time when possible; "" when it
	// could not be resolved.
	TranscriptLocator string `json:"transcript_locator,omitempty"`
}

// QueueFacts is what the server's last GET said about a queued round's place
// in its builder queue.
type QueueFacts struct {
	Position int       `json:"position"` // 1-based
	Ahead    int       `json:"ahead"`
	Running  int       `json:"running"`
	Cap      int       `json:"cap"`
	Since    time.Time `json:"since"`
}

type DiffFacts struct {
	Files   int `json:"files"`
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// LiveFacts is copied from the server's view on each running poll; nil in
// every other round state.
type LiveFacts struct {
	At             time.Time    `json:"at"`
	PID            int          `json:"pid,omitempty"`
	StartedAt      time.Time    `json:"started_at,omitzero"`
	ExitCode       string       `json:"exit_code,omitempty"`
	Tail           []string     `json:"tail,omitempty"`
	Usage          *usage.Usage `json:"usage,omitempty"`
	PriorTokens    usage.Tokens `json:"prior_tokens,omitzero"`
	Diff           *DiffFacts   `json:"diff,omitempty"`
	LastProgressAt time.Time    `json:"last_progress_at,omitzero"`
	ExploringSince time.Time    `json:"exploring_since,omitzero"`
	GatingSince    time.Time    `json:"gating_since,omitzero"`
}

// Headless reports whether this endpoint is a process relevo runs rather than
// a pane it watches. "" is pane.
func (e Endpoint) Headless() bool { return e.Mode == ModeHeadless }

// Remote reports whether this endpoint is hosted on a remote relevo server.
func (e Endpoint) Remote() bool { return e.Mode == ModeRemote }
