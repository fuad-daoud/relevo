package remote

import (
	"encoding/json"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// FeatureChain is the WhoAmI.Features token a server advertises when it
// accepts POST /v1/chains and drives the chain on its own daemon, serving
// GET /v1/chains/{name} and the stop, resume and done routes on it.
const FeatureChain = "chain"

// FeatureWorkflow is the WhoAmI.Features token a server advertises when it
// accepts custom workflow definitions on chain create.
const FeatureWorkflow = "workflow"

// CodeChainRunning is a 409: the chain is already running, so a request that
// wanted it otherwise cannot proceed.
const CodeChainRunning Code = "chain_running"

// ChainSettings is a chain create request's resolved configuration. It mirrors
// chain.Settings; internal/remote does not import internal/chain, so the two
// are converted at the edges.
type ChainSettings struct {
	MaxCorrections int    `json:"max_corrections,omitempty"`
	ReviewerActor  string `json:"reviewer_actor,omitempty"`
	PlannerActor   string `json:"planner_actor,omitempty"`
	SecurityActor  string `json:"security_actor,omitempty"`
	Security       bool   `json:"security,omitempty"`
	Gate           string `json:"gate,omitempty"`
	Regate         int    `json:"regate,omitempty"`
}

// CreateChainRequest is the multipart create of POST /v1/chains: the "chain"
// field carries this JSON, and, when the client has one, a "bundle" file part
// carries the base bundle.
type CreateChainRequest struct {
	Name               string            `json:"name"`
	RepoID             string            `json:"repo_id"`
	BaseCommit         string            `json:"base_commit"`
	Plans              []string          `json:"plans,omitempty"`
	Settings           ChainSettings     `json:"settings,omitempty"`
	Feature            string            `json:"feature,omitempty"`
	Ticket             string            `json:"ticket,omitempty"`
	Author             *GitIdentity      `json:"author,omitempty"`
	ClientInstallation string            `json:"client_installation,omitempty"`
	ClientBindingIDs   map[string]string `json:"client_binding_ids,omitempty"`
	Workflow           json.RawMessage   `json:"workflow,omitempty"`
	ClientActorIDs     map[string]string `json:"client_actor_ids,omitempty"`
}

// ClosedRoundView is one closed member round's facts: the facts BindingView
// carries for ClosedRound, but for any round.
type ClosedRoundView struct {
	Round         int      `json:"round"`
	ReportOutcome string   `json:"report_outcome,omitempty"`
	GateResult    string   `json:"gate_result,omitempty"`
	Stopped       string   `json:"stopped,omitempty"`
	ReportNote    string   `json:"report_note,omitempty"`
	Switches      []string `json:"switches,omitempty"`
	DiffNote      string   `json:"diff_note,omitempty"`
	DiffCommits   int      `json:"diff_commits,omitempty"`
	DiffTree      string   `json:"diff_tree,omitempty"`
	// DirtyCommit is the round's uncommitted work at
	// refs/relevo/<name>/round-<Round>, "" when the round closed clean. The
	// server retains it for the round it last closed only, so an older round
	// ships "" rather than another round's commit.
	DirtyCommit string        `json:"dirty_commit,omitempty"`
	Usage       *usage.Usage  `json:"usage,omitempty"`
	Rusage      *store.Rusage `json:"rusage,omitempty"`
	// PriorTokens is the tokens the server's earlier builders in this round
	// used. Nil when zero, and nil from a server older than the field.
	PriorTokens *usage.Tokens `json:"prior_tokens,omitempty"`
}

// ChainMemberView is one chain member: its part and binding name, the actor it
// plays, its current binding view, and the closed rounds in
// (acked_round, closed_round], ascending.
type ChainMemberView struct {
	Part   string            `json:"part"`
	Name   string            `json:"name"`
	Actor  string            `json:"actor,omitempty"`
	View   BindingView       `json:"view"`
	Rounds []ClosedRoundView `json:"rounds,omitempty"`
}

// ChainEventView is one chain_event trace row: the state before the event, the
// round whose close it was, and the event and action JSON as stored.
type ChainEventView struct {
	Seq    int       `json:"seq"`
	TS     time.Time `json:"ts"`
	Phase  string    `json:"phase,omitempty"`
	Step   string    `json:"step,omitempty"`
	Member string    `json:"member,omitempty"`
	Round  int       `json:"round,omitempty"`
	Plan   int       `json:"plan,omitempty"`
	Event  string    `json:"event,omitempty"`
	Action string    `json:"action,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// ChainView is a chain's wire representation: its state, settings, member
// views and trace.
type ChainView struct {
	Name            string            `json:"name"`
	Status          string            `json:"status,omitempty"`
	Reason          string            `json:"reason,omitempty"`
	Phase           string            `json:"phase,omitempty"`
	Step            string            `json:"step,omitempty"`
	Plan            int               `json:"plan,omitempty"`
	Plans           int               `json:"plans,omitempty"`
	Corrections     int               `json:"corrections,omitempty"`
	AwaitingMember  string            `json:"awaiting_member,omitempty"`
	AwaitingRound   int               `json:"awaiting_round,omitempty"`
	Base            string            `json:"base,omitempty"`
	Branch          string            `json:"branch,omitempty"`
	Feature         string            `json:"feature,omitempty"`
	Ticket          string            `json:"ticket,omitempty"`
	PlanStartCommit string            `json:"plan_start_commit,omitempty"`
	Settings        ChainSettings     `json:"settings,omitempty"`
	Findings        int               `json:"findings,omitempty"`
	Members         []ChainMemberView `json:"members,omitempty"`
	Trace           []ChainEventView  `json:"trace,omitempty"`
	Workflow        json.RawMessage   `json:"workflow,omitempty"`
	State           json.RawMessage   `json:"state,omitempty"`
}

// ChainResumeRequest is the body of POST /v1/chains/{name}/resume. A nil
// pointer keeps the chain's current value; a non-nil pointer sets it, and a
// Gate pointing at "" clears the gate.
type ChainResumeRequest struct {
	MaxCorrections *int    `json:"max_corrections,omitempty"`
	ReviewerActor  string  `json:"reviewer_actor,omitempty"`
	PlannerActor   string  `json:"planner_actor,omitempty"`
	SecurityActor  string  `json:"security_actor,omitempty"`
	Security       *bool   `json:"security,omitempty"`
	Gate           *string `json:"gate,omitempty"`
	Regate         *int    `json:"regate,omitempty"`
}

// ChainStopResponse is the answer to POST /v1/chains/{name}/stop: the round
// that was stopped, the action taken, and the chain's view afterwards.
type ChainStopResponse struct {
	Round  int       `json:"round"`
	Action string    `json:"action"`
	Chain  ChainView `json:"chain"`
}
