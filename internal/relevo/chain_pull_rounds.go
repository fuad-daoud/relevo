package relevo

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainRoundView is round r's closed-round facts as the catch-up reads them:
// the binding view with ClosedRound set to r and every other closed-round fact
// from the view's Round r. That makes older rounds installable by the same
// code a single closed round uses.
func chainRoundView(mv remote.ChainMemberView, r int) remote.BindingView {
	v := mv.View
	v.ClosedRound = r
	cr, ok := chainRoundEntry(mv, r)
	if !ok {
		return v
	}
	v.ReportOutcome = cr.ReportOutcome
	v.GateResult = cr.GateResult
	v.Stopped = cr.Stopped
	v.ReportNote = cr.ReportNote
	v.Switches = cr.Switches
	v.DiffNote = cr.DiffNote
	v.DiffCommits = cr.DiffCommits
	v.DiffTree = cr.DiffTree
	v.DirtyCommit = cr.DirtyCommit
	v.Usage = cr.Usage
	v.Rusage = cr.Rusage
	v.PriorTokens = cr.PriorTokens
	v.RoundStartedAt = cr.RoundStartedAt
	return v
}

// chainRoundEntry is round r's own entry from the member view, completed by
// chainRoundFacts, and whether the view lists that round at all. An unlisted
// round reports false rather than an empty entry, so "no entry" reads apart
// from an entry carrying none.
func chainRoundEntry(mv remote.ChainMemberView, r int) (remote.ClosedRoundView, bool) {
	for _, cr := range mv.Rounds {
		if cr.Round == r {
			return remote.ChainRoundFacts(cr, mv.View), true
		}
	}
	return remote.ClosedRoundView{}, false
}

// chainPullPrompt fetches one round's prompt: the staged prompt the server
// serves as the round's plan file.
func chainPullPrompt(ctx context.Context, rt Runtime, b store.Binding, r int) ([]byte, bool) {
	rc, err := rt.Remote.RoundFile(ctx, b.Builder.Server, b.Name, r, "plan")
	if err != nil {
		if !remote.Is404(err) {
			slog.Warn("chain prompt fetch failed", "server", b.Builder.Server, "name", b.Name, "round", r, "err", err)
		}
		return nil, false
	}
	defer func() {
		if cerr := rc.Close(); cerr != nil {
			slog.Warn("chain prompt close failed", "server", b.Builder.Server, "name", b.Name, "round", r, "err", cerr)
		}
	}()
	body, rerr := io.ReadAll(io.LimitReader(rc, chainPullMaxPlanBytes+1))
	if rerr != nil || len(body) > chainPullMaxPlanBytes {
		slog.Warn("chain prompt over cap; not stored", "server", b.Builder.Server, "name", b.Name, "round", r)
		return nil, false
	}
	return body, true
}

// chainPullPromptEntry writes the round's prompt file and records the entry
// saying the member was handed it. An existing entry is left alone, so a
// re-run of an interrupted install never doubles it.
//
// The entry carries the server's own start, so a mirror's duration is the
// round's real one rather than the pull-to-close gap. A server sending no
// start falls back to the clock, the ingest time the mirror always recorded
// before.
func chainPullPromptEntry(rt Runtime, tx *store.Tx, b store.Binding, r int, body []byte, startedAt time.Time) error {
	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return err
	}
	if HasPromptEntry(entries, r) {
		return nil
	}
	path := rt.Store.PromptPath(b.Name, r)
	if err := StagePlan(path, body); err != nil {
		return fmt.Errorf("%s: stage prompt at %s: %w", b.Name, path, err)
	}
	return tx.AppendLog(b.Name, store.LogEntry{
		TS: chainPullStart(rt, startedAt), Round: r, Direction: store.DirToBuilder,
		Kind: store.KindPrompt, Path: path, Confirmed: true,
		Note: chain.StepNote(tx, b.Name),
	})
}

// chainPullStart is the stamp a pulled round carries: the server's own start,
// or this machine's clock when it sent none.
func chainPullStart(rt Runtime, startedAt time.Time) time.Time {
	if startedAt.IsZero() {
		return rt.Now().UTC()
	}
	return startedAt.UTC()
}
