// Package db is relevo's system of record: a pure-Go sqlite file at
// <state root>/relevo.db, behind this package alone -- it is the only place a
// driver is imported, so the move to Turso later is a driver swap here.
package db

import "time"

// Every id is a text ULID minted by NewID, and every time is time.Time in Go
// but RFC3339 UTC with millisecond precision in the db. Nullable columns are
// pointers on the Go side.

// Repo is a git repository relevo has seen, identified by its normalised
// origin URL, its git common dir, or both.
type Repo struct {
	ID        string
	OriginURL *string
	CommonDir *string
	FirstSeen time.Time
}

type MasterMind struct {
	ID          string
	HarnessKind string
	SessionID   string
	FirstSeen   time.Time
	LastSeen    time.Time
}

type Binding struct {
	ID                  string
	Name                string
	RepoID              *string
	MasterMindID        *string
	Feature             *string
	Ticket              *string
	ForkedFromBindingID *string
	ForkedFromRound     *int
	CWD                 string
	Worktree            *string
	Branch              *string
	BaseCommit          *string
	Tier                *string
	Gate                *string
	BuilderMode         string
	Server              *string
	CreatedAt           time.Time
	FinalState          *string
	ArchivedAt          *time.Time
	ArchivePath         *string
	IngestSource        string
}

type Round struct {
	ID        string
	BindingID string
	Number    int
	StartedAt time.Time
	ClosedAt  *time.Time
	Outcome   string
	Candidate *string
	// Account is the login the candidate's pool drew the round from; nil on
	// every round recorded on a host with no accounts.
	Account        *string
	Harness        *string
	Provider       *string
	Model          *string
	Mode           *string
	Actor          string
	Tier           *string
	Commits        *int
	Tree           *string
	GateResult     *string
	GateExit       *int
	GateDurationMS *int64
	InTokens       *int64
	CacheTokens    *int64
	WriteTokens    *int64
	OutTokens      *int64
	CostUSD        *float64
	CostBasis      *string
	ReportOutcome  *string
	Switches       int
}

type Event struct {
	ID          string
	BindingID   string
	RoundID     *string
	Seq         int
	TS          time.Time
	Kind        string
	Direction   string
	Note        *string
	Path        *string
	DeliveredAt *time.Time
	Confirmed   bool
	Late        bool
	Flagged     *int
	FlaggedBy   *string
	EntryJSON   string
}

// EventLogRow is one event projected with its binding name and round summary.
type EventLogRow struct {
	TS          time.Time
	Seq         int
	Kind        string
	Note        *string
	EntryJSON   string
	BindingName string
	RoundID     *string
	Round       *int
	Tokens      *int64
	DurationMS  *int64
}

// Artifact is one captured file for a round: a plan, report, diff, drift
// patch, gate log, or a consult's question/answer/ask/findings.
type Artifact struct {
	ID         string
	RoundID    string
	Kind       string
	ConsultID  *string
	Text       string
	Bytes      int64
	SHA256     string
	CapturedAt time.Time
}

// TranscriptRecord is one record of a round's builder stream or a mastermind's
// session, copied verbatim (RecordJSON) alongside its rendered line.
type TranscriptRecord struct {
	ID         string
	OwnerKind  string
	OwnerID    string
	Seq        int
	TS         *time.Time
	RecordJSON string
	Rendered   string
}

type Cursor struct {
	Source     string
	ByteOffset int64
	HeadSHA    string
	WholeSHA   *string
	UpdatedAt  time.Time
}

// Filter is the shared query contract: the zero value of every field means
// "no constraint" on that field.
type Filter struct {
	Repo, Here, Feature, Ticket, Binding, MasterMind     string
	Harness, Provider, Model, Candidate                  string
	Outcome, ReportOutcome, State, GateResult, CostBasis string
	Round                                                int
	Since, Until                                         time.Time
	Archived                                             *bool
	Limit                                                int
	Newest                                               bool
}

type RoundRow struct {
	BindingID, BindingName              string
	Repo, Feature, Ticket               *string
	Number                              int
	StartedAt                           time.Time
	ClosedAt                            *time.Time
	Outcome                             string
	Actor                               string
	Candidate, Harness, Provider, Model *string
	// Account is the login the round's candidate pool drew from, nil when the
	// round recorded none. Tagged omitempty so an account-less round keeps the
	// history document it always had.
	Account                                       *string `json:"Account,omitempty"`
	Commits                                       *int
	Tree, GateResult                              *string
	CostUSD                                       *float64
	CostBasis                                     *string
	InTokens, CacheTokens, WriteTokens, OutTokens *int64
	ReportOutcome                                 *string
	Mode, Server                                  *string
	// DurationMS is *ClosedAt - StartedAt in milliseconds; nil when the
	// round has no closed_at (still open, or a source that records none).
	DurationMS *int64
	Switches   int
	Archived   bool
	ArchivedAt *time.Time
}

// BindingRow is one binding plus its repo's identity and round summary.
type BindingRow struct {
	Binding
	RepoOrigin, RepoCommonDir *string
	Rounds                    int
	LastActivity              time.Time
}

type Stats struct {
	Version     int
	SizeBytes   int64
	Rows        map[string]int
	NewestRound *time.Time
}

const (
	OutcomeReported     = "reported"
	OutcomeHalted       = "halted"
	OutcomeExited       = "exited"
	OutcomeSwitched     = "switched"
	OutcomeDoneNoReport = "done_no_report"
	OutcomeOpen         = "open"
)

func ValidOutcome(s string) bool {
	switch s {
	case OutcomeReported, OutcomeHalted, OutcomeExited, OutcomeSwitched, OutcomeDoneNoReport, OutcomeOpen:
		return true
	}
	return false
}

const (
	ArtifactPrompt   = "prompt"
	ArtifactReport   = "report"
	ArtifactDiff     = "diff"
	ArtifactDrift    = "drift"
	ArtifactGateLog  = "gate_log"
	ArtifactQuestion = "question"
	ArtifactAnswer   = "answer"
	ArtifactAsk      = "ask"
	ArtifactFindings = "findings"
)

func ValidArtifactKind(s string) bool {
	switch s {
	case ArtifactPrompt, ArtifactReport, ArtifactDiff, ArtifactDrift, ArtifactGateLog,
		ArtifactQuestion, ArtifactAnswer, ArtifactAsk, ArtifactFindings:
		return true
	}
	return false
}

const (
	OwnerRound = "round"
)

func ValidOwnerKind(s string) bool {
	switch s {
	case OwnerRound:
		return true
	}
	return false
}

const (
	IngestLive    = "live"
	IngestArchive = "archive"
)

func ValidIngestSource(s string) bool {
	switch s {
	case IngestLive, IngestArchive:
		return true
	}
	return false
}
