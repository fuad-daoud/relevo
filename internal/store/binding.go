package store

import (
	"encoding/json"
	"time"
)

// Binding shapes name what a binding's actor does to its working tree:
// ShapeWriter changes the tree, ShapeReader only leaves artifacts. Every
// record names one; a record written before A5 (format 8 and earlier) has no
// shape key and reads as a writer, because a reader could not be bound then.
const (
	ShapeWriter = "writer"
	ShapeReader = "reader"
)

// RepoRef identifies the git repository a binding works in; nil means it could
// not be determined and never fails the caller.
type RepoRef struct {
	OriginURL string `json:"origin_url,omitempty"`
	CommonDir string `json:"common_dir,omitempty"`
}

// ForkRef records the source binding and round a fork was cut from.
type ForkRef struct {
	Name  string `json:"name"`
	Round int    `json:"round"`
}

// Rusage is the cgroup's usage_usec and memory.peak for a round's systemd
// scope, read after the builder exits.
type Rusage struct {
	CPUMS        int64 `json:"cpu_ms,omitempty"`
	PeakMemBytes int64 `json:"peak_mem_bytes,omitempty"`
}

// Progress is the current round's sampled progress clock. relevo never acts on
// it.
type Progress struct {
	SampledAt time.Time `json:"sampled_at"`
	Tree      string    `json:"tree,omitempty"`
	TreeAt    time.Time `json:"tree_at"`
	// Output is a pre-pane-removal builder's last screen fingerprint; only an
	// old bind.json carries one.
	Output   string    `json:"output,omitempty"`
	OutputAt time.Time `json:"output_at"`
}

// GateRun is the gate process for the CURRENT round while it runs.
type GateRun struct {
	PID       int    `json:"pid"`
	StartedAt int64  `json:"started_at"`
	Round     int    `json:"round"`
	Command   string `json:"command"`
	// Attempt is 0 for this round's first gate run and 1 for the single re-run
	// allowed after a daemon restart took the gate with it; never greater.
	Attempt int `json:"attempt,omitempty"`
}

