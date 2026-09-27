package relevo

import (
	"context"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// usageDeadline bounds one usage read at round close. The round closes
// whatever the reader does; a slow read is a note, not a stall.
const usageDeadline = 5 * time.Second

// liveDeadline bounds one live usage read per binding (#234). Status runs
// on every ui tick and statusline call across every binding, so a slow or
// absent record shows nothing live rather than stalling the refresh.
const liveDeadline = 500 * time.Millisecond

// peekUsage reads what the binding's open round has consumed so far
// (#234): the same reader and source recordUsage uses, asked without
// waiting for the record to close. nil when no round is open, no reader
// is wired, or nothing is readable yet. Never recorded, never summed.
func peekUsage(ctx context.Context, rt Runtime, b store.Binding, now time.Time) *usage.Usage {
	if rt.Usage == nil || b.RoundStartedAt.IsZero() || b.Builder.Kind == "" {
		return nil
	}
	src := roundSource(rt, b, b.RoundStartedAt, now)
	pctx, cancel := context.WithTimeout(ctx, liveDeadline)
	defer cancel()
	samples, note := rt.Usage.Peek(pctx, src)
	if len(samples) == 0 {
		return nil
	}
	u := usage.Fold(samples, rt.Prices, src.Plan, note)
	u.Harness = src.Harness
	if u.Provider == "" {
		u.Provider = src.Provider
	}
	if !src.Start.IsZero() && src.End.After(src.Start) {
		u.DurationMS = src.End.Sub(src.Start).Milliseconds()
	}
	return &u
}

// roundSource is everything the usage reader needs for the binding's
// current round: the builder that closed it, its candidate's provider and
// model when it was spawned from one, the round's stream file, the worktree
// and the window. A local builder is always headless since #303, so the mode
// defaults to headless.
func roundSource(rt Runtime, b store.Binding, start, end time.Time) usage.Source {
	src := usage.Source{
		Harness:  b.Builder.Kind,
		Mode:     usage.ModeHeadless,
		ReadFile: rt.Store.ReadFile,
		Worktree: b.Worktree,
		Start:    start,
		End:      end,
	}
	if b.Builder.Headless() {
		src.StreamPath = rt.Store.StreamPath(b.Name, b.Round)
		src.StreamFrom = b.Builder.StreamStart
	}
	fillCandidate(&src, rt, b.BuilderCandidate)
	return src
}

// consultSource is roundSource for one consult: its own process stream, the
// binding's worktree, its spawn as the window's start. A consult is always a
// process since #303, so the mode is headless and the stream path is the
// process's log.
func consultSource(rt Runtime, b store.Binding, c store.Consult, end time.Time) usage.Source {
	return usage.Source{
		Harness:    c.Endpoint.Kind,
		Mode:       usage.ModeHeadless,
		Worktree:   b.Worktree,
		StreamPath: c.Endpoint.LogPath, // the consult's stream file: stdout, stderr and the exit trailer (#420)
		ReadFile:   rt.Store.ReadFile,
		Start:      c.SpawnedAt,
		End:        end,
	}
}

func fillCandidate(src *usage.Source, rt Runtime, token string) {
	if token == "" || rt.Candidates == nil {
		return
	}
	ref, err := candidate.ParseRef(token)
	if err != nil {
		return
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		src.Provider, src.Model = ref.Provider, ref.Model
		return
	}
	src.Provider, src.Model, src.Plan = c.Provider, c.Model, c.Plan
}

// remoteNoUsage is what the client records for a remote round whose
// server's view carried no usage (#216): the same shape recordUsage
// returns for an unreadable round, with an honest note instead of a
// figure guessed from a record the client does not have.
func remoteNoUsage(rt Runtime, b store.Binding, start, end time.Time) *usage.Usage {
	src := roundSource(rt, b, start, end)
	u := usage.Fold(nil, rt.Prices, src.Plan, "remote: server sent no usage")
	u.Harness = src.Harness
	if u.Provider == "" {
		u.Provider = src.Provider
	}
	if !src.Start.IsZero() && src.End.After(src.Start) {
		u.DurationMS = src.End.Sub(src.Start).Milliseconds()
	}
	return &u
}

// recordUsage reads src and folds it with the runtime's prices. It never
// returns nil and never errors: no reader, a reader that finds nothing,
// and a reader that overruns usageDeadline are all Basis unknown with a
// note.
func recordUsage(ctx context.Context, rt Runtime, src usage.Source) *usage.Usage {
	var u usage.Usage
	if rt.Usage == nil {
		u = usage.Fold(nil, rt.Prices, src.Plan, "no reader")
	} else {
		rctx, cancel := context.WithTimeout(ctx, usageDeadline)
		samples, note := rt.Usage.Read(rctx, src)
		timedOut := rctx.Err() != nil
		cancel()
		u = usage.Fold(samples, rt.Prices, src.Plan, note)
		if len(samples) > 0 && note != "" && !strings.Contains(u.Note, note) {
			if u.Note != "" {
				u.Note += "; "
			}
			u.Note += note
		}
		if timedOut {
			if u.Note != "" {
				u.Note += "; "
			}
			u.Note += "timed out"
		}
	}
	u.Harness = src.Harness
	if u.Provider == "" {
		u.Provider = src.Provider
	}
	if !src.Start.IsZero() && src.End.After(src.Start) {
		u.DurationMS = src.End.Sub(src.Start).Milliseconds()
	}
	// The step figures come from the round's builder stream, read at close
	// (#323, #324). A missing or unreadable stream leaves them zero; this
	// is not part of the reader, so it happens with no reader too.
	if src.StreamPath != "" {
		if data, err := rt.Store.ReadFile(src.StreamPath); err == nil {
			steps := usage.StreamSteps(src.Harness, data)
			u.Steps, u.ToolCalls = steps.Steps, steps.ToolCalls
			u.StepP50MS, u.FirstOutputP50MS = steps.StepP50MS, steps.FirstOutputP50MS
		}
	}
	return &u
}
