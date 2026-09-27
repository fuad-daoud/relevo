package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// RoundStateOf returns the execution state of an owned binding (remote-builders spec §3.1).
// It is derived live from the binding and its entries, never stored.
func RoundStateOf(b store.Binding, entries []store.LogEntry) remote.RoundState {
	if b.State == store.StateNeedsYou {
		return remote.RoundNeedsYou
	}
	open := HasEntry(entries, b.Round, store.DirToBuilder, store.KindPlan) &&
		!HasEntry(entries, b.Round, store.DirToPlanner, store.KindReport)
	if open && !b.QueuedAt.IsZero() {
		return remote.RoundQueued
	}
	if open {
		return remote.RoundRunning
	}
	if b.Serve != nil && b.Serve.ClosedRound > b.Serve.AckedRound {
		return remote.RoundClosed
	}
	return remote.RoundIdle
}

// closeServedRound runs once per closed round on an owned binding, inside
// reconcileHeadless after closeOnMarker reports closed and before
// clearProcess. It never fails the close: any git error is a slog.Warn and
// the facts stay at their previous values, so the owner's next GET shows the
// round still running until the next tick retries.
func closeServedRound(ctx context.Context, rt Runtime, b store.Binding) store.Binding {
	if b.Owner == "" || b.Serve == nil {
		return b
	}
	closed := b.Round - 1
	head, ok, err := rt.Git.RefSHA(ctx, b.Serve.BareRepo, "refs/heads/"+b.Branch)
	if err != nil || !ok {
		slog.Warn("branch missing at close", "binding", b.Name, "branch", b.Branch, "err", err)
		return b
	}
	dirty, err := rt.Git.Dirty(ctx, b.Worktree)
	if err != nil {
		slog.Warn("dirty check failed at close", "binding", b.Name, "err", err)
		return b
	}
	dirtyCommit := ""
	if dirty {
		tree, err := rt.Git.SnapshotTree(ctx, b.Worktree)
		if err != nil {
			slog.Warn("snapshot tree failed at close", "binding", b.Name, "err", err)
			return b
		}
		sha, err := rt.Git.CommitTree(ctx, b.Serve.BareRepo, tree, head,
			fmt.Sprintf("[relevo] %s: round %d, uncommitted work", b.Name, closed))
		if err != nil {
			slog.Warn("commit tree failed at close", "binding", b.Name, "err", err)
			return b
		}
		if err := rt.Git.UpdateRef(ctx, b.Serve.BareRepo, fmt.Sprintf("refs/relevo/%s/round-%d", b.Name, closed), sha, ""); err != nil {
			slog.Warn("update ref failed at close", "binding", b.Name, "err", err)
			return b
		}
		dirtyCommit = sha
	}
	b.Serve.ClosedRound = closed
	b.Serve.ResultCommit = head
	b.Serve.DirtyCommit = dirtyCommit
	return b
}

// ServedView is the wire view of an owned binding (spec §3.1).
func ServedView(b store.Binding, entries []store.LogEntry) remote.BindingView {
	rState := RoundStateOf(b, entries)
	var halt string
	if b.State == store.StateNeedsYou {
		halt = b.Halt
	}
	var resultCommit, dirtyCommit string
	if rState == remote.RoundClosed && b.Serve != nil {
		resultCommit = b.Serve.ResultCommit
		dirtyCommit = b.Serve.DirtyCommit
	}
	var reportOutcome string
	var reportUsage *usage.Usage
	var reportRusage *store.Rusage
	if b.Serve != nil && b.Serve.ClosedRound > 0 {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Round == b.Serve.ClosedRound && entries[i].Kind == store.KindReport {
				reportOutcome = entries[i].Outcome
				reportUsage = entries[i].Usage
				reportRusage = entries[i].Rusage
				break
			}
		}
	}
	var diffNote, diffTree string
	var diffCommits int
	if b.Serve != nil && b.Serve.ClosedRound > 0 {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Round == b.Serve.ClosedRound && entries[i].Kind == store.KindDiff {
				diffNote = entries[i].Note
				diffCommits = entries[i].Commits
				diffTree = entries[i].Tree
				break
			}
		}
	}
	// stopped is how the closed round was stopped: the newest KindStop entry
	// for ClosedRound whose note names one ("stopped/killed",
	// "stopped/reaped", "stopped/gone" or "stopped/dequeued"). A close any
	// other way writes no such entry, and the field stays "" (#344).
	var stopped string
	if b.Serve != nil && b.Serve.ClosedRound > 0 {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Round == b.Serve.ClosedRound && entries[i].Kind == store.KindStop {
				stopped = strings.TrimPrefix(entries[i].Note, "stopped/")
				if stopped == entries[i].Note {
					stopped = ""
				}
				break
			}
		}
	}
	var ackedRound int
	var closedRound int
	var priorTokens *usage.Tokens
	if b.Serve != nil {
		ackedRound = b.Serve.AckedRound
		closedRound = b.Serve.ClosedRound
		if closedRound > 0 {
			pt := view.PriorTokensOf(entries, closedRound)
			if pt.Total() > 0 {
				priorTokens = &pt
			}
		}
	}
	return remote.BindingView{
		Name:           b.Name,
		State:          string(b.State),
		Round:          b.Round,
		RoundState:     rState,
		ClosedRound:    closedRound,
		Halt:           halt,
		ResultCommit:   resultCommit,
		DirtyCommit:    dirtyCommit,
		ReportOutcome:  reportOutcome,
		Stopped:        stopped,
		DiffNote:       diffNote,
		DiffCommits:    diffCommits,
		DiffTree:       diffTree,
		AckedRound:     ackedRound,
		Candidate:      b.BuilderCandidate,
		RoundStartedAt: b.RoundStartedAt,
		RoundCap:       b.RoundCap,
		RoundTimeoutMS: b.RoundTimeoutMS,
		Tier:           string(effectiveTier(b)),
		Usage:          reportUsage,
		PriorTokens:    priorTokens,
		Rusage:         reportRusage,
		StalledSince:   b.StalledSince,
	}
}

