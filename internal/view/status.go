// Package view renders a binding's state for a human and holds the plain
// types that carry it. The code that computes that state -- probes, live
// usage, git numstat, mastermind routing -- stays in relevo and fills them.
package view

import (
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// AgentUnknown is the status word for a builder relevo could not determine: a
// remote server that has not reported yet, or a headless process whose state
// could not be read. It means "relevo did not ask and must not claim absence",
// the same word the headless and remote paths use for "could not determine".
const AgentUnknown = "unknown"

// LiveDiff is the live "+N/-M in F" a status row shows while a round is
// open: dir's working tree against the round's baseline tree, no patch body,
// just the numstat. Shared marks a --cwd binding, whose worktree is the
// mastermind's own tree rather than one relevo created.
type LiveDiff struct {
	Files   int  `json:"files"`
	Added   int  `json:"added"`
	Removed int  `json:"removed"`
	Shared  bool `json:"shared,omitempty"`
}

// BindingStatus is one row of relevo status: stored binding plus what relevo
// can determine about its builder and its delivery route.
type BindingStatus struct {
	Name  string `json:"name"`
	CWD   string `json:"cwd"`
	Round int    `json:"round"`
	// PlanRound is the highest round with a plan log entry -- the round in
	// flight while one is, the last round sent once it has closed; 0 before
	// any plan. Unlike Round, it never names an unsent round.
	PlanRound        int    `json:"plan_round,omitempty"`
	State            string `json:"state"`
	Display          string `json:"display"`
	BuilderCandidate string `json:"candidate"`
	// BuilderName is that candidate's short name, for display. Empty when the
	// candidate is no longer configured, in which case the token is shown;
	// the token always stays the identity.
	BuilderName string `json:"candidate_name,omitempty"`
	// Role is the actor the runner plays; always present, "builder" when the
	// binding stores the empty (builder) one.
	Role string `json:"actor"`
	// MasterMindID and MasterMindName name the relevo mastermind record this binding
	// belongs to.
	MasterMindID   string `json:"mastermind_id,omitempty"`
	MasterMindName string `json:"mastermind_name,omitempty"`
	// MasterMindChatLabel and MasterMindChatLink are the harness's own name for the
	// mastermind's session: Label.Text and Label.Link. Only cmd/relevo fills
	// them, inside the command a person ran, and only to print them; Status
	// itself leaves them empty, so no label is ever computed on, or sent to,
	// a server. They are never stored or logged.
	MasterMindChatLabel string `json:"mastermind_chat_label,omitempty"`
	MasterMindChatLink  string `json:"mastermind_chat_link,omitempty"`
	MasterMindKind      string `json:"mastermind_kind"`
	// MasterMindRoute is how a pending report reaches this binding mastermind:
	// "channel", "deliverer" or "pull". "pull" is a route, not a fault: it is
	// the background wait's `relevo wait`, which is how a Claude Code mastermind
	// in tools mode gets its report.
	MasterMindRoute string `json:"mastermind_route"`
	// MasterMindRouteLive reports whether that route can push right now: a live
	// channel claim, or a configured deliverer. A pull route is never live,
	// because the daemon cannot see whether a background wait is running.
	MasterMindRouteLive bool   `json:"mastermind_route_live"`
	BuilderKind         string `json:"harness"`
	// BuilderDefinition is the builder's resolved agent definition on this
	// binding's builder kind, set only when it is custom: a shipped
	// definition leaves the field empty and omitted, so today's JSON is
	// unchanged.
	BuilderDefinition string `json:"agent_definition,omitempty"`
	// BuilderDefinitionCustom is true exactly when BuilderDefinition is set,
	// so a consumer can tell "custom" from "absent" without the string.
	BuilderDefinitionCustom bool   `json:"agent_definition_custom,omitempty"`
	BuilderStatus           string `json:"runner_status"`
	// Headless is set for a headless builder: its process state and log.
	// BuilderStatus is one of idle, working, exited N, exited, unknown. Nil
	// for a remote builder.
	Headless *HeadlessInfo `json:"headless,omitempty"`
	// Detail explains an overloaded state where the display word cannot.
	// Populated only for store.StateBroken, which covers three situations
	// whose correct recoveries differ -- and in one of which the obvious
	// recovery orphans a builder that is still running.
	Detail string     `json:"detail,omitempty"`
	Last   *LastEvent `json:"last,omitempty"`
	// LastSeq is the Seq of the newest log entry; 0 when the log is empty.
	// No omitempty: a consumer reads 0 as "nothing yet".
	LastSeq int `json:"last_seq"`
	// LastPayload is the most recent plan/report/question/answer entry --
	// the four kinds that cross between mastermind and builder -- as opposed to
	// Last, which is the most recent entry of any kind including relevo's own
	// bookkeeping (drift, pick, switch, exit, diff). Nil when the log has
	// none.
	LastPayload *LastEvent `json:"last_payload,omitempty"`
	// LastClose is the newest diff entry's commit facts; nil when the log
	// has no diff entry.
	LastClose *CloseInfo `json:"last_close,omitempty"`
	// LastUsage is the newest report entry's usage; nil when no report entry
	// carries one -- every round closed before the usage record existed, and
	// every round not yet closed. Spend sums every report and findings entry
	// that carries usage; nil when none does. Both omitted from JSON when nil
	// so a consumer that never learned them sees the document it always did.
	LastUsage *usage.Usage `json:"last_usage,omitempty"`
	Spend     *usage.Spend `json:"spend,omitempty"`
	// LiveUsage is what the open round has consumed so far, read from the
	// harness's record on this call. nil when no round is open or nothing is
	// readable yet. Never recorded, never summed into Spend.
	LiveUsage *usage.Usage `json:"live_usage,omitempty"`
	// RoundStart is the TS of the earliest KindPlan + DirToBuilder entry whose
	// Round equals the round of the newest such entry. Zero when the log has no
	// such entry.
	RoundStart time.Time `json:"round_start,omitzero"`
	// RoundEnd is the TS of the newest KindReport + DirToMasterMind entry with that
	// same Round and TS >= RoundStart. Zero when none (the round is open).
	RoundEnd time.Time `json:"round_end,omitzero"`
	// RoundUsage is a copy of the Usage on the entry that set RoundEnd. Nil when
	// RoundEnd is zero or that entry's Usage is nil. Never taken from any other entry.
	RoundUsage *usage.Usage `json:"round_usage,omitempty"`
	// RoundPriorTokens is the tokens of the current round's earlier segments.
	RoundPriorTokens usage.Tokens `json:"round_prior_tokens,omitzero"`
	// Server is b.Builder.Server when b.Builder.Remote(); "" otherwise.
	Server string `json:"server,omitempty"`
	// Dirty is the rendered rule: the newest close left the tree dirty and
	// no newer round has been sent, so the uncommitted work is still what
	// the tree holds. False once a round is running -- a dirty tree is then
	// the expected state.
	Dirty         bool         `json:"dirty"`
	Pending       *PendingInfo `json:"pending,omitempty"`
	ForkedFrom    string       `json:"forked_from,omitempty"`
	ForkedAtRound int          `json:"forked_at_round,omitempty"`
	// Consults is how many consults are reserved or running on this binding.
	// Terminal ones are omitted: they are a reap chore, not work in flight.
	Consults int `json:"consults,omitempty"`
	// Switches is builder switches in the current round; zero is omitted.
	Switches int `json:"switches,omitempty"`
	// Branch is the binding's worktree branch; "" for a --cwd binding,
	// which has no worktree of its own.
	Branch string `json:"branch,omitempty"`
	// Landed is the land rule: "landed", or "landed pr <url>" when the last
	// land created a PR. Empty until the binding is first landed, and cleared
	// by the next Send.
	Landed string `json:"landed,omitempty"`
	// LandedAt and LandedPR are the same fact as data, for a statusline
	// consumer that should not parse the rendered word. LandedAt is the zero
	// time and LandedPR "" until the first land.
	LandedAt time.Time `json:"landed_at,omitempty"`
	LandedPR string    `json:"landed_pr,omitempty"`
	// LastProgressAt is when one of the binding's progress signals last
	// changed: the later of the tree's and the output's last change. The zero
	// time when the open round has not been sampled.
	LastProgressAt time.Time `json:"last_progress_at,omitempty"`
	// Stall is the stalled label ("stalled 17m") while the binding is
	// stalled; "" otherwise. Relevo never acts on it.
	Stall string `json:"stall,omitempty"`
	// Exploring is the exploring label ("exploring 22m") while the
	// builder's output is moving and its tree is not; "" otherwise.
	Exploring string `json:"exploring,omitempty"`
	// Stale is the stale label ("stale 4h 0m") for a NEEDS YOU or HELD
	// binding that has sat unacted past stale_after_ms; "" otherwise.
	Stale string `json:"stale,omitempty"`
	// Waiting is set when the binding is stalled on a human (WaitingOn):
	// the cause, the one-line reason, since when, and the verb that
	// resolves it. Nil otherwise, including for a switchable broken
	// binding the daemon is about to fix itself.
	Waiting *Waiting `json:"waiting,omitempty"`
	// Verdict is the newest reviewer verdict, shown while it belongs
	// to the round just closed: "verdict: rejected (2 reasons)". Empty when
	// the binding never had a verify round and when the verdict is stale --
	// a consumer cannot tell those apart, and does not need to.
	Verdict string `json:"last_verdict,omitempty"`
	// Live is the round's live diff stat against its baseline tree:
	// nil when no round is open, the baseline was never recorded, or the
	// git read failed -- a status row never fails because of it.
	Live *LiveDiff `json:"live,omitempty"`
	// QuietFor is how long since LastProgressAt for an ACTIVE row with an
	// open round, AgeText-formatted; "" before the first progress
	// sample and for every other display state.
	QuietFor string `json:"quiet_for,omitempty"`
	// Unread is true when the binding's newest report entry is newer than
	// its .viewed stamp -- or there is no stamp at all and a report exists.
	// No omitempty: a consumer reads false as "seen".
	Unread bool `json:"unread"`
	// Owner is the client this row belongs to on a serve box: the client's
	// "SHA256:<base64>" fingerprint. Empty on a mastermind, where every row
	// belongs to the one runtime the UI is welded to.
	Owner string `json:"owner,omitempty"`
	// OwnerLabel is Owner rendered for a human: Clients.LabelOf, or
	// ShortOwner(Owner) when that client has no label. Empty on a mastermind
	// row. Renderers key new behaviour on OwnerLabel != "" only.
	OwnerLabel string `json:"owner_label,omitempty"`
	// Queued is a served row's place in the server's builder queue:
	// nil unless the round is queued. Set only by internal/serve's
	// AdminStatus/FlatStatus; always nil from a mastermind's own Status.
	Queued *remote.QueueView `json:"queued,omitempty"`
}

// Key is the UI's row identity. A mastermind row keys by Name; a server row
// keys by owner/name, because two clients may share a binding name.
func (b BindingStatus) Key() string {
	if b.Owner == "" {
		return b.Name
	}
	return b.Owner + "/" + b.Name
}

// ProcessWord returns "remote" when the binding is hosted on a remote server,
// else "headless".
func (b BindingStatus) ProcessWord() string {
	if b.Server != "" {
		return "remote"
	}
	return "headless"
}

// HeadlessInfo is the process half of a headless builder's status row.
// Nil on a pane row.
type HeadlessInfo struct {
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"started_at"` // zero when idle
	LogPath   string    `json:"log_path,omitempty"`
	// ExitCode is "3", or "unknown" when the process is gone without a
	// trailer; "" while running or idle.
	ExitCode string `json:"exit_code,omitempty"`
	// Tail is the log's last few lines, for the human. Never parsed.
	Tail []string `json:"tail,omitempty"`
}

// LastEvent is the most recent relayed message, carried as data rather than
// prose. RenderStatus formats it for a human; a statusline consumer reads the
// fields directly instead of parsing a sentence apart.
type LastEvent struct {
	TS        time.Time       `json:"ts"`
	Round     int             `json:"round"`
	Direction store.Direction `json:"direction"`
	Kind      store.Kind      `json:"kind"`
	// Note is the entry's note, when it has one. A nudge is a plan entry to
	// the builder and a scrape is a report entry to the mastermind, so without
	// it the last line after either reads exactly like the ordinary case.
	Note    string `json:"note,omitempty"`
	Outcome string `json:"outcome,omitempty"`
}

// CloseInfo is the newest round close's commit facts, from its diff log
// entry. Tree is "clean", "dirty", or "" when the entry predates the
// facts or git could not answer.
type CloseInfo struct {
	Round   int    `json:"round"`
	Commits int    `json:"commits"`
	Tree    string `json:"tree"`
}

// PendingInfo describes a payload waiting on the mastermind.
type PendingInfo struct {
	Round int        `json:"round"`
	Kind  store.Kind `json:"kind"`
}

// Report is the whole status surface.
type Report struct {
	Bindings []BindingStatus `json:"bindings"`
	// DoneHidden is the number of DONE rows HideDone removed; zero and absent
	// whenever nothing was filtered, so a consumer that never learned the
	// field sees the document it always did.
	DoneHidden int `json:"done_hidden,omitempty"`
	// Gated lists every live ledger gate against the configured candidates,
	// independent of any binding: a rate limit or spawn failure exists
	// whether or not a builder is currently running it. Absent from JSON
	// when nothing is gated, so a consumer that never learned the field
	// sees the document it always did.
	Gated []availability.Gate `json:"gated,omitempty"`
	// Unused lists the live rate-limit gates on providers no configured
	// candidate uses, so a renamed provider's live gate stays visible
	// instead of blocking nothing in silence. Kept out of JSON: the status
	// document is a pinned contract.
	Unused []ProviderGate `json:"-"`
}

// ApplyRemoteLive sets the live facts from the remote server on the client's
// status row.
func ApplyRemoteLive(row *BindingStatus, lf *store.LiveFacts, stalled bool, logPath string, now time.Time) {
	if lf == nil {
		return
	}
	row.Headless = &HeadlessInfo{
		PID:       lf.PID,
		StartedAt: lf.StartedAt,
		LogPath:   logPath,
		ExitCode:  lf.ExitCode,
		Tail:      lf.Tail,
	}
	if lf.Usage != nil {
		copyU := *lf.Usage
		row.LiveUsage = &copyU
	} else {
		row.LiveUsage = nil
	}
	if lf.Diff != nil {
		row.Live = &LiveDiff{
			Files:   lf.Diff.Files,
			Added:   lf.Diff.Added,
			Removed: lf.Diff.Removed,
		}
	} else {
		row.Live = nil
	}
	row.LastProgressAt = lf.LastProgressAt

	switch {
	case !lf.ExploringSince.IsZero():
		row.BuilderStatus = "exploring " + AgeText(now.Sub(lf.ExploringSince))
		if !stalled {
			row.Exploring = row.BuilderStatus
		}
	case !lf.GatingSince.IsZero():
		row.BuilderStatus = "gating " + AgeText(now.Sub(lf.GatingSince))
	case lf.ExitCode == "unknown":
		row.BuilderStatus = "exited"
	case lf.ExitCode != "":
		row.BuilderStatus = "exited " + lf.ExitCode
	default:
		row.BuilderStatus = "working"
	}
}

// QueueText renders a served, queued round's status word: the bare
// "queued" when the server has not yet reported facts, else the full picture
// -- how many builders are busy on that server, how far back in line, and how
// long it has waited.
func QueueText(q *store.QueueFacts, server string, now time.Time) string {
	if q == nil {
		return "queued"
	}
	return fmt.Sprintf("queued (%d/%d busy on %s, %d ahead, %s)",
		q.Running, q.Cap, server, q.Ahead, AgeText(now.Sub(q.Since)))
}

// IsPayloadKind reports whether k is one of the four kinds that cross
// between mastermind and builder (plan, report, question, answer) -- the ones
// LastPayload tracks, as opposed to relevo's own bookkeeping kinds.
func IsPayloadKind(k store.Kind) bool {
	switch k {
	case store.KindPlan, store.KindReport, store.KindQuestion, store.KindAnswer:
		return true
	default:
		return false
	}
}

// PriorTokensOf returns the sum of tokens from earlier segments in the given
// round: outgoing switch entries that recorded usage, plus any PriorTokens
// carried on the newest report entry (for closed remote rounds). Pure; no I/O.
func PriorTokensOf(entries []store.LogEntry, round int) usage.Tokens {
	var total usage.Tokens
	var reportPrior *usage.Tokens
	for _, e := range entries {
		if e.Round != round {
			continue
		}
		if e.Kind == store.KindSwitch && e.Usage != nil {
			total = total.Add(e.Usage.Tokens)
		}
		if e.Kind == store.KindReport && e.Direction == store.DirToMasterMind && e.PriorTokens != nil {
			reportPrior = e.PriorTokens
		}
	}
	if reportPrior != nil {
		total = total.Add(*reportPrior)
	}
	return total
}

// RoundFacts derives the current round's start, end and report usage from log
// entries. Pure; no I/O. entries is in log order (oldest first).
func RoundFacts(entries []store.LogEntry) (start, end time.Time, u *usage.Usage) {
	var targetRound int
	hasPlan := false
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Kind == store.KindPlan && e.Direction == store.DirToBuilder {
			targetRound = e.Round
			hasPlan = true
			break
		}
	}
	if !hasPlan {
		return time.Time{}, time.Time{}, nil
	}

	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if e.Kind == store.KindPlan && e.Direction == store.DirToBuilder && e.Round == targetRound {
			start = e.TS
			break
		}
	}

	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Kind == store.KindReport && e.Direction == store.DirToMasterMind && e.Round == targetRound && !e.TS.Before(start) {
			end = e.TS
			if e.Usage != nil {
				copyU := *e.Usage
				u = &copyU
			}
			break
		}
	}

	return start, end, u
}

// DisplayState collapses the stored states into the words the human cares
// about. needs_you and broken both mean "a human must act"; paused means the
// worktree was released deliberately and bind --resume brings it back.
func DisplayState(s store.State) string {
	switch s {
	case store.StateNeedsYou, store.StateBroken:
		return "NEEDS YOU"
	case store.StatePaused:
		return "PAUSED"
	case store.StateDone:
		return "DONE"
	default:
		return "ACTIVE"
	}
}

// ShortOwner truncates a client id for a human: "SHA256:" plus the first
// twelve characters after the prefix, then an ellipsis. An id without the
// prefix, or one too short to truncate, is returned unchanged.
func ShortOwner(id string) string {
	const prefix = "SHA256:"
	if !strings.HasPrefix(id, prefix) {
		return id
	}
	// Ids are base64 ASCII, so runes and bytes agree here: unchanged unless
	// the id is longer than the 19-rune "SHA256:" + 12 fingerprint prefix.
	if len([]rune(id)) <= len([]rune(prefix))+12 {
		return id
	}
	return id[:len(prefix)+12] + "…"
}

// HideDone returns a copy of r excluding every binding whose state is DONE.
// The order of the remaining bindings is preserved. DoneHidden is set to
// the count of removed bindings. The input report is not modified.
func HideDone(r Report) Report {
	out := Report{
		Bindings:   make([]BindingStatus, 0, len(r.Bindings)),
		DoneHidden: 0,
		// Gated is machine-wide, not per binding: hiding DONE rows must not
		// hide a rate limit. Found by rendering a hand-written ledger
		// through the real binary; the renderer tests could not see it.
		Gated: r.Gated,
		// Unused is machine-wide too: a gate on a provider no candidate uses
		// belongs to no binding, so hiding DONE rows must not hide it.
		Unused: r.Unused,
	}
	for _, b := range r.Bindings {
		if b.State == string(store.StateDone) {
			out.DoneHidden++
		} else {
			out.Bindings = append(out.Bindings, b)
		}
	}
	return out
}
