package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// catchUp fetches a closed round's files and bundle and installs them under
// tx. Callers that fetched ahead pass their fetch to applyCatchUp instead, so
// only the inline one-off path pays the fetch here.
func catchUp(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, view remote.BindingView) (store.Binding, error) {
	cf := fetchCatchUp(ctx, rt, b, view)
	defer cf.release()
	next, a, err := applyCatchUp(ctx, rt, tx, b, view, cf)
	if a != nil {
		return settleCatchUpInline(ctx, rt, tx, next, a)
	}
	return next, err
}

// catchUpAck is a catch-up the apply half installed whose ack has not left yet.
// The ack runs outside the state lock; only a successful one unblocks the
// report entry.
type catchUpAck struct {
	Server       string             // b.Builder.Server at the apply
	Name         string             // b.Name at the apply
	Round        int                // view.ClosedRound: the round to ack and to file the report under
	BindingRound int                // b.Round at the apply: guards the report against a binding that moved on
	View         remote.BindingView // the closed-round facts the report entry is built from
	HaveReport   bool               // a report temp was renamed into place
	HaveDiff     bool               // the diff body was stored
	GatePath     string             // where the fetched acceptance check's log landed, "" if none arrived
}

// matches reports whether b is still the binding the ack was for: same name,
// same round, still remote, and in a known live state. A false answer means the
// report is not queued; the pass that moved the binding owns it.
func (a *catchUpAck) matches(b store.Binding) bool {
	return b.Name == a.Name &&
		b.Round == a.BindingRound &&
		b.Builder.Remote() &&
		b.State != store.StateDone &&
		b.State != store.StatePaused &&
		store.KnownState(b.State)
}

// ackCatchUp acks a collected round outside the state lock. A failure is the
// same retry as before: warned here, no report queued, the still-closed round
// re-collected by the next pass.
func ackCatchUp(ctx context.Context, rt Runtime, a *catchUpAck) error {
	if _, err := rt.Remote.Ack(ctx, a.Server, a.Name, a.Round); err != nil {
		slog.Warn("ack failed", "server", a.Server, "name", a.Name, "round", a.Round, "err", err)
		return err
	}
	return nil
}

// settleCatchUpInline finishes a catch-up for a caller that still holds the
// lock: today's one-pass behaviour, for the inline paths.
func settleCatchUpInline(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, a *catchUpAck) (store.Binding, error) {
	if err := ackCatchUp(ctx, rt, a); err != nil {
		return b, nil
	}
	return applyCatchUpReport(ctx, rt, tx, b, a)
}

// applyCatchUpFiles renames the fetched report, log and stream temps into
// place and stores the diff and DB-form log under tx. A failure anywhere logs
// as the inline write did and leaves the binding for the next tick.
func applyCatchUpFiles(rt Runtime, tx *store.Tx, b store.Binding, view remote.BindingView, cf *catchUpFetch) bool {
	// A reader has no report or diff: its downloaded artifacts are renamed
	// into place instead, the output at the path reportPathFor recorded.
	if b.Shape == store.ShapeReader {
		return applyCatchUpArtifacts(cf)
	}
	n := view.ClosedRound
	name := b.Name
	if cf.ReportTemp != "" {
		path := rt.Store.ReportPath(name, n)
		if err := os.Rename(cf.ReportTemp, path); err != nil {
			slog.Warn("write report failed", "path", path, "err", err)
			return false
		}
	}
	if cf.Diff != nil {
		path := rt.Store.DiffPath(name, n)
		if err := tx.PutRoundFile(name, n, path, cf.Diff); err != nil {
			slog.Warn("store diff failed", "path", path, "err", err)
			return false
		}
	}
	logPath := rt.Store.BuilderLogPath(name, n)
	if cf.LogTemp != "" {
		if err := os.Rename(cf.LogTemp, logPath); err != nil {
			slog.Warn("write log failed", "path", logPath, "err", err)
			return false
		}
	}
	if cf.Log != nil {
		if err := tx.PutRoundFile(name, n, logPath, cf.Log); err != nil {
			slog.Warn("write log failed", "path", logPath, "err", err)
			return false
		}
	}
	streamPath := rt.Store.StreamPath(name, n)
	if cf.StreamTemp != "" {
		if err := os.Rename(cf.StreamTemp, streamPath); err != nil {
			slog.Warn("write stream failed", "path", streamPath, "err", err)
			return false
		}
	}
	gatePath := cf.GatePath
	if cf.GateTemp != "" {
		if err := os.Rename(cf.GateTemp, gatePath); err != nil {
			slog.Warn("write gate failed", "path", gatePath, "err", err)
			return false
		}
	}
	return true
}

// catchUpFailureBudget is how many consecutive failures of one catch-up stage
// a closed round may suffer before the binding asks for a human. Both stages
// mean the same thing -- this closed round cannot be brought home -- so they
// share one number; there is no case for another.
const catchUpFailureBudget = 10

