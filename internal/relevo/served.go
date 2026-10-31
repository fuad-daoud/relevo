package relevo

import (
	"context"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// ServedView is the wire view of an owned binding. recordID is the
// server's binding_record id and installation the server's own installation id;
// both are what a client records to link its row to this server's copy, and
// both are "" for a caller that holds neither.
func ServedView(b store.Binding, entries []store.LogEntry, recordID, installation string) remote.BindingView {
	rState := remote.RoundStateOf(b, entries)
	// A halted or broken binding ships its reason: without it the client shows
	// the state word and nothing to act on.
	var halt string
	if b.State == store.StateNeedsYou || b.State == store.StateBroken {
		halt = b.Halt
	}
	var resultCommit, dirtyCommit string
	if rState == remote.RoundClosed && b.Serve != nil {
		resultCommit = b.Serve.ResultCommit
		dirtyCommit = b.Serve.DirtyCommit
	}
	var facts remote.ClosedRoundView
	if b.Serve != nil && b.Serve.ClosedRound > 0 {
		facts = servedRoundFacts(entries, b.Serve.ClosedRound)
		// The binding's own start answers only for the round it is on now, so
		// it completes a newest closed round whose own entries named none. A
		// binding whose closed round is not its current one leaves it zero, and
		// the log-derived start stands on its own.
		if facts.RoundStartedAt.IsZero() && b.Serve.ClosedRound == b.Round-1 {
			facts.RoundStartedAt = b.RoundStartedAt
		}
	}
	var ackedRound int
	var closedRound int
	if b.Serve != nil {
		ackedRound = b.Serve.AckedRound
		closedRound = b.Serve.ClosedRound
	}
	return remote.BindingView{
		ID:             recordID,
		Installation:   installation,
		Name:           b.Name,
		State:          string(b.State),
		Round:          b.Round,
		RoundState:     rState,
		ClosedRound:    closedRound,
		Halt:           halt,
		ResultCommit:   resultCommit,
		DirtyCommit:    dirtyCommit,
		ReportOutcome:  facts.ReportOutcome,
		GateResult:     facts.GateResult,
		Stopped:        facts.Stopped,
		ReportNote:     facts.ReportNote,
		Switches:       facts.Switches,
		Shape:          b.Shape,
		DiffNote:       facts.DiffNote,
		DiffCommits:    facts.DiffCommits,
		DiffTree:       facts.DiffTree,
		AckedRound:     ackedRound,
		Candidate:      b.BuilderCandidate,
		Account:        b.BuilderAccount,
		RoundStartedAt: chainServedRoundStart(b, facts.RoundStartedAt),
		RoundCap:       b.RoundCap,
		RoundTimeoutMS: b.RoundTimeoutMS,
		Tier:           string(resolve.EffectiveTier(b)),
		Feature:        b.Feature,
		Ticket:         b.Ticket,
		Usage:          facts.Usage,
		PriorTokens:    facts.PriorTokens,
		Rusage:         facts.Rusage,
		StalledSince:   b.StalledSince,
	}
}

// chainServedRoundStart is the start ServedView ships: the closed round's own
// start when the binding has one and a closed round at all, and the live round's
// stamp otherwise. A binding whose round is still open has no closed round to
// describe, so its running start is the only one a client can mean.
func chainServedRoundStart(b store.Binding, closedStart time.Time) time.Time {
	if b.Serve != nil && b.Serve.ClosedRound > 0 && !closedStart.IsZero() {
		return closedStart
	}
	return b.RoundStartedAt
}

// servedRoundFacts is the per-round scan for round n. Every field names the
// newest entry for round n of its kind. n <= 0 yields the zero value.
func servedRoundFacts(entries []store.LogEntry, n int) remote.ClosedRoundView {
	var f remote.ClosedRoundView
	if n <= 0 {
		return f
	}
	f.Round = n
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Round == n && entries[i].Kind == store.KindReport {
			f.ReportOutcome = entries[i].Outcome
			f.ReportNote = entries[i].Note
			f.Usage = entries[i].Usage
			f.Rusage = entries[i].Rusage
			break
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Round == n && entries[i].Kind == store.KindReport && entries[i].Gate != nil {
			f.GateResult = entries[i].Gate.Result
			break
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Round == n && entries[i].Kind == store.KindDiff {
			f.DiffNote = entries[i].Note
			f.DiffCommits = entries[i].Commits
			f.DiffTree = entries[i].Tree
			break
		}
	}
	// switches is the round's whole switch history, in log order, so a round
	// that rotated or went A-B-A carries every switch it took rather than only
	// the one a client's poll could have happened to observe. Nudge entries are
	// excluded by note, as switchLines excludes them.
	for _, e := range entries {
		if e.Round == n && e.Kind == store.KindSwitch && e.Note != nudgeNote && e.Note != "" {
			f.Switches = append(f.Switches, e.Note)
		}
	}
	// priorTokens is the round's own earlier segments: the sum store.PriorTokensOf
	// walks for n. Nil when the round used none, so a client keys each round's
	// figure off that round rather than off the newest closed one.
	if pt := store.PriorTokensOf(entries, n); pt.Total() > 0 {
		f.PriorTokens = &pt
	}
	// stopped is how the round was stopped: the newest KindStop entry for n
	// whose note names one ("stopped/killed", "stopped/reaped", "stopped/gone"
	// or "stopped/dequeued"). A close any other way writes no such entry.
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Round == n && entries[i].Kind == store.KindStop {
			stopped := strings.TrimPrefix(entries[i].Note, "stopped/")
			if stopped != entries[i].Note {
				f.Stopped = stopped
			}
			break
		}
	}
	f.RoundStartedAt, f.QueuedAt = servedRoundStamps(entries, n)
	return f
}

// ResolveServedTierFor is the served counterpart of add.go's
// resolveTier+checkTierCap, for the binding's role. token is the candidate
// PickServedCandidateFor chose (may be "" or unresolvable: then the candidate
// contributes no tier); explicit is the wire tier ("" = none).
//
//	chain: explicit > candidate.Tier > policy's tier.<role> > harness
//	cap:   resolve.CheckTierCap(tier, rt.Policy, allowYolo=false) -- the server's
//	       max_tier is a ceiling nothing on the wire lifts
//
// Errors: harness.ParseTier's error for a malformed explicit;
// resolve.ErrTierAboveMax (wrapped, message names the ceiling) above the cap.
// explicit == "" can still fail: a policy tier.<role> above max_tier is a
// policy authoring error and returns resolve.ErrTierAboveMax too, so the create
// fails loudly instead of launching at a tier the policy forbids (add.go
// behaves the same locally).
func ResolveServedTierFor(rt Runtime, role, token, explicit string) (harness.Tier, error) {
	var c candidate.Candidate
	if rt.Candidates != nil && token != "" {
		if ref, err := candidate.ParseRef(token); err == nil {
			if c2, err := rt.Candidates.Lookup(ref); err == nil {
				c = c2
			}
		}
	}
	if explicit != "" {
		if _, err := harness.ParseTier(explicit); err != nil {
			return "", err
		}
	}
	tier := resolve.ResolveTier(explicit, c, rt.RoleRegistry(), role)
	if err := resolve.CheckTierCap(tier, rt.Policy, false); err != nil {
		return tier, err
	}
	return tier, nil
}

// ResolveServedTier is ResolveServedTierFor for the built-in builder role.
func ResolveServedTier(rt Runtime, token, explicit string) (harness.Tier, error) {
	return ResolveServedTierFor(rt, "builder", token, explicit)
}

// RenderServedTier validates that kind can run tier for role, flooring a
// reader through readerTier first exactly as add.go does, and returns the
// tier to store. It renders the launch with an empty prompt, which skips the
// definition read, so the call is a pure validation with no other file use.
// An empty kind, an unresolvable token or an unknown role skips the render:
// there is no harness to refuse. A refusal wraps harness.ErrTierUnsupported
// or harness.ErrExtraArgsPermission, the same refusal a local bind produces.
func RenderServedTier(rt Runtime, role, token, kind string, tier harness.Tier) (harness.Tier, error) {
	if kind == "" {
		return tier, nil
	}
	if shape, err := resolve.ActorShape(rt.RoleRegistry(), role); err == nil && shape == store.ShapeReader {
		var rerr error
		if tier, rerr = resolve.ReaderTier(tier, kind, rt.Policy); rerr != nil {
			return tier, rerr
		}
	}
	var c candidate.Candidate
	if rt.Candidates == nil || token == "" {
		return tier, nil
	}
	ref, err := candidate.ParseRef(token)
	if err != nil {
		return tier, nil
	}
	c, err = rt.Candidates.Lookup(ref)
	if err != nil {
		return tier, nil
	}
	spec, err := rt.RoleRegistry().Spec(role, kind)
	if err != nil {
		return tier, nil
	}
	if _, err := candidate.HeadlessLaunch(c, spec, tier, 0, "", "", "", "", ""); err != nil {
		return tier, err
	}
	return tier, nil
}

// ServedBuilderTier is what WhoAmI reports and relevo serve logs at startup:
// ResolveServedTier(rt, PickServedCandidate(rt, ""), ""), falling back to
// harness (not an error) when the chain refuses.
func ServedBuilderTier(rt Runtime) harness.Tier {
	token, _, _ := PickServedCandidate(rt, "")
	tier, err := ResolveServedTier(rt, token, "")
	if err != nil {
		return harness.TierHarness
	}
	return tier
}

// PickServedCandidateFor resolves the role's candidate token and harness kind
// for a served binding. An explicit token the role refuses is returned as that
// refusal, never swapped for another candidate: the create that named it must
// answer, not silently serve someone else. With no token it is the role's
// ranked pick, and a role the gates refuse -- every candidate serving it
// gated, resolve.ErrAllGated -- is returned as that refusal too: a served create is
// answered with the refusal a local bind answers with, not with a member
// whose candidate is empty. The one non-refusal empty answer is a runtime
// with no candidates configured at all, which is the shape's own answer.
func PickServedCandidateFor(rt Runtime, role, token string) (string, string, error) {
	if rt.Candidates == nil {
		return token, "", nil
	}
	res, err := resolve.ResolveRole(rt.RoleRegistry(), rt.Candidates, availability.Gates(AvailabilityDeps(rt)), token, role)
	if err != nil {
		if token != "" {
			return token, "", err
		}
		return "", "", err
	}
	return res.Candidate.Ref().String(), res.Candidate.Harness, nil
}

// PickServedCandidate is PickServedCandidateFor for the built-in builder role.
func PickServedCandidate(rt Runtime, token string) (string, string, error) {
	return PickServedCandidateFor(rt, "builder", token)
}

func liveViewOf(row view.BindingStatus, b store.Binding, at time.Time) *remote.LiveView {
	v := &remote.LiveView{
		At:             at,
		Usage:          row.LiveUsage,
		PriorTokens:    row.RoundPriorTokens,
		LastProgressAt: row.LastProgressAt,
		ExploringSince: b.ExploringSince,
	}
	if row.Headless != nil {
		v.PID = row.Headless.PID
		v.StartedAt = row.Headless.StartedAt
		v.ExitCode = row.Headless.ExitCode
		v.Tail = row.Headless.Tail
	}
	if row.Live != nil {
		v.Diff = &remote.DiffStat{
			Files:   row.Live.Files,
			Added:   row.Live.Added,
			Removed: row.Live.Removed,
		}
	}
	if b.GateRun != nil {
		v.GatingSince = time.Unix(b.GateRun.StartedAt, 0)
	}
	return v
}

// ServedLive returns the live view of an owned binding's running round.
// It is the one single-row surface a client reads, and every figure it carries
// (usage, tail, diff) is a detail figure, so the row is built with them on.
func ServedLive(ctx context.Context, rt Runtime, b store.Binding) (*remote.LiveView, error) {
	row, err := statusRow(ctx, rt, b, statusConfig{detail: true})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if rt.Now != nil {
		now = rt.Now()
	}
	return liveViewOf(row, b, now), nil
}
