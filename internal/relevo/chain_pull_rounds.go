package relevo

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainRoundView is the closed-round facts of round r as the catch-up reads
// them: the member's binding view, its ClosedRound set to r and every other
// closed-round fact taken from the view's Round r, the newest one completed
// by chainRoundFacts. That is what makes one member's older rounds
// installable by the same code a single closed round uses.
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

// chainRoundEntry is round r's own entry from the member view, completed from
// the binding view by chainRoundFacts, and whether the view lists that round at
// all. A round the view does not list is reported false rather than as an empty
// entry, so a caller that needs the round's own facts tells "no entry" from an
// entry that happens to carry none.
func chainRoundEntry(mv remote.ChainMemberView, r int) (remote.ClosedRoundView, bool) {
	for _, cr := range mv.Rounds {
		if cr.Round == r {
			return chainRoundFacts(cr, mv.View), true
		}
	}
	return remote.ClosedRoundView{}, false
}

// chainPullPrompt fetches one round's prompt. The server keeps the prompt it
// staged at the round's own path and serves it as the round's plan file.
func chainPullPrompt(ctx context.Context, rt Runtime, b store.Binding, r int) ([]byte, bool) {
	rc, err := rt.Remote.RoundFile(ctx, b.Builder.Server, b.Name, r, "plan")
	if err != nil {
		if !remote.Is404(err) {
			slog.Warn("chain prompt fetch failed", "server", b.Builder.Server, "name", b.Name, "round", r, "err", err)
		}
		return nil, false
	}
	defer rc.Close()
	body, rerr := io.ReadAll(io.LimitReader(rc, chainPullMaxPlanBytes+1))
	if rerr != nil || len(body) > chainPullMaxPlanBytes {
		slog.Warn("chain prompt over cap; not stored", "server", b.Builder.Server, "name", b.Name, "round", r)
		return nil, false
	}
	return body, true
}

// chainPullPromptEntry writes the round's prompt file and records the entry
// that says the member was handed it. A round whose entry already exists is
// left alone, so a re-run of an interrupted install never doubles it.
//
// The entry carries the server's own start for the round, so a mirror's
// duration is the round's real one rather than the gap between the pull and the
// close. A server that sends no start -- one older than the field -- falls back
// to the clock, which is the ingest time the mirror always recorded before.
func chainPullPromptEntry(rt Runtime, tx *store.Tx, b store.Binding, r int, body []byte, startedAt time.Time) error {
	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return err
	}
	if HasPromptEntry(entries, r) {
		return nil
	}
	path := rt.Store.PromptPath(b.Name, r)
	if err := stagePlan(path, body); err != nil {
		return fmt.Errorf("%s: stage prompt at %s: %w", b.Name, path, err)
	}
	return tx.AppendLog(b.Name, store.LogEntry{
		TS: chainPullStart(rt, startedAt), Round: r, Direction: store.DirToBuilder,
		Kind: store.KindPrompt, Path: path, Confirmed: true,
		Note: chainStepNote(tx, b.Name),
	})
}

// chainPullStart is the stamp a pulled round carries: the server's own start
// when it sent one, and this machine's clock when it did not.
func chainPullStart(rt Runtime, startedAt time.Time) time.Time {
	if startedAt.IsZero() {
		return rt.Now().UTC()
	}
	return startedAt.UTC()
}
