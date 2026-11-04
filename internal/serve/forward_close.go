package serve

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// closeForwardedRound records a round a node ran as closed on the head. The
// close facts come from what the node sent back -- the absorbed branch tip
// and, when the node kept uncommitted work, its side ref -- never from the
// head worktree: the round did not run there, so that tree still holds the
// state before the round, and reading it as dirt would record the node's
// work as undone. The stale worktree is removed for the same reason; the
// next served round checks it out again from the branch. Anything an
// earlier local round left uncommitted there is already kept in that
// round's side ref.
func closeForwardedRound(ctx context.Context, rt relevo.Runtime, b *store.Binding, fwd store.Forward, nodeDirty bool) bool {
	if b.Serve == nil || b.Branch == "" {
		return false
	}
	head, ok, err := rt.Git.RefSHA(ctx, b.Serve.BareRepo, "refs/heads/"+b.Branch)
	if err != nil || !ok {
		slog.Warn("burst: forwarded close: branch missing", "binding", b.Name, "branch", b.Branch, "err", err)
		return false
	}
	dirty := ""
	if nodeDirty {
		side := "refs/relevo/" + b.Name + "/round-" + strconv.Itoa(fwd.Round)
		sha, ok, err := rt.Git.RefSHA(ctx, b.Serve.BareRepo, side)
		if err != nil || !ok {
			slog.Warn("burst: forwarded close: side ref missing", "binding", b.Name, "ref", side, "err", err)
			return false
		}
		dirty = sha
	}
	if b.Worktree != "" {
		if _, statErr := os.Stat(b.Worktree); statErr == nil {
			if err := rt.Git.RemoveWorktree(ctx, b.Serve.BareRepo, b.Worktree, true); err != nil {
				slog.Warn("burst: forwarded close: stale worktree not removed", "binding", b.Name, "err", err)
				return false
			}
		}
	}
	b.Serve.ClosedRound = fwd.Round
	b.Serve.ResultCommit = head
	b.Serve.DirtyCommit = dirty
	return true
}

// putForwardedDiff files the node's patch where a local close files its own. A
// diff is a row-only name: on disk it is no record at all, so a show would find
// no diff, and every walk of the round's files would warn about a stray.
func putForwardedDiff(rt relevo.Runtime, name string, round int, body []byte) bool {
	if body == nil {
		return true
	}
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile(name, round, rt.Store.DiffPath(name, round), body)
	})
	if err != nil {
		slog.Warn("burst: observe close: diff not stored", "binding", name, "round", round, "err", err)
		return false
	}
	return true
}

