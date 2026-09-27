package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// Version is the remote protocol version.
const Version = 1

// ContentTypeGitBundle is the MIME content type for git bundles.
const ContentTypeGitBundle = "application/x-git-bundle"

// HeaderClientVersion is the request header a client sets to its buildVersion
// (#373). It is informational -- the server logs it and never rejects a
// request on it, and it is never part of the signature.
const HeaderClientVersion = "Relevo-Client-Version"

// HeaderFileSize is the file's total byte length on the server.
const HeaderFileSize = "X-Relevo-Size"

// HeaderFileFrom is the offset honoured by the server.
const HeaderFileFrom = "X-Relevo-From"

// FileRange describes the byte range honoured by a server for a round file route.
// Honored is true only when both headers are present and parse as non-negative ints.
type FileRange struct {
	Honored bool
	From    int64
	Size    int64
}

// ParseFileRange extracts a FileRange from HTTP response headers.
func ParseFileRange(h http.Header) FileRange {
	fromStr := h.Get(HeaderFileFrom)
	sizeStr := h.Get(HeaderFileSize)
	if fromStr == "" || sizeStr == "" {
		return FileRange{}
	}
	from, err1 := strconv.ParseInt(fromStr, 10, 64)
	size, err2 := strconv.ParseInt(sizeStr, 10, 64)
	if err1 != nil || err2 != nil || from < 0 || size < 0 {
		return FileRange{}
	}
	return FileRange{
		Honored: true,
		From:    from,
		Size:    size,
	}
}

// RoundState represents the execution state of a round on the server.
type RoundState string

const (
	RoundIdle     RoundState = "idle"
	RoundRunning  RoundState = "running"
	RoundClosed   RoundState = "closed"
	RoundNeedsYou RoundState = "needs_you"
	// RoundQueued is a round accepted by the server with no builder process
	// yet: staged, waiting for a slot under serve.max_builders (#285).
	RoundQueued RoundState = "queued"
)

// WhoAmI represents the response to an authentication identity check.
type WhoAmI struct {
	ID            ClientID `json:"id"`
	Label         string   `json:"label"`
	ServerVersion int      `json:"server_version"`
	Transports    []string `json:"transports"` // ["git-bundle"]

	// Features, BuilderTier and MaxTier are additive (#141 remote half): a
	// pre-tier server omits them, so a client can tell the two apart before
	// creating anything.
	Features    []string `json:"features,omitempty"`     // ["tier"] on a server with this change
	BuilderTier string   `json:"default_tier,omitempty"` // ServedBuilderTier(rt): the tier a headless builder
	// launches at when the client sends none
	MaxTier string `json:"max_tier,omitempty"` // policy.MaxTierOrDefault()

	// Builders is the server's builder census (#285); nil from a pre-queue
	// server.
	Builders *BuildersView `json:"runners,omitempty"`
}

// GitIdentity is a client's git identity (#335): the name and email its own
// commits would use, which the server runs a binding's builders as. Both
// values are required when the struct is present, 1-256 bytes with no
// newline, carriage return, NUL, < or >.
type GitIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// CreateBindingRequest holds the parameters for creating a new binding on the server.
type CreateBindingRequest struct {
	Name           string `json:"name"`        // required; store name rules apply on the server
	RepoID         string `json:"repo_id"`     // required; RepoID()
	BaseCommit     string `json:"base_commit"` // required; 40 hex
	Candidate      string `json:"candidate,omitempty"`
	RoundCap       int    `json:"round_cap,omitempty"`
	RoundTimeoutMS int    `json:"round_timeout_ms,omitempty"`
	Tier           string `json:"tier,omitempty"`  // "" = server's choice; else harness|read|edit|yolo
	Role           string `json:"actor,omitempty"` // the actor the runner plays; resolved against the server's own actors

	// Author is the client's git identity; the server runs this binding's
	// builders as it (#335). nil means an old client that sent none.
	Author *GitIdentity `json:"author,omitempty"`
}

// TagRef is one tag a client ships beside a round's bundle, so a server
// worktree can `git describe --tags` (#242). The wire field is the multipart
// form value "tags" on POST /v1/bindings/{name}/rounds: a JSON array of
// TagRef sorted by Name. Absent or empty = no tags.
type TagRef struct {
	Name string `json:"name"` // tag name without refs/tags/, e.g. "v0.4.0"; non-empty
	SHA  string `json:"sha"`  // the COMMIT the tag points at (annotated tags peeled); 40 hex
}