// applyCatchUpAbsorb records the fetch's bundle outcome in b under tx. stop
// reports whether it already decided the binding's fate: a checked-out branch
// and an absorb failure both wait for the next tick, and the tenth failure
// halts. A fatal ref error is returned to the caller.
//
// An absorb failure is not counted when the binding's branch already holds the
// round's result: the store has the round, so the failure is not evidence that
// the round is missing, and counting it would halt a binding that is already
// caught up.
func applyCatchUpAbsorb(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, view remote.BindingView, cf *catchUpFetch) (next store.Binding, stop bool, err error) {
	switch {
	case cf.CheckedOut:
		return b, true, nil
	case cf.AbsorbErr != nil:
		if absorbHoldsRound(ctx, rt, b, view) {
			return clearAbsorbHalt(b), false, nil
		}
		b.RemoteAbsorbFailures++
		if b.RemoteAbsorbFailures >= catchUpFailureBudget {
			next, err = haltAndSettle(ctx, rt, tx, b, cf.AbsorbErr.Error())
			return next, true, err
		}
		return b, true, nil
	case cf.Fatal != nil:
		return b, true, cf.Fatal
	}
	return clearAbsorbHalt(b), false, nil
}

// applyCatchUpBundleFailure counts a round-bundle fetch that failed and halts
// at the shared budget. The fetch half runs without the state lock, so the
// count lands here. Unlike an absorb failure there is no verified-store gate: a
// bundle that never arrived left nothing to check the round against.
func applyCatchUpBundleFailure(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, view remote.BindingView, cf *catchUpFetch) (store.Binding, *catchUpAck, error) {
	b.RemoteBundleFailures++
	if b.RemoteBundleFailures >= catchUpFailureBudget {
		next, err := haltAndSettle(ctx, rt, tx, b, fmt.Sprintf("%s: cannot fetch round bundle %d from %s: %s", b.Name, view.ClosedRound, b.Builder.Server, cf.BundleErr))
		return next, nil, err
	}
	return b, nil, nil
}

// absorbHoldsRound reports whether the binding's local branch already sits at
// the closed round's result commit. A round-close fetch can fail on git's own
// maintenance racing it -- a repack rewrites refs while the fetch reads them,
// and the fetch then dies on a bad object -- while the objects the round needs
// are already in the store. Such a failure says nothing about whether the
// round arrived, so it must not count toward the halt.
func absorbHoldsRound(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView) bool {
	if rt.Git == nil || view.ResultCommit == "" {
		return false
	}
	branchRef := b.Branch
	if !strings.HasPrefix(branchRef, "refs/heads/") {
		branchRef = "refs/heads/" + branchRef
	}
	sha, ok, err := rt.Git.RefSHA(ctx, b.Repo, branchRef)
	if err != nil || !ok {
		return false
	}
	return sha == view.ResultCommit
}

// clearAbsorbHalt clears a catch-up halt once a later attempt succeeds. A
// round that got through after earlier failures proves those failures were
// transient, so the binding relays again without waiting for the round to
// close -- a lost ack otherwise leaves it asking for a human it does not need.
// Both counters admit a binding here: a bundle-fetch halt must clear the same
// way as an absorb one. The round itself is untouched: only the close advances
// it.
func clearAbsorbHalt(b store.Binding) store.Binding {
	if b.RemoteAbsorbFailures == 0 && b.RemoteBundleFailures == 0 {
		return b
	}
	b.RemoteAbsorbFailures = 0
	b.RemoteBundleFailures = 0
	b = clearHaltFields(b)
	b.State = store.StateActive
	return b
}

// remoteGateRecord is the closed remote round's gate record for its report
// entry: the view's result with the log this client fetched, or nil when the
// server ran no check. The record carries no Command -- the chain's repair text
// names the member's own Gate -- because the server's command is not part of
// the view. A nil record reads as chain.GateNone: the round ran no check.
//
// The log path is the one the fetch installed its bytes at, and a fetch that
// brought no log names none. A gate result the server reported without sending
// its log still reaches the entry, because the result is the fact the mirror's
// chain maps red and green from; what it does not get is a path that reads as
// a log and opens nothing.
func remoteGateRecord(a *catchUpAck) *store.GateRecord {
	if a.View.GateResult == "" {
		return nil
	}
	return &store.GateRecord{
		Result:  a.View.GateResult,
		LogPath: a.GatePath,
	}
}