// CheckRun is one acceptance check a client asked a served binding to run,
// while it runs and once it settles. A binding keeps one: a check is a single
// process against one tree, so a second one cannot start until this one ends.
type CheckRun struct {
	// ID is the caller's own name for the run. A repeated request carrying it
	// answers from this record instead of starting a second run, so a client
	// that lost the first answer learns the run's state rather than doubling
	// it.
	ID string `json:"id"`
	// Command is the check, run through `sh -c` in the binding's worktree.
	Command string `json:"command"`
	// Step names the workflow step the check belongs to, when the caller had
	// one; empty for a plain gate-shaped check.
	Step string `json:"step,omitempty"`
	// PID and StartedAt are the process handle while the run is going, empty
	// before it starts and once it ends.
	PID       int   `json:"pid,omitempty"`
	StartedAt int64 `json:"started_at,omitempty"`
	Round     int   `json:"round,omitempty"`
	// Attempt is 0 for the run's first process and 1 for the single re-run
	// allowed after a daemon restart took the first one with it; never greater.
	Attempt    int    `json:"attempt,omitempty"`
	Result     string `json:"result,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Note       string `json:"note,omitempty"`
	// LogPath is the file the run streams to, under the binding's round-file
	// area. It is written by the run and read as a bounded tail.
	LogPath string `json:"log_path,omitempty"`
}

// Settled reports whether the run has a result, so its record is final and a
// new check may take its place.
func (r CheckRun) Settled() bool { return r.Result != "" }

// Verdict is one reviewer's verdict on a closed round, shown by status until
// the round after next.
type Verdict struct {
	Round   int      `json:"round"`
	Verdict string   `json:"verdict"`
	Reasons []string `json:"reasons,omitempty"`
	// Findings is the round file key the verdict was parsed from.
	Findings string `json:"findings"`
}

// OOMRequeue records the details of an oom-kill that caused this round to be
// re-queued. Admit clears it when the round is admitted again.
type OOMRequeue struct {
	// At is when relevo saw the oom-killed exit (UTC).
	At time.Time `json:"at"`
	// Running is the number of local headless rounds with a live process at the
	// moment of the kill, including the killed one; always at least 1.
	Running int `json:"running"`
	// PeakBytes is the killed scope's peak memory, the fact the requeue note
	// and the halt message render; 0 means unknown. It is kept on the record so
	// the incident survives the log note.
	PeakBytes int64 `json:"peak_bytes,omitempty"`
}

// OwedHalt is one halt notification a binding still owes its MasterMind: the
// round the entry is filed under and the text it carries.
//
// It exists because the binding log's append can fail after a round has already
// been reported. The round close is committed to the log before the halt that
// follows it, so a failed halt entry cannot be retried by closing the round
// again; the close records what it owes here instead and a later tick writes it.
type OwedHalt struct {
	// Round is the round that closed, which is not the binding's current round:
	// a halt that fires after the advance is about the round that just ended.
	Round int `json:"round"`
	// Text is the halt reason as the binding records it, name-stripped.
	Text string `json:"text"`
}

type ServeFacts struct {
	RepoID       string    `json:"repo_id"`
	BareRepo     string    `json:"bare_repo"`
	ClosedRound  int       `json:"closed_round,omitempty"`
	ResultCommit string    `json:"result_commit,omitempty"`
	DirtyCommit  string    `json:"dirty_commit,omitempty"`
	AckedRound   int       `json:"acked_round,omitempty"`
	LastSeen     time.Time `json:"last_seen,omitempty"`
	// AuthorName and AuthorEmail are the client's git identity, so every
	// builder relevo starts for this binding commits as the client.
	AuthorName  string `json:"author_name,omitempty"`
	AuthorEmail string `json:"author_email,omitempty"`
}

// Binding ties one mastermind to one builder over one working tree.
type Binding struct {
	// Format is the on-disk format: 0 (a missing key) is format 1, today's
	// shape; save refuses to overwrite a Format it does not know.
	Format int `json:"format,omitempty"`

	Name string `json:"name"`
	CWD  string `json:"cwd"`
	// MasterMind's json key is the historical "planner": bind.json keys are
	// state already written.
	MasterMind Endpoint `json:"planner"`
	// MasterMindID names the relevo mastermind record this binding belongs to. A
	// remote binding carries the client mastermind's id too, even though the
	// mastermind never goes over the wire.
	MasterMindID     string   `json:"planner_id,omitempty"` // why: ditto, state already written
	Builder          Endpoint `json:"runner"`
	BuilderCandidate string   `json:"candidate,omitempty"`
	// BuilderAccount names the login of the candidate's pool the round drew
	// from, empty on a host with no accounts. It is stamped at the current
	// format, so a binary that predates it refuses the record rather than
	// saving one back with the account erased.
	BuilderAccount string `json:"account,omitempty"`
	// Role names the actor the runner plays. It is always written; the empty
	// value means builder and is stored as the literal "builder".
	Role string `json:"actor"`
	// Shape is what the runner's actor does to the tree: ShapeWriter changes
	// it, ShapeReader only leaves artifacts. It is always written; a record
	// with no shape key (format 8 and earlier) decodes as a writer.
	Shape string `json:"shape"`
	// Tier "" means harness.
	Tier      string `json:"tier,omitempty"`
	RoundTier string `json:"round_tier,omitempty"`
	// RoundCPU is a pointer because core 0 is valid; nil means none.
	RoundCPU *int `json:"round_cpu,omitempty"`
	// Gate is the acceptance command run through `sh -c` when the round's
	// completion marker appears; "" means no gate.
	Gate          string   `json:"gate,omitempty"`
	GateTimeoutMS int      `json:"gate_timeout_ms,omitempty"`
	GateRun       *GateRun `json:"gate_run,omitempty"`

	// CheckRun is the served binding's current check: the process while it
	// runs, and its record once it settles. The record outlives the run on
	// purpose, because it is what an idempotent repeat and a later read both
	// answer from.
	CheckRun *CheckRun `json:"check_run,omitempty"`

	RoundVerify bool `json:"round_verify,omitempty"`
	// LastVerdict is shown by status while Round-1 == LastVerdict.Round.
	LastVerdict *Verdict `json:"last_verdict,omitempty"`

	// Regate is the maximum number of automatic repair rounds relevo opens
	// after a failing gate; 0 means off. A human send or a gate pass resets
	// RepairCount, not this: the budget is a property of the binding.
	Regate      int `json:"regate,omitempty"`
	RepairCount int `json:"repair_count,omitempty"`
	// LastGateSig is the sha256 hex of the normalised output of the last
	// FAILED gate: the stall bound compares the next failure against it.
	LastGateSig string `json:"last_gate_sig,omitempty"`

	Round          int       `json:"round"`
	State          State     `json:"state"`
	RoundCap       int       `json:"round_cap"`
	RoundTimeoutMS int       `json:"round_timeout_ms"`
	RoundStartedAt time.Time `json:"round_started_at"`
	// QueuedAt is non-zero while the current round is accepted and waiting for a
	// builder slot: on a server while waiting under serve.max_builders, and on a
	// local machine while waiting after an oom kill.
	QueuedAt time.Time `json:"queued_at,omitempty"`
	// HaltNotifiedRound is deliberately NOT derived from State, which a later
	// step in the same tick may rewrite.
	HaltNotifiedRound int `json:"halt_notified_round,omitempty"`

	// FinishPending is deliberately NOT derived from State, like
	// HaltNotifiedRound.
	FinishPending bool `json:"finish_pending,omitempty"`

	// Halt is meaningful only while State == needs_you, so a stale value is
	// harmless.
	Halt   string    `json:"halt,omitempty"`
	HaltAt time.Time `json:"halt_at,omitempty"`

	// OwedHalt is the halt notification whose entry could not be written when
	// the halt was decided. Non-nil only in that window: the next tick queues
	// the entry and clears it, so a binding carrying one has halted and asked
	// for a human without having said so in the log yet.
	OwedHalt *OwedHalt `json:"owed_halt,omitempty"`

	RoundSwitches int `json:"round_switches,omitempty"`

	// RoundExcluded are candidate tokens that exited without a report during
	// the CURRENT round, so a switch never lands the pick back on a builder
	// that just proved it cannot finish the round.
	RoundExcluded []string `json:"round_excluded,omitempty"`

	// OOMRequeue is non-nil only while this round is queued after a
	// systemd-oomd kill. Admit clears it.
	OOMRequeue *OOMRequeue `json:"oom_requeue,omitempty"`

	// RoundOOMKills counts oom kills in the current round; reset to 0
	// wherever RoundSwitches is reset (reconcile.go and send.go).
	RoundOOMKills int `json:"round_oom_kills,omitempty"`

	// AbandonedSessions are harness sessions relevo stopped using while their
	// round was still open. The harness would resume them on its own -- a
	// rebooted opencode service restarts every session left marked running --
	// so the daemon deletes them. Empty in every other case.
	AbandonedSessions []AbandonedSession `json:"abandoned_sessions,omitempty"`

	// BuilderMissingSince is stamped on the first miss and cleared on any hit,
	// so a detection flicker never accumulates toward a switch.
	BuilderMissingSince time.Time `json:"runner_missing_since,omitempty"`

	// StalledSince: relevo never acts on it -- killing stays the human's
	// decision.
	StalledSince time.Time `json:"stalled_since,omitempty"`

	// StopRequestedAt and StopGraceMS are cleared by Send and by the round
	// close, so a stale request never outlives its round.
	StopRequestedAt time.Time `json:"stop_requested_at,omitempty"`
	StopGraceMS     int       `json:"stop_grace_ms,omitempty"`
	Progress        *Progress `json:"progress,omitempty"`

	// ExploringSince is a label only -- no hook event -- because some plans
	// are read-heavy.
	ExploringSince time.Time `json:"exploring_since,omitempty"`

	// StaleSince is when a NEEDS YOU binding last changed state once it has
	// been unacted for policy.json's stale_after_ms.
	StaleSince time.Time `json:"stale_since,omitempty"`

	// RoundBaselineTree empty means no baseline was captured and the round
	// produces no diff.
	RoundBaselineTree string `json:"round_baseline_tree,omitempty"`
	// RoundClosedTree is deliberately not derived from RoundBaselineTree: the
	// two describe different instants.
	RoundClosedTree   string `json:"round_closed_tree,omitempty"`
	RoundBaselineHead string `json:"round_baseline_head,omitempty"`
	// legacy: bind.json from before pane builders were removed.
	BuilderScreen      string        `json:"runner_screen,omitempty"`
	BuilderScreenAt    time.Time     `json:"runner_screen_at,omitempty"`
	MasterMindScreen   string        `json:"planner_screen,omitempty"`    // why: state already written
	MasterMindScreenAt time.Time     `json:"planner_screen_at,omitempty"` // why: ditto
	HeldGrace          time.Duration `json:"held_grace,omitempty"`

	// Worktree is the only directory relevo may ever remove.
	Worktree      string `json:"worktree,omitempty"`
	ForkedFrom    string `json:"forked_from,omitempty"`
	ForkedAtRound int    `json:"forked_at_round,omitempty"`

	Branch string `json:"branch,omitempty"`
	Base   string `json:"base,omitempty"`
	// BaseRef "" means `relevo land` requires an explicit --onto.
	BaseRef string `json:"base_ref,omitempty"`

	LandedAt       time.Time `json:"landed_at,omitempty"`
	LandedPR       string    `json:"landed_pr,omitempty"`
	ExistingBranch bool      `json:"existing_branch,omitempty"`

	// Repo empty disables worktree-escape detection.
	Repo string `json:"repo,omitempty"`

	// RepoRef is distinct from Repo, the add/fork source checkout.
	RepoRef *RepoRef `json:"repo_ref,omitempty"`

	// Link names the other copy of a remote binding; nil on a local binding,
	// on a remote binding an older peer created, and on every binding that
	// existed before the link columns did. It is a field recordFormat does
	// not stamp: a binary that drops it loses only the link.
	Link *RemoteLink `json:"link,omitempty"`

	// RecordID is the binding_record id a save should use, set only by a
	// caller that must know the id before the save -- addRemote pre-mints it
	// so the server can be told its client's id. It is not part of bind.json:
	// the row already carries the id.
	RecordID string `json:"-"`

	Feature string `json:"feature,omitempty"`

	// Ticket is the issue this binding serves, in stored form (#N or
	// owner/repo#N). A binding may carry a ticket without a feature.
	Ticket string `json:"ticket,omitempty"`

	// Consults omitempty keeps every bind.json written before consults existed
	// byte-identical until its first consult.
	Consults []Consult `json:"consults,omitempty"`

	// ConsultCap zero means DefaultConsultCap.
	ConsultCap int `json:"consult_cap,omitempty"`

	// Edges is read from records written before relevo edge was removed;
	// nothing writes it.
	Edges []Edge `json:"edges,omitempty"`

	// Owner is the enrolled client id that created this binding on a relevo
	// server; empty on every local binding.
	Owner string `json:"owner,omitempty"`

	// Serve is nil on local bindings.
	Serve *ServeFacts `json:"serve,omitempty"`

	RemoteUnreachableSince time.Time `json:"remote_unreachable_since,omitempty"`
	RemoteAbsorbFailures   int       `json:"remote_absorb_failures,omitempty"`

	// RemoteBundleFailures counts the consecutive round-bundle fetches that
	// failed. The apply half owns it -- the fetch half runs without the state
	// lock and cannot write binding state -- and it stays separate from the
	// absorb counter so a halt names the stage that actually failed.
	RemoteBundleFailures int `json:"remote_bundle_failures,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// bindingAlias is Binding without its methods, so UnmarshalJSON decodes the
// new keys with the ordinary field rules.
type bindingAlias Binding

// bindingNewKeys records which A4 keys a record carried, so a legacy key is
// only consulted when its replacement is absent: when both are present the new
// key wins.
type bindingNewKeys struct {
	Runner             *Endpoint  `json:"runner"`
	Candidate          *string    `json:"candidate"`
	Actor              *string    `json:"actor"`
	RunnerMissingSince *time.Time `json:"runner_missing_since"`
	RunnerScreen       *string    `json:"runner_screen"`
	RunnerScreenAt     *time.Time `json:"runner_screen_at"`
}

// bindingLegacyKeys are the pre-A4 spellings of Binding's renamed fields.
type bindingLegacyKeys struct {
	Builder             *Endpoint  `json:"builder"`
	BuilderCandidate    *string    `json:"builder_candidate"`
	Role                *string    `json:"role"`
	BuilderMissingSince *time.Time `json:"builder_missing_since"`
	BuilderScreen       *string    `json:"builder_screen"`
	BuilderScreenAt     *time.Time `json:"builder_screen_at"`
}

// UnmarshalJSON reads a binding record written by this relevo or by one before
// the A4 rename, mapping the old keys onto the same fields. A record with no
// actor decodes to the "builder" actor, so readers never see an empty one.
func (b *Binding) UnmarshalJSON(raw []byte) error {
	var alias bindingAlias
	if err := json.Unmarshal(raw, &alias); err != nil {
		return err
	}
	var newKeys bindingNewKeys
	if err := json.Unmarshal(raw, &newKeys); err != nil {
		return err
	}
	var old bindingLegacyKeys
	if err := json.Unmarshal(raw, &old); err != nil {
		return err
	}

	out := Binding(alias)
	if newKeys.Runner == nil && old.Builder != nil {
		out.Builder = *old.Builder
	}
	if newKeys.Candidate == nil && old.BuilderCandidate != nil {
		out.BuilderCandidate = *old.BuilderCandidate
	}
	if newKeys.Actor == nil && old.Role != nil {
		out.Role = *old.Role
	}
	if newKeys.RunnerMissingSince == nil && old.BuilderMissingSince != nil {
		out.BuilderMissingSince = *old.BuilderMissingSince
	}
	if newKeys.RunnerScreen == nil && old.BuilderScreen != nil {
		out.BuilderScreen = *old.BuilderScreen
	}
	if newKeys.RunnerScreenAt == nil && old.BuilderScreenAt != nil {
		out.BuilderScreenAt = *old.BuilderScreenAt
	}
	if out.Role == "" {
		out.Role = "builder"
	}
	// A record with no shape predates A5, when only writers could be bound.
	if out.Shape == "" {
		out.Shape = ShapeWriter
	}
	*b = out
	return nil
}