// ResolveServedTierFor is the served counterpart of add.go's
// resolveTier+checkTierCap, for the binding's role. token is the candidate
// PickServedCandidateFor chose (may be "" or unresolvable: then the candidate
// contributes no tier); explicit is the wire tier ("" = none).
//
//	chain: explicit > candidate.Tier > policy's tier.<role> > harness
//	cap:   checkTierCap(tier, rt.Policy, allowYolo=false) -- the server's
//	       max_tier is a ceiling nothing on the wire lifts
//
// Errors: harness.ParseTier's error for a malformed explicit;
// ErrTierAboveMax (wrapped, message names the ceiling) above the cap.
// explicit == "" can still fail: a policy tier.<role> above max_tier is a
// policy authoring error and returns ErrTierAboveMax too, so the create
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
	tier := resolveRoleTier(explicit, c, rt.RoleRegistry(), role)
	if err := checkTierCap(tier, rt.Policy, false); err != nil {
		return tier, err
	}
	return tier, nil
}

// ResolveServedTier is ResolveServedTierFor for the built-in builder role.
func ResolveServedTier(rt Runtime, token, explicit string) (harness.Tier, error) {
	return ResolveServedTierFor(rt, "builder", token, explicit)
}

// ServedBuilderTier is what WhoAmI reports and relevo serve logs at startup:
// ResolveServedTier(rt, PickServedCandidate(rt, ""), ""), falling back to
// harness (not an error) when the chain refuses.
func ServedBuilderTier(rt Runtime) harness.Tier {
	token, _ := PickServedCandidate(rt, "")
	tier, err := ResolveServedTier(rt, token, "")
	if err != nil {
		return harness.TierHarness
	}
	return tier
}

// PickServedCandidateFor resolves the role's candidate token and harness kind
// for a served binding.
func PickServedCandidateFor(rt Runtime, role, token string) (string, string) {
	if rt.Candidates == nil {
		return token, ""
	}
	res, err := resolveRole(rt.RoleRegistry(), rt.Candidates, availability.Gates(AvailabilityDeps(rt)), token, role)
	if err == nil {
		return res.Candidate.Ref().String(), res.Candidate.Harness
	}
	if token != "" {
		// The token may be a candidate name or a canonical token (A1 §4.2).
		if c, err := rt.Candidates.Resolve(token); err == nil {
			return c.Ref().String(), c.Harness
		}
	}
	return token, ""
}

// PickServedCandidate is PickServedCandidateFor for the built-in builder role.
func PickServedCandidate(rt Runtime, token string) (string, string) {
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

// ServedLive returns the live view of an owned binding's running round (spec §3.1).
func ServedLive(ctx context.Context, rt Runtime, b store.Binding) (*remote.LiveView, error) {
	row, err := statusRow(ctx, rt, b)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if rt.Now != nil {
		now = rt.Now()
	}
	return liveViewOf(row, b, now), nil
}