// appendForwardedCloseFacts writes into the head log the entries a local
// close of this round would have written: one diff entry, one report entry,
// one switch entry per switch the node took, and the stop entry when the node
// stopped the round. The head's listings, chain pulls and ingest read those
// entries, so without them a forwarded round shows no gate result, no diff
// and no stop, and the burst columns of its history row stay NULL. Every
// fact is copied from the view the node shipped: the round did not run here,
// so the head measures nothing. The guard below covers the whole helper:
// a close whose binding save failed is retried on the next tick, and a
// retried close must append nothing a second time.
func appendForwardedCloseFacts(rt relevo.Runtime, name string, fwd store.Forward, fetch *observeFetch, now time.Time) bool {
	view := fetch.view
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		slog.Warn("burst: observe close: log unreadable, close deferred", "binding", name, "err", err)
		return false
	}
	// One of the two always marks a completed run of this helper: the report
	// entry when a report came home, the stop entry for the round that
	// stopped without one. With neither, nothing here has been written yet.
	if store.HasKind(entries, fwd.Round, store.DirToMasterMind, store.KindReport) ||
		store.HasKind(entries, fwd.Round, store.DirToMasterMind, store.KindStop) {
		return true
	}
	if view.DiffNote != "" && !store.HasKind(entries, fwd.Round, store.DirToMasterMind, store.KindDiff) {
		path := ""
		if fetch.diff != nil {
			path = rt.Store.DiffPath(name, fwd.Round)
		}
		if err := rt.Store.AppendLog(name, store.LogEntry{
			TS: now, Round: fwd.Round, Direction: store.DirToMasterMind, Kind: store.KindDiff,
			Path: path, Note: view.DiffNote, Commits: view.DiffCommits, Tree: view.DiffTree,
			Confirmed: true,
		}); err != nil {
			slog.Warn("burst: observe close: diff entry not appended", "binding", name, "round", fwd.Round, "err", err)
			return false
		}
	}
	if fetch.report != nil && !appendForwardedCloseReport(rt, name, fwd, fetch, now) {
		return false
	}
	for _, sw := range view.Switches {
		if sw == "" || sw == store.NudgeNote {
			continue
		}
		if err := rt.Store.AppendLog(name, store.LogEntry{
			TS: now, Round: fwd.Round, Direction: store.DirToMasterMind, Kind: store.KindSwitch,
			Note: sw, Confirmed: true,
		}); err != nil {
			slog.Warn("burst: observe close: switch entry not appended", "binding", name, "round", fwd.Round, "err", err)
			return false
		}
	}
	if view.Stopped != "" {
		if err := rt.Store.AppendLog(name, store.LogEntry{
			TS: now, Round: fwd.Round, Direction: store.DirToMasterMind, Kind: store.KindStop,
			Note: "stopped/" + view.Stopped, Confirmed: true,
		}); err != nil {
			slog.Warn("burst: observe close: stop entry not appended", "binding", name, "round", fwd.Round, "err", err)
			return false
		}
	}
	return true
}

// appendForwardedCloseReport writes the round's report entry from the node's
// view. TS is the head's close time: the wire carries no node close
// timestamp, and the node's own entry was sealed on its machine. Outcome,
// note, usage, rusage and prior tokens land verbatim from the view, and the
// gate result lands as a Gate record with only the result the wire carries,
// the way a remote catch-up files it. Confirmed follows a local close: an
// unconsumed report is left unconfirmed so the owner daemon delivers it on
// the standard path, and one a running chain holds is marked consumed with
// the chain's name, because the chain's own end delivery is the MasterMind's
// copy. The node's earlier segments ride the PriorTokens field, never a
// second parse of the fetched stream: the node already folded them.
func appendForwardedCloseReport(rt relevo.Runtime, name string, fwd store.Forward, fetch *observeFetch, now time.Time) bool {
	view := fetch.view
	note := view.ReportNote
	confirmed := false
	if chainName, consumed := forwardedCloseChain(rt, name); consumed {
		confirmed = true
		if note != "" {
			note += " "
		}
		note += "consumed by chain " + chainName
	}
	entry := store.LogEntry{
		TS: now, Round: fwd.Round,
		Direction: store.DirToMasterMind, Kind: store.KindReport,
		Path: rt.Store.ReportPath(name, fwd.Round), Payload: string(fetch.report),
		Note: note, Outcome: view.ReportOutcome, Confirmed: confirmed,
	}
	if view.Usage != nil {
		u := *view.Usage
		entry.Usage = &u
	}
	if view.PriorTokens != nil {
		p := *view.PriorTokens
		entry.PriorTokens = &p
	}
	entry.Rusage = view.Rusage
	if view.GateResult != "" {
		entry.Gate = &store.GateRecord{Result: view.GateResult}
	}
	if err := rt.Store.AppendLog(name, entry); err != nil {
		slog.Warn("burst: observe close: report entry not appended", "binding", name, "round", fwd.Round, "err", err)
		return false
	}
	return true
}

// forwardedCloseChain names the running chain that consumes this member's
// close, the way the engine's queueReport decides before it files the report
// entry. No chain, or any other status, leaves the report for delivery.
func forwardedCloseChain(rt relevo.Runtime, name string) (string, bool) {
	var row db.ChainRow
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var cerr error
		row, cerr = tx.ChainByMember(name)
		return cerr
	})
	if err != nil || row.Status != string(chain.StatusRunning) {
		return "", false
	}
	return row.Name, true
}
