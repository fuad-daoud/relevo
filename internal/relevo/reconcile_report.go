package relevo

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/pathscope"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// noteScraped marks a report entry whose body is a terminal capture, not
// the builder's own file. A scraped body is never tail-parsed.
const noteScraped = "scraped"

// joinNotes space-joins the non-empty ones, so a report entry's note can
// carry both an existing reason (e.g. "noreport") and the escape annotation
// without either overwriting the other.
func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + " " + b
	}
}

// closeOnMarker closes an open round when the builder's completion marker
// (Store.DonePath) exists. It is the one place the round decides "the builder
// says it is finished".
//
// With a gate configured it first runs the gate across ticks: gating is true
// while the gate runs and the round is left alone; closed is true when the
// round was closed this tick; the two are never both true.
//
// extraNote is the headless path's escape annotation from escapeCheck ("" for
// the pane path), appended (via joinNotes) to whichever note this close would
// otherwise write. It is computed before this call because queueReport clears
// RoundBaselineTree and the comparison needs it.
//
// Preconditions: the round is open -- a plan was sent for b.Round and no
// report has been queued for it.
// Postconditions:
//   - marker absent: closed and gating are false, b is returned unchanged, nothing written.
//   - gate running: gating is true, the binding carries the started/updated GateRun.
//   - marker and report present: the round closes normally (note extraNote,
//     plus "gate=<result>" and the gate's payload line when gated).
//   - marker present, report absent: the round closes with note
//     joinNotes("noreport", extraNote) and a payload saying so. The terminal
//     is never read: the builder said it was done, and a scrape would be a
//     worse artefact than an honest gap.
//
// Errors are gateStep's or queueReport's, wrapped; the round stays open and
// the next tick retries, since the marker is still on disk.
//
// The gate record is returned alongside the close (nil when no gate ran) so
// the caller can act on a failure after the report is queued.
func closeOnMarker(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, extraNote string) (store.Binding, bool, bool, *store.GateRecord, error) {
	if _, _, ok, _ := rt.Store.StatFile(rt.Store.DonePath(b.Name, b.Round)); !ok {
		return b, false, false, nil, nil
	}

	// A reader has no check, so the gate never runs for it: done is true with
	// no record, exactly as an ungated writer.
	var (
		done    bool
		rec     *store.GateRecord
		verdict pathscope.Verdict
	)
	if b.Shape == store.ShapeReader {
		done = true
	} else if b.GateRun == nil {
		// Scope fires before the gate: a refusal closes the round
		// without running gateStep at all -- no gate record, no KindGate
		// entry, no repair round, Regate untouched.
		verdict = judgeRoundScope(ctx, rt, b)
		if verdict.Refused {
			done = true
		} else {
			var err error
			b, done, rec, err = gateStep(ctx, rt, tx, b)
			if err != nil {
				return b, false, false, nil, fmt.Errorf("close round on marker: gate: %w", err)
			}
			if done && rec != nil {
				// The tree can move while the gate runs: judge again on the
				// tick the gate finishes.
				verdict = judgeRoundScope(ctx, rt, b)
			}
		}
	} else {
		var err error
		b, done, rec, err = gateStep(ctx, rt, tx, b)
		if err != nil {
			return b, false, false, nil, fmt.Errorf("close round on marker: gate: %w", err)
		}
		if done && rec != nil {
			verdict = judgeRoundScope(ctx, rt, b)
		}
	}
	if !done {
		return b, false, true, nil, nil
	}

	note := extraNote
	gateSuffix := ""
	if rec != nil {
		note = joinNotes(note, "gate="+rec.Result)
		var tail []string
		if rec.Result == "fail" {
			tail = tailLines(rt.Store.ReadFile, rec.LogPath, gateTailLines)
		}
		gateSuffix = "\n" + gateLine(b.Name, b.Round, *rec, tail)
	}

	reportPath, fellBack, serr := writeReaderSummary(rt, b)
	if serr != nil {
		slog.Warn("reader output not written", "binding", b.Name, "round", b.Round, "err", serr)
	}
	if fellBack {
		// The runner left no output file, so the close saved its last message:
		// recorded in the note the round's status row and `show --output` read.
		note = joinNotes(note, view.ReaderFallbackNote)
	}
	if _, _, ok, _ := rt.Store.StatFile(reportPath); ok {
		slog.Info("round closed by marker", "binding", b.Name, "round", b.Round)
		next, err := queueReport(ctx, rt, tx, b, entries, reportPath,
			fmt.Sprintf("The runner finished round %d. %s", b.Round, closeClause(rt, b, b.Round))+gateSuffix, joinNotes("", note), rec, nil, nil, nil, "", false, "", verdict)
		if err != nil {
			return b, false, false, nil, fmt.Errorf("close round on marker: %w", err)
		}
		return next, true, false, rec, nil
	}
	slog.Warn("round closed by marker without a report", "binding", b.Name, "round", b.Round, "note", "noreport")
	next, err := queueReport(ctx, rt, tx, b, entries, reportPath,
		fmt.Sprintf("Builder wrote its completion marker for round %d but wrote no %s.", b.Round, text.OutputWord(b.Shape == store.ShapeReader))+gateSuffix, joinNotes("noreport", note), rec, nil, nil, nil, "", false, "", verdict)
	if err != nil {
		return b, false, false, nil, fmt.Errorf("close round on marker: %w", err)
	}
	return next, true, false, rec, nil
}

func queueReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, path, payload, note string, gate *store.GateRecord, usage *usage.Usage, rusage *store.Rusage, prior *usage.Tokens, fallbackOutcome string, stopped bool, dirtyCommit string, verdict pathscope.Verdict) (store.Binding, error) {
	// The round this close is closing: the cap check below sizes that round's
	// artifact directory after b.Round has advanced.
	closedRound := b.Round
	// The chain this close belongs to, when it belongs to one. It is read
	// before the round advances, so the event names the round that closed;
	// the wiring advances the chain after the advance below. chainRunning is
	// the consumption decision: a running chain's member close is consumed
	// rather than delivered.
	chainRow, chainErr := tx.ChainByMember(b.Name)
	chainRunning := chainErr == nil && chainRow.Status == string(chain.StatusRunning)
	// A reader's report is its artifact directory's summary.md: it is written
	// here from the runner's final message when the runner wrote none itself,
	// and it is the path the report entry records. A writer's report is the
	// flat NNN-report.md the caller computed.
	if p, _, err := writeReaderSummary(rt, b); err != nil {
		slog.Warn("reader output not written", "binding", b.Name, "round", b.Round, "err", err)
	} else {
		path = p
	}
	// The round's throwaway worktree goes away at close, after the summary is
	// on disk: the artifact directory lives under the binding, not the scratch.
	removeReaderScratch(ctx, rt, b, b.Round)
	// The round's private temp dir goes with it, and no close route escapes
	// this funnel: every one of them queues its report through here.
	housekeep.RemoveRoundTmp(HousekeepDeps(rt), b, b.Round)

	now := rt.Now().UTC()
	roundStart := b.RoundStartedAt

	// A round that closes while a stop was requested is the stop succeeding:
	// the note says so, and a KindStop entry follows the report below.
	// This is the stored request's fact, not the caller's `stopped` parameter:
	// stop_test.go pins exactly one stopped/killed entry per stop, and the
	// parameter drives only the chain event below.
	stopRequested := !b.StopRequestedAt.IsZero()
	if stopRequested {
		note = joinNotes(note, "stopped")
	}

	body, _ := rt.Store.ReadFile(path)
	var (
		tail   reporttail.Tail
		ok     bool
		reject string
	)
	outcome := reporttail.OutcomeUnstructured
	if note != noteScraped {
		// A scraped body is a terminal capture, which holds the prompt's own
		// ```relevo skeleton, truncated by the capture. The tail contract is
		// for the file the builder writes.
		tail, ok, reject = reporttail.ParseWithReason(body)
		if !ok && reject == "" && fallbackOutcome == "" && chainErr != nil {
			tail, ok = readerStreamTail(rt, b)
		}
	}
	if ok {
		outcome = tail.Status
	} else {
		if reject != "" {
			// A fence was present but unreadable: keep unstructured, but say why
			// so the mastermind does not treat this as "the builder omitted the block".
			note = joinNotes(note, reject)
		}
		// A remote reader's output has its relevo block stripped at the
		// server's close, so parsing cannot recover the status: the view's
		// ReportOutcome is the only record of it.
		if fallbackOutcome != "" {
			outcome = fallbackOutcome
		}
	}
	sc := scanForInjection(ctx, rt, "report", b, body)
	note = joinNotes(note, sc.Note)

	// The scope verdict: a refusal forces the outcome to halted and
	// names the path, before the chain event below is built, so a chain
	// member's refusal halts its chain instead of advancing it.
	if verdict.Scoped {
		note = joinNotes(note, pathscope.Note(verdict))
		if verdict.Refused {
			outcome = reporttail.OutcomeHalted
			tail.HaltedAt = pathscope.HaltedAt(verdict)
		}
	}

	// The chain event this close produces, built from the in-memory body
	// before the reader output is stripped below (a re-read there would find
	// no block). It names the closing member and the round that closed.
	var closeWF *chainCloseWF
	if chainErr == nil {
		closeWF = &chainCloseWF{Body: body, Path: path, Outcome: outcome, Note: note, Stopped: stopped, Round: closedRound}
	}

	// A chain reader member's saved output has its block stripped before the
	// tail parse above, so that parse reads unstructured while the chain reads
	// the member's real status from its block-carrying message. The member's
	// recorded outcome is that status.
	if closeWF != nil && b.Shape == store.ShapeReader {
		if st := chainReaderCloseOutcome(rt, tx, b, closedRound, body); st != "" {
			outcome = st
		}
	}
	if closeWF != nil {
		closeWF.DirtyCommit = dirtyCommit
	}

	pLines := strings.SplitN(payload, "\n", 2)
	pFirst := pLines[0]
	pRest := ""
	if len(pLines) > 1 {
		pRest = "\n" + pLines[1]
	}

	if outcome != reporttail.OutcomeDone && outcome != reporttail.OutcomeUnstructured {
		// The prefix match accepts either wording, so a payload carrying
		// either is annotated in place; anything else gets the outcome appended.
		prefix := fmt.Sprintf("The runner finished round %d", b.Round)
		legacyPrefix := fmt.Sprintf("Builder finished round %d", b.Round)
		matched := ""
		switch {
		case strings.HasPrefix(pFirst, prefix):
			matched = prefix
		case strings.HasPrefix(pFirst, legacyPrefix):
			matched = legacyPrefix
		}
		if matched != "" {
			replacement := matched + " -- " + outcome
			if tail.HaltedAt != "" {
				replacement += fmt.Sprintf(" at %q", tail.HaltedAt)
			}
			pFirst = replacement + pFirst[len(matched):]
		} else {
			pFirst = pFirst + fmt.Sprintf(" Outcome: %s.", outcome)
		}
	}
	if sc.Flagged > 0 {
		pFirst += flaggedParenthetical(sc.Flagged, sc.Record)
	}
	payload = pFirst + pRest
	if verdict.Refused {
		payload += "\n" + pathscope.PayloadLine(text.ShowCommand(b.Name, b.Round, "diff"), verdict)
	}

	closed := ""
	if b.Shape != store.ShapeReader && !HasEntry(entries, b.Round, store.DirToMasterMind, store.KindDiff) {
		d := captureDeps(rt)
		result := capture.RoundDiff(ctx, d, tx, b)
		facts := capture.CommitFacts(ctx, d, b)
		closed = result.EndTree
		diffNote := capture.DiffSummary(result, facts)
		// The key's presence, not the list's, is what makes the counts
		// comparable: changed_paths: [] is a real list of zero paths
		// and must be checked against the diff, while a report with no key
		// at all is never compared.
		pathsMismatch := result.Available && ok && tail.ChangedPathsSet && len(tail.ChangedPaths) != result.Stat.FilesChanged
		if pathsMismatch {
			diffNote = joinNotes(diffNote, fmt.Sprintf("paths: report %d, diff %d", len(tail.ChangedPaths), result.Stat.FilesChanged))
		}
		diffEntry := store.LogEntry{
			TS:        rt.Now().UTC(),
			Round:     b.Round,
			Direction: store.DirToMasterMind,
			Kind:      store.KindDiff,
			Path:      result.Path,
			Note:      diffNote,
			Confirmed: true,
		}
		if facts.Known {
			diffEntry.Commits = facts.Commits
			diffEntry.Tree = "clean"
			if facts.Dirty {
				diffEntry.Tree = "dirty"
			}
		}
		if err := tx.AppendLog(b.Name, diffEntry); err != nil {
			return b, err
		}
		if line := capture.DiffLine(result, facts, b.Branch, b.Name, b.Round); line != "" {
			payload = payload + "\n" + line
		}
		if pathsMismatch {
			payload = payload + "\n" + capture.PathsLine(len(tail.ChangedPaths), result.Stat.FilesChanged)
		}
	}

	// The switches this round took, one line each, after the diff and before
	// the usage the entry carries. A switch is status, not a delivery: nothing
	// is queued for one, so this line is the only trace of it the mastermind
	// gets, and it rides the report that would have been delivered anyway.
	// A round that never switched appends nothing, so its payload is unchanged
	// by this.
	for _, line := range switchLines(entries, closedRound, payloadNamesSwitches(payload)) {
		payload = payload + "\n" + line
	}

	// The closed round's usage: what the caller measured (a remote
	// binding's server measured its own round and shipped it), or a local
	// read when the caller sent none. entryUsage's type is inferred from
	// the parameter: the parameter's name shadows the usage package in
	// this body, so the type cannot be written out here.
	var entryUsage = usage
	if entryUsage != nil {
		copied := *entryUsage
		entryUsage = &copied
	} else {
		src := roundSource(rt, b, roundStart, now)
		entryUsage = recordUsage(ctx, rt, src)
		recordAllowance(rt, src)
	}

	// The closed round's cgroup measurement: what the caller already has
	// (a remote binding's server measured its own round and shipped it as
	// view.Rusage), or a local read when the caller sent none -- headless
	// only, since a pane round has no supervisor.
	entryRusage := rusage
	if entryRusage == nil && rt.Runner != nil && b.Builder.Headless() {
		if r, ok := rt.Runner.Rusage(ctx, handleOf(b.Builder), rt.Store.StreamPath(b.Name, b.Round)); ok {
			entryRusage = &store.Rusage{CPUMS: r.CPUMS, PeakMemBytes: r.PeakMemBytes}
		}
	}

	var reportPrior = prior
	if reportPrior != nil {
		p := *reportPrior
		reportPrior = &p
	}

	entry := store.LogEntry{
		TS: now, Round: b.Round,
		Direction: store.DirToMasterMind, Kind: store.KindReport,
		Path: path, Payload: payload, Note: note,
		Usage:        entryUsage,
		PriorTokens:  reportPrior,
		Rusage:       entryRusage,
		Outcome:      outcome,
		HaltedAt:     tail.HaltedAt,
		ChangedPaths: tail.ChangedPaths,
		CommandsRun:  tail.CommandsRun,
		NotDone:      tail.NotDone,
		Flagged:      sc.Flagged,
		FlaggedBy:    sc.FlaggedBy,
		Classify:     sc.Record,
		Gate:         gate,
	}
	entry.BuilderSession = builderSessionOf(b)
	if chainRunning {
		// A chain member's close is consumed by the chain: the mastermind gets
		// the chain's own end delivery when the chain ends, so the member's
		// report is recorded confirmed with the chain's name rather than
		// queued. delivery.Queue would force Confirmed=false.
		entry.Confirmed = true
		entry.Note = joinNotes(entry.Note, "consumed by chain "+chainRow.Name)
		if err := tx.AppendLog(b.Name, entry); err != nil {
			return b, err
		}
	} else if err := delivery.Queue(ctx, deliveryDeps(rt), tx, b.Name, entry); err != nil {
		return b, err
	}

	// Strip the relevo block from a reader's output file. The parse above is
	// the only reader of the block, and the file the mastermind reads must not
	// carry it. Stripping here, after the entry is queued, means the outcome
	// survives a close that fails later and is retried on the next tick.
	if b.Shape == store.ShapeReader {
		if stripped := reporttail.StripTail(body); !bytes.Equal(stripped, body) {
			if root, rel, err := readerOutputRoot(rt.Store, b, path); err != nil {
				slog.Warn("reader output not stripped", "binding", b.Name, "round", b.Round, "err", err)
			} else {
				err := replaceReaderOutput(root, rel, stripped)
				_ = root.Close()
				if err != nil {
					slog.Warn("reader output not stripped", "binding", b.Name, "round", b.Round, "err", err)
				}
			}
		}
	}

	if stopRequested {
		// Filed under the round that was stopped: b.Round advances below.
		if err := tx.AppendLog(b.Name, store.LogEntry{
			TS: rt.Now().UTC(), Round: b.Round, Direction: store.DirToMasterMind,
			Kind: store.KindStop, Note: "stopped/graceful", Confirmed: true,
		}); err != nil {
			return b, err
		}
	}

	b.Round++
	b.State = store.StateActive

	// The new round has not been sent yet, so it has no deadline: leaving the
	// old round's start in place would time the next round out against a clock
	// that started before the mastermind had even seen this report. Send stamps a
	// fresh RoundStartedAt when it hands the round over.
	b.RoundStartedAt = time.Time{}

	// A halt notified for the old round says nothing about the new one, so the
	// next round that goes wrong gets its own single notification. The clear
	// takes the kind with the rest: a kind left from an unreachable episode
	// would answer for a halt the new round never had.
	b = ClearHaltFields(b)
	// A switch counted against the old round says nothing about the new one,
	// and neither does an exclusion recorded against it.
	b.RoundSwitches = 0
	b.RoundExcluded = nil
	b.RoundOOMKills = 0
	// The round's verify flag has been acted on by the close: the
	// consult, when there is one, is already running.
	b.RoundVerify = false
	b.RoundTier = ""
	b.RoundCPU = nil
	b.RoundBaselineTree = ""
	b.RoundClosedBaselineHead = b.RoundBaselineHead
	b.RoundBaselineHead = ""
	b.RoundRepoBaselineTree = ""
	// A remote member's closed tree is the pulled result commit the catch-up
	// recorded -- there is no local worktree to snapshot -- so it keeps that
	// value across the advance. Every non-remote binding takes the assignment
	// below.
	if !b.Builder.Remote() {
		b.RoundClosedTree = closed
	}
	// The stream id was the closed round's; the next round's process
	// announces its own (a pane builder has none).
	b.Builder.StreamSessionID = ""
	b.GateRun = nil
	// A passing gate clears the repair bookkeeping: the next
	// failing gate gets a fresh budget and a fresh stall comparison, whatever
	// the previous repair rounds cost.
	if gate != nil && gate.Result == "pass" {
		b.RepairCount = 0
		b.LastGateSig = ""
	}
	// A closed round is over: a stall stamped against it says nothing about
	// the next one, and neither do the progress sample or the stale
	// clock.
	b.StalledSince = time.Time{}
	// A request to stop belonged to the round that just closed: the
	// stop bookkeeping never outlives it.
	b.StopRequestedAt = time.Time{}
	b.StopGraceMS = 0
	b.Progress = nil
	b.ExploringSince = time.Time{}
	b.StaleSince = time.Time{}

	// A reader round whose artifact directory is over
	// policy.artifact_max_mb closes as usual -- the summary is written and
	// the report is queued -- but the binding asks for a human, and nothing
	// is deleted: sealRounds holds the round back until the cap is raised.
	//
	// The entry is filed under closedRound, not the round the binding has
	// already advanced to: the cause is this round's artifact size, and a
	// default wait still sitting on this round would never pull an N+1 entry
	// (closedRoundHalt).
	//
	// A halt whose entry cannot be written is owed to a later tick, not
	// returned: the report entry above is already on disk whatever this
	// returns, so returning the error would leave the tick unsaved with the
	// binding still at N -- and the next tick would find the round closed,
	// take the idle branch, never halt again, and refuse every send with
	// ErrReportPending.
	if b.Shape == store.ShapeReader {
		if over, total := view.ArtifactCapExceeded(rt.Store, b, closedRound, rt.Policy.ArtifactMaxBytes()); over {
			b = closeHaltOrOwe(ctx, rt, tx, b, closedRound, view.ArtifactCapReason(total, rt.Policy.ArtifactMaxBytes()))
		}
	}

	// A scope refusal asks for a human, exactly where the reader artifact
	// cap does: after the round has advanced, naming the offending file.
	// Filed under closedRound, and its failed queue owed, for the
	// reasons the cap halt above gives.
	if verdict.Refused {
		b = closeHaltOrOwe(ctx, rt, tx, b, closedRound, pathscope.HaltText(b.Name, b.WriterRole(), closedRound, verdict))
	}

	// The round has advanced, so the chain may now move: the close is mapped
	// to an event, the pure transition decides, and the action (seed the next
	// member, halt, stop, finish) runs on the chain's own sender in this same
	// critical section. A close that did not advance the chain writes no
	// trace row.
	if closeWF != nil {
		// chainApply returns the binding its action wrote: a remote member's
		// staged repair carries the chain's own bookkeeping (RepairCount,
		// LastGateSig, the staged round's baseline head), and the caller saves
		// what it returns.
		next, cerr := chainApply(ctx, rt, tx, b, closeWF)
		b = next
		if cerr != nil {
			return b, cerr
		}
	}

	return b, nil
}