// BindingView is the server's wire representation of a binding's state.
type BindingView struct {
	Name          string     `json:"name"`
	State         string     `json:"state"` // the store's State string
	Round         int        `json:"round"`
	RoundState    RoundState `json:"round_state"`
	ClosedRound   int        `json:"closed_round"`
	Halt          string     `json:"halt,omitempty"`
	ResultCommit  string     `json:"result_commit,omitempty"`
	DirtyCommit   string     `json:"dirty_commit,omitempty"`
	ReportOutcome string     `json:"report_outcome,omitempty"` // reporttail.Tail.Status or "unstructured"
	// Stopped is how the closed round (ClosedRound) was stopped: "killed",
	// "reaped", "gone" or "dequeued". It is "" when that round closed any
	// other way, on a pre-stop server, or when ClosedRound is 0.
	Stopped string `json:"stopped,omitempty"`
	// DiffNote, DiffCommits and DiffTree are the closed round's diff facts,
	// from the newest KindDiff entry for Serve.ClosedRound -- the same facts
	// DiffSummary wrote to the server's own log at close. Empty/zero on any
	// binding that is not closed, or whose close wrote no diff entry.
	DiffNote       string    `json:"diff_note,omitempty"`
	DiffCommits    int       `json:"diff_commits,omitempty"`
	DiffTree       string    `json:"diff_tree,omitempty"`
	AckedRound     int       `json:"acked_round"`
	Candidate      string    `json:"candidate,omitempty"`
	RoundStartedAt time.Time `json:"round_started_at,omitempty"`
	RoundCap       int       `json:"round_cap"`
	RoundTimeoutMS int       `json:"round_timeout_ms"`
	Tier           string    `json:"tier,omitempty"` // effectiveTier(b) on the server; "" from a pre-tier server

	// Usage is the closed round's usage as the server recorded it on its
	// report entry (usage.Usage is already JSON-tagged; it is the same
	// struct store.LogEntry.Usage holds). nil from a pre-usage server, or
	// when the closed round has no report entry.
	Usage *usage.Usage `json:"usage,omitempty"`

	// Rusage is the closed round's cgroup measurement as the server
	// recorded it on its report entry (#244, #216). Nil from a pre-scope
	// server, a round that was not a scope, or when the closed round has
	// no report entry.
	Rusage *store.Rusage `json:"rusage,omitempty"`

	// PriorTokens is the tokens the server's earlier builders in ClosedRound
	// used. Nil when zero or when ClosedRound is 0.
	PriorTokens *usage.Tokens `json:"prior_tokens,omitempty"`

	// StalledSince is the server's stall stamp for a live-but-quiet headless
	// round (#252), copied onto the client binding for a running round. Zero
	// from a pre-stall server, and zero when the round is not stalled.
	StalledSince time.Time `json:"stalled_since,omitempty"`

	// Queue is this round's place in the server's builder queue (#285);
	// non-nil iff RoundState == RoundQueued.
	Queue *QueueView `json:"queue,omitempty"`

	// Live is the running round's live facts as the server's own status row
	// sees it. Non-nil only when RoundState == RoundRunning on a server that
	// sends it; nil from an older server.
	Live *LiveView `json:"live,omitempty"`
}

// QueueView is a queued round's place in the server's queue (#285).
type QueueView struct {
	Position int       `json:"position"` // 1-based
	Ahead    int       `json:"ahead"`    // Position-1
	Running  int       `json:"running"`
	Cap      int       `json:"cap"`
	Since    time.Time `json:"since"`
}