// applyCatchUpReport queues the round's report entry: the client's own diff
// entry from the server's facts first, then the payload, the stop entry and
// the idle status, in the order the inline catch-up used.
func applyCatchUpReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, a *catchUpAck) (store.Binding, error) {
	n := a.View.ClosedRound
	name := b.Name

	entries, err := tx.ReadLog(name)
	if err != nil {
		return b, err
	}
	// The server already recorded a diff entry at close; writing the client's
	// own from the view's facts makes queueReport skip its own capture.
	if a.View.DiffNote != "" && !HasEntry(entries, n, store.DirToMasterMind, store.KindDiff) {
		diffPath := ""
		if a.HaveDiff {
			diffPath = rt.Store.DiffPath(name, n)
		}
		diffEntry := store.LogEntry{
			TS: rt.Now().UTC(), Round: n, Direction: store.DirToMasterMind, Kind: store.KindDiff,
			Path: diffPath, Note: a.View.DiffNote, Commits: a.View.DiffCommits, Tree: a.View.DiffTree, Confirmed: true,
		}
		if err := tx.AppendLog(name, diffEntry); err != nil {
			return b, err
		}
		entries = append(entries, diffEntry)
	}

	payload, note := catchUpPayload(b, a.View, a.HaveReport, closeClause(rt, b, a.View.ClosedRound))
	var u *usage.Usage = a.View.Usage
	if u == nil {
		// A pre-usage server ships no figure: record honestly that the server
		// sent none rather than reading a record the client does not have.
		u = remoteNoUsage(rt, b, b.RoundStartedAt, rt.Now().UTC())
	}
	next, err := queueReport(ctx, rt, tx, b, entries, reportPathFor(rt, b), payload, note, remoteGateRecord(a), u, a.View.Rusage, a.View.PriorTokens, a.View.ReportOutcome, a.View.Stopped != "", scopeVerdict{})
	if err != nil {
		return b, err
	}
	if a.View.Stopped != "" {
		// After queueReport, so the entry is filed under the stopped round.
		if err := tx.AppendLog(name, store.LogEntry{
			TS: rt.Now().UTC(), Round: n, Direction: store.DirToMasterMind,
			Kind: store.KindStop, Note: "stopped/" + a.View.Stopped, Confirmed: true,
		}); err != nil {
			return next, err
		}
	}
	// The caller (reconcileRemote or SyncRemote) decides whether to deliver.
	next.Builder.RemoteStatus = "idle"
	next.Builder.RemoteQueue = nil
	next.Builder.RemoteLive = nil
	return next, nil
}

// catchUpPayload builds the mastermind payload and note for a collected round:
// the stopped form when the server stopped it, else the finished form naming
// the artifact, both carrying the diff's summary lines. clause is the artifact
// clause closeClause resolved for the binding; the caller resolves it, so this
// stays pure.
func catchUpPayload(b store.Binding, view remote.BindingView, haveReport bool, clause string) (payload, note string) {
	n := view.ClosedRound
	server, name := b.Builder.Server, b.Name
	if view.Stopped != "" {
		payload, note = stopPayload(view.Stopped, name, n, " on "+server, haveReport, clause, b.Shape)
	} else {
		payload = fmt.Sprintf("The runner finished round %d on %s. %s", n, server, clause)
		// The server's own report note is what says the round closed without
		// its completion marker, and this note is the only place the client's
		// log records that. Without it an unmarked close reads as marked, and
		// Wait certifies a report nothing confirmed.
		note = view.ReportNote
	}
	if line := capture.DiffLineFromNote(view.DiffNote, view.DiffCommits, view.DiffTree, b.Branch); line != "" {
		payload = payload + "\n" + line
	}
	if line := capture.PathsLineFromNote(view.DiffNote); line != "" {
		payload = payload + "\n" + line
	}
	// The switches the round took on the server, one line each, after the diff
	// and before the usage -- the same place and the same rendering a local
	// close puts them. The client logs a switch only when a poll happens to
	// observe a candidate delta, so these are the round's own history: every
	// rotation and every leg of an A-B-A, which that one line can never be.
	for _, s := range view.Switches {
		if line := switchLine(s); line != "" {
			payload = payload + "\n" + line
		}
	}
	if view.DirtyCommit != "" {
		note = joinNotes(note, fmt.Sprintf("uncommitted work at refs/relevo/%s/round-%d", name, n))
	}
	return payload, note
}

// chainRoundFacts is one closed-round entry of a chain member view, completed
// from the member's binding view for the round that view itself describes: the
// server keeps the uncommitted work and the prior tokens of the round it last
// closed on the binding view and, on a server older than both fields, nowhere
// else, so a client that read the entry alone would install that round as
// clean and bill it for nothing. Every other round's entry is the whole record
// -- the server ships no older figures to complete it from -- and is returned
// untouched, which is what keeps the newest round's values off the older ones.
func chainRoundFacts(cr remote.ClosedRoundView, view remote.BindingView) remote.ClosedRoundView {
	if cr.Round != view.ClosedRound {
		return cr
	}
	if cr.DirtyCommit == "" {
		cr.DirtyCommit = view.DirtyCommit
	}
	if cr.PriorTokens == nil {
		cr.PriorTokens = view.PriorTokens
	}
	return cr
}