// DiffStat is a round's diff stats across the wire.
type DiffStat struct {
	Files   int `json:"files"`
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// LiveView is the running round as the server's own status row sees it.
// No field holds a server path.
type LiveView struct {
	At             time.Time    `json:"at"`
	PID            int          `json:"pid,omitempty"`
	StartedAt      time.Time    `json:"started_at,omitzero"`
	ExitCode       string       `json:"exit_code,omitempty"`
	Tail           []string     `json:"tail,omitempty"`
	Usage          *usage.Usage `json:"usage,omitempty"`
	PriorTokens    usage.Tokens `json:"prior_tokens,omitzero"`
	Diff           *DiffStat    `json:"diff,omitempty"`
	LastProgressAt time.Time    `json:"last_progress_at,omitzero"`
	ExploringSince time.Time    `json:"exploring_since,omitzero"`
	GatingSince    time.Time    `json:"gating_since,omitzero"`
}

// BuildersView is the server's builder census (#285).
type BuildersView struct {
	Running int    `json:"running"`
	Queued  int    `json:"queued"`
	Cap     int    `json:"cap"`
	Scopes  bool   `json:"scopes"`          // part 3 sets it; false here
	Slice   string `json:"slice,omitempty"` // part 3 sets it; "" here
	Quota   string `json:"quota,omitempty"` // "" = no CPU quota; else e.g. "200%"
}

// UnavailableRequest reports builder unavailability with a diagnostic reason.
type UnavailableRequest struct {
	Token  string `json:"token"`
	Reason string `json:"reason"`
}

// AvailableRequest lifts recorded unavailability: subject is a provider name
// or a candidate token, as relevo.Available takes.
type AvailableRequest struct {
	Subject string `json:"subject"`
}

// AvailableResponse reports what Available lifted: the provider it resolved
// subject to, and how many rate-limit entries it removed.
type AvailableResponse struct {
	Provider string `json:"provider"`
	Removed  int    `json:"removed"`
}

type CandidateView struct {
	Token string `json:"token"`          // canonical harness/provider/model token
	Name  string `json:"name,omitempty"` // the candidate's short name, when the server knows one
	Kind  string `json:"kind"`           // harness kind: agy | claude | opencode
	Gated bool   `json:"gated"`          // a live limit gate on the ledger
	Pick  bool   `json:"pick"`           // what the policy order would pick right now for the builder actor
}

type CandidatesResponse struct {
	Candidates []CandidateView `json:"candidates"`
}

// Code represents a structured error code returned by the remote protocol.
type Code string

const (
	CodeNotEnrolled    Code = "not_enrolled"
	CodeRevoked        Code = "revoked"
	CodeBadSignature   Code = "bad_signature"
	CodeStale          Code = "stale"
	CodeNotFound       Code = "not_found"
	CodeRoundOpen      Code = "round_open"
	CodeRoundStarted   Code = "round_started"
	CodeNotFastForward Code = "not_fast_forward"
	CodeNoRunner       Code = "no_runner"
	CodeSpawnFailed    Code = "spawn_failed"
	CodeTooLarge       Code = "too_large"
	CodeVersion        Code = "version"
	CodeInvalid        Code = "invalid"
	CodeTierAboveMax   Code = "tier_above_max"
	// CodeRoundHalted is a 409: the round could not start because the
	// binding halted trying to start it (e.g. a builder spawn failure);
	// Message is the binding's Halt text.
	CodeRoundHalted Code = "round_halted"
	// CodeNothingToStop is a 409: the binding has no running or queued
	// round to stop.
	CodeNothingToStop Code = "nothing_to_stop"
)

// FeatureTier is the WhoAmI.Features token a server with the permission-tier
// wire fields advertises (#141 remote half).
const FeatureTier = "tier"

// FeatureQueue is the WhoAmI.Features token a server with the builder
// cap/queue advertises (#285).
const FeatureQueue = "queue"

// FeatureStop is the WhoAmI.Features token a server with
// POST /v1/bindings/{name}/stop advertises (#344).
const FeatureStop = "stop"

// FeatureBuilder is the WhoAmI.Features token a server that accepts the
// "candidate" multipart form value on POST /v1/bindings/{name}/rounds
// advertises. The field is a canonical candidate token that persists as the
// binding's candidate from that round on; absent or "" keeps the binding's
// candidate.
const FeatureBuilder = "candidate"

// FeatureRoles is the WhoAmI.Features token a server that honours
// CreateBindingRequest.Role advertises; a server without it ignores the field
// and runs the default actor.
const FeatureRoles = "actors"

// FeatureIdempotentSend is the WhoAmI.Features token a server that answers a
// repeated identical start-round request for the open round with 200 and the
// current view -- no re-Send, no QueuedAt reset, no new log entry -- advertises
// (#373). The client round (R2) retries StartRound only when it sees this.
const FeatureIdempotentSend = "idempotent_send"

// FeatureAuthor is the WhoAmI.Features token a server that honours
// CreateBindingRequest.Author advertises (#335, #373). A client whose server
// lacks it logs a Warn once per server and continues.
const FeatureAuthor = "author"

// ErrorBody represents a JSON error response returned by the server.
type ErrorBody struct {
	Code    Code   `json:"error"`
	Message string `json:"message"`
}

// Error implements error, returning Message.
func (e ErrorBody) Error() string {
	return e.Message
}

// CodeOf maps auth sentinels to their respective error codes, returning "" for unknown errors.
func CodeOf(err error) Code {
	switch {
	case errors.Is(err, ErrUnknownClient):
		return CodeNotEnrolled
	case errors.Is(err, ErrRevoked):
		return CodeRevoked
	case errors.Is(err, ErrBadSignature):
		return CodeBadSignature
	case errors.Is(err, ErrStale):
		return CodeStale
	default:
		return ""
	}
}

// ErrNoRoot indicates that an empty root commit SHA was passed to RepoID.
var ErrNoRoot = errors.New("empty root commit")

// RepoID returns the lower-case hex representation of sha256(rootCommit), where
// rootCommit is the ASCII 40-hex string. Returns ErrNoRoot if rootCommit is empty.
func RepoID(rootCommit string) (string, error) {
	if rootCommit == "" {
		return "", ErrNoRoot
	}
	sum := sha256.Sum256([]byte(rootCommit))
	return hex.EncodeToString(sum[:]), nil
}
