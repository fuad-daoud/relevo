package relevo

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// nudgeNote marks the one reminder relevo sends when a builder went idle without
// writing its report file. Old logs carry nudge entries, so wait.go and
// waiting.go still filter on it.
const nudgeNote = "nudge"

func emitMutations(ctx context.Context, rt Runtime, orig, next store.Binding) {
	if rt.Hooks == nil {
		return
	}
	if next.Round > orig.Round {
		rt.Hooks.Dispatch(ctx, hooks.Event{
			Type:      hooks.EventRoundStarted,
			BindingID: next.Name,
			State:     string(next.State),
			OldState:  string(orig.State),
			Round:     next.Round,
			Timestamp: rt.Now().UTC(),
		})
	}
	if next.State != orig.State {
		rt.Hooks.Dispatch(ctx, hooks.Event{
			Type:      hooks.EventStateChanged,
			BindingID: next.Name,
			State:     string(next.State),
			OldState:  string(orig.State),
			Round:     next.Round,
			Timestamp: rt.Now().UTC(),
		})
	}
	// One builder_stalled per episode, on the zero -> set edge (#252). The
	// clear emits nothing: the hook payload has no new fields, and a resumed
	// builder is unremarkable.
	if orig.StalledSince.IsZero() && !next.StalledSince.IsZero() {
		rt.Hooks.Dispatch(ctx, hooks.Event{
			Type:      hooks.EventBuilderStalled,
			BindingID: next.Name,
			State:     string(next.State),
			OldState:  string(orig.State),
			Round:     next.Round,
			Timestamp: rt.Now().UTC(),
		})
	}
	// One binding_stale per episode, on the zero -> set edge (#135). As with
	// builder_stalled, the clear emits nothing.
	if orig.StaleSince.IsZero() && !next.StaleSince.IsZero() {
		rt.Hooks.Dispatch(ctx, hooks.Event{
			Type:      hooks.EventBindingStale,
			BindingID: next.Name,
			State:     string(next.State),
			OldState:  string(orig.State),
			Round:     next.Round,
			Timestamp: rt.Now().UTC(),
		})
	}
}

// stampStale maintains the stale clock (#135) for a NEEDS YOU or HELD binding:
// it stamps StaleSince on the moment the binding started waiting -- the halt
// time when there is one, otherwise the newest log entry -- once that moment is
// stale_after_ms old. Every other state carries no stale stamp. It never
// clears StaleNotifiedAt; Send, resume/rebind and round close do that, which is
// what makes a notification one per episode rather than one per tick.
func stampStale(rt Runtime, tx *store.Tx, b store.Binding) store.Binding {
	switch b.State {
	case store.StateNeedsYou:
	default:
		b.StaleSince = time.Time{}
		return b
	}
	if !b.StaleSince.IsZero() {
		return b
	}

	since := b.HaltAt
	if since.IsZero() {
		entries, err := tx.ReadLog(b.Name)
		if err != nil || len(entries) == 0 {
			return b
		}
		since = entries[len(entries)-1].TS
	}
	if rt.Now().UTC().Sub(since) >= rt.Policy.StaleAfter() {
		b.StaleSince = since
	}
	return b
}

// stateWarned is the daemon's warn-once memory, keyed by binding name plus the
// reason (#372). It is a log dedupe and nothing more: the skipped binding is
// still skipped on every tick. Keying on the reason as well as the name lets
// one binding carry both a newer-format and an unknown-state warning.
var stateWarned sync.Map

// warnOnce logs msg at Warn the first time binding+reason is seen in this
// process. The variadic args are structured attributes, as every other slog
// call in the package uses.
func warnOnce(binding, reason, msg string, args ...any) {
	if _, seen := stateWarned.LoadOrStore(binding+"\x00"+reason, struct{}{}); seen {
		return
	}
	slog.Warn(msg, args...)
}

// Reconcile advances one binding: its consults, then its builder's round
// (headless process or remote poll), and finally any pending planner payload.
//
// Reconcile does NOT persist anything: it returns the binding and the caller
// must `tx.Save` it before releasing the lock. Everything it calls takes the
// same tx rather than locking itself, which is what lets the caller hold one
// critical section across the whole read-reconcile-write.
func Reconcile(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (out store.Binding, err error) {
	return reconcileWith(ctx, rt, tx, b, nil)
}

// reconcileWith is Reconcile with an optional prefetched remote view: a nil
// pre makes a remote binding fetch inline, as Reconcile always did.
func reconcileWith(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, pre *remoteFetch) (out store.Binding, err error) {
	// An unknown State is one a newer relevo wrote (#372 §4.1). Reconciling it
	// as live would drive a builder the newer relevo is already driving, so
	// the binding is returned exactly as it is and nothing at all runs: no
	// consults, no builder handling, and no save by the caller. The warning
	// repeats only once per process.
	//
	// It comes before every other step -- including the consult pass below --
	// because "left alone" must mean byte-for-byte unchanged.
	if !store.KnownState(b.State) {
		warnOnce(b.Name, "unknown-state",
			fmt.Sprintf("binding %s has unknown state %q; leaving it to a newer relevo", b.Name, string(b.State)),
			"binding", b.Name, "state", string(b.State))
		return b, nil
	}

	orig := b
	defer func() {
		if err == nil {
			// The stale clock (#135) is stamped on whatever state the tick
			// settled on, before the mutations and notices read it.
			out = stampStale(rt, tx, out)
			emitMutations(ctx, rt, orig, out)
		}
	}()
	// Consults reconcile before the builder is located, and before the DONE
	// gate below, because they are orthogonal to both: a reviewer reading a
	// diff has no stake in whether the builder's pane still exists, nor in
	// whether the planner has already called the work done. Reconcile returns
	// early when the binding is done, when the builder is gone (below), and on
	// the round-cap halt, and none of those should stop a consult finishing.
	//
	// The DONE case is the one that bites: a consult never advanced past
	// ConsultRunning would otherwise never be observed by a tick again, and gc
	// then removes the binding and its consults with it.
	//
	// On those early-return paths deliverAndSettle is skipped, so queued
	// findings wait on disk and the background wait's delivery retrieves them
	// -- the same behaviour the halt comment below describes for a halted
	// binding.
	b, err = consult.Reconcile(ctx, consultDeps(rt), tx, b)
	if err != nil {
		return b, err
	}

	if b.State == store.StateDone || b.State == store.StatePaused {
		return b, nil
	}

	if b.Builder.Remote() {
		return reconcileRemote(ctx, rt, tx, b, pre)
	}

	// Every local binding reaching this line is headless: a local builder is
	// only ever a process relevo runs per round (#99, #303). Spec §5.1 is its
	// own tick.
	return reconcileHeadless(ctx, rt, tx, b)
}

// haltBinding stops relaying and asks for a human, exactly once per round.
//
// The dedup keys on HaltNotifiedRound rather than on State because State is
// rewritten by other steps of the same tick, which is what made every earlier
// State-keyed guard log once per poll instead of once. Per round is also the
// behaviour a human wants: one log line per round that goes wrong.
func haltBinding(_ context.Context, rt Runtime, b store.Binding, message string) (store.Binding, error) {
	text := strings.TrimPrefix(message, b.Name+": ")
	if b.Halt != text || b.HaltAt.IsZero() {
		b.HaltAt = rt.Now().UTC()
	}
	b.Halt = text

	if b.HaltNotifiedRound != b.Round {
		slog.Info("binding halted", "binding", b.Name, "round", b.Round, "reason", message)

		b.HaltNotifiedRound = b.Round
	}

	b.State = store.StateNeedsYou

	return b, nil
}

// checkRoundTimeout flags a builder that has been working past its budget. It
// never kills anything -- the human decides whether to nudge, reset or switch.
//
// The bool reports whether it halted, so the caller can skip delivery the way
// the round cap does. It is returned rather than inferred from the state,
// because a binding can arrive here already NeedsYou for an unrelated reason
// and must still have its pending payload delivered.
//
// The halting tick scans for a rate limit first (spec §5): a match switches
// the builder instead of halting, and the switch does not count toward
// max_switches.
func checkRoundTimeout(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, bool, error) {
	if b.RoundStartedAt.IsZero() || b.RoundTimeoutMS <= 0 {
		return b, false, nil
	}

	budget := time.Duration(b.RoundTimeoutMS) * time.Millisecond
	if rt.Now().UTC().Sub(b.RoundStartedAt) < budget {
		return b, false, nil
	}

	if b.HaltNotifiedRound != b.Round {
		text := limitText(ctx, rt, b)
		next, _, handled, err := gateOnLimit(ctx, rt, tx, b, text, true)
		if handled {
			return next, true, err
		}
	}

	next, err := haltBinding(ctx, rt, b,
		fmt.Sprintf("%s: round %d has run past %s", b.Name, b.Round, budget))

	return next, true, err
}

// noteScraped marks a report entry whose body is a terminal capture, not
// the builder's own file. A scraped body is never tail-parsed (#221).
const noteScraped = "scraped"

// joinNotes space-joins the non-empty ones, so a report entry's note can
// carry both an existing reason (e.g. "noreport") and the escape annotation
// (#192) without either overwriting the other.
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
// With a gate configured (#132) it first runs the gate across ticks: gating
// is true while the gate is running and the round must be left alone --
// both callers return early without acting on the builder in any way (no
// nudge, no "exited without a report" switch, no timeout halt). closed is
// true when the round was closed this tick. closed and gating are never
// both true.
//
// extraNote is appended (via joinNotes) to whichever note this close would
// otherwise write -- "" for the pane path, and the escape annotation (#192)
// computed by the headless path from escapeCheck before this call, since
// queueReport clears RoundBaselineTree and the comparison must happen while
// it is still on the binding.
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
// the caller can act on a failure after the report is queued (#132 part 2).
func closeOnMarker(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, extraNote string) (store.Binding, bool, bool, *store.GateRecord, error) {
	if _, err := os.Stat(rt.Store.DonePath(b.Name, b.Round)); err != nil {
		return b, false, false, nil, nil
	}

	// A reader has no check, so the gate never runs for it: done is true with
	// no record, exactly as an ungated writer.
	var (
		done bool
		rec  *store.GateRecord
	)
	if b.Shape == store.ShapeReader {
		done = true
	} else {
		var err error
		b, done, rec, err = gateStep(ctx, rt, tx, b)
		if err != nil {
			return b, false, false, nil, fmt.Errorf("close round on marker: gate: %w", err)
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

	reportPath, serr := writeReaderSummary(rt, b)
	if serr != nil {
		slog.Warn("reader summary not written", "binding", b.Name, "round", b.Round, "err", serr)
	}
	if _, err := os.Stat(reportPath); err == nil {
		slog.Info("round closed by marker", "binding", b.Name, "round", b.Round)
		next, err := queueReport(ctx, rt, tx, b, entries, reportPath,
			fmt.Sprintf("The runner finished round %d. %s", b.Round, closeClause(rt, b, b.Round))+gateSuffix, joinNotes("", note), rec, nil, nil, nil)
		if err != nil {
			return b, false, false, nil, fmt.Errorf("close round on marker: %w", err)
		}
		return next, true, false, rec, nil
	}
	slog.Warn("round closed by marker without a report", "binding", b.Name, "round", b.Round, "note", "noreport")
	next, err := queueReport(ctx, rt, tx, b, entries, reportPath,
		fmt.Sprintf("Builder wrote its completion marker for round %d but wrote no report.", b.Round)+gateSuffix, joinNotes("noreport", note), rec, nil, nil, nil)
	if err != nil {
		return b, false, false, nil, fmt.Errorf("close round on marker: %w", err)
	}
	return next, true, false, rec, nil
}

func queueReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, path, payload, note string, gate *store.GateRecord, usage *usage.Usage, rusage *store.Rusage, prior *usage.Tokens) (store.Binding, error) {
	// The round this close is closing: the cap check below sizes that round's
	// artifact directory after b.Round has advanced.
	closedRound := b.Round
	// A reader's report is its artifact directory's summary.md: it is written
	// here from the runner's final message when the runner wrote none itself,
	// and it is the path the report entry records. A writer's report is the
	// flat NNN-report.md the caller computed.
	if p, err := writeReaderSummary(rt, b); err != nil {
		slog.Warn("reader summary not written", "binding", b.Name, "round", b.Round, "err", err)
	} else {
		path = p
	}
	// The round's throwaway worktree goes away at close, after the summary is
	// on disk: the artifact directory lives under the binding, not the scratch.
	removeReaderScratch(ctx, rt, b, b.Round)

	now := rt.Now().UTC()
	roundStart := b.RoundStartedAt

	// A round that closes while a stop was requested is the stop succeeding
	// (#138): the note says so, and a KindStop entry follows the report below.
	stopped := !b.StopRequestedAt.IsZero()
	if stopped {
		note = joinNotes(note, "stopped")
	}

	body, _ := os.ReadFile(path)
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
	}
	if ok {
		outcome = tail.Status
	} else if reject != "" {
		// A fence was present but unreadable: keep unstructured, but say why
		// so the planner does not treat this as "the builder omitted the block".
		note = joinNotes(note, reject)
	}
	sc := scanForInjection(ctx, rt, "report", b, body)
	note = joinNotes(note, sc.Note)

	pLines := strings.SplitN(payload, "\n", 2)
	pFirst := pLines[0]
	pRest := ""
	if len(pLines) > 1 {
		pRest = "\n" + pLines[1]
	}

	if outcome != reporttail.OutcomeDone && outcome != reporttail.OutcomeUnstructured {
		// The prefix match accepts the current words and the pre-rename ones,
		// so a payload carrying either is annotated in place; anything else
		// gets the outcome appended.
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

	closed := ""
	if b.Shape != store.ShapeReader && !HasEntry(entries, b.Round, store.DirToPlanner, store.KindDiff) {
		d := captureDeps(rt)
		result := capture.RoundDiff(ctx, d, tx, b)
		facts := capture.CommitFacts(ctx, d, b)
		closed = result.EndTree
		diffNote := capture.DiffSummary(result, facts)
		// The key's presence, not the list's, is what makes the counts
		// comparable (#216): changed_paths: [] is a real list of zero paths
		// and must be checked against the diff, while a report with no key
		// at all is never compared.
		pathsMismatch := result.Available && ok && tail.ChangedPathsSet && len(tail.ChangedPaths) != result.Stat.FilesChanged
		if pathsMismatch {
			diffNote = joinNotes(diffNote, fmt.Sprintf("paths: report %d, diff %d", len(tail.ChangedPaths), result.Stat.FilesChanged))
		}
		diffEntry := store.LogEntry{
			TS:        rt.Now().UTC(),
			Round:     b.Round,
			Direction: store.DirToPlanner,
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
		entryUsage = recordUsage(ctx, rt, roundSource(rt, b, roundStart, now))
	}

	// The closed round's cgroup measurement: what the caller already has
	// (a remote binding's server measured its own round and shipped it as
	// view.Rusage), or a local read when the caller sent none -- headless
	// only, since a pane round has no supervisor (#244, #216).
	entryRusage := rusage
	if entryRusage == nil && rt.Runner != nil && b.Builder.Headless() {
		if r, ok := rt.Runner.Rusage(ctx, handleOf(b.Builder), rt.Store.BuilderStreamPath(b.Name, b.Round)); ok {
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
		Direction: store.DirToPlanner, Kind: store.KindReport,
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
	if err := delivery.Queue(ctx, deliveryDeps(rt), tx, b.Name, entry); err != nil {
		return b, err
	}

	// Strip the relevo block from a reader's summary.md. The parse above is
	// the only reader of the block, and the file the planner reads must not
	// carry it. Stripping here, after the entry is queued, means the outcome
	// survives a close that fails later and is retried on the next tick.
	if b.Shape == store.ShapeReader {
		if stripped := reporttail.StripTail(body); !bytes.Equal(stripped, body) {
			if err := os.WriteFile(path, stripped, 0o644); err != nil {
				slog.Warn("reader summary not stripped", "binding", b.Name, "round", b.Round, "err", err)
			}
		}
	}

	if stopped {
		// Filed under the round that was stopped: b.Round advances below.
		if err := tx.AppendLog(b.Name, store.LogEntry{
			TS: rt.Now().UTC(), Round: b.Round, Direction: store.DirToPlanner,
			Kind: store.KindStop, Note: "stopped/graceful", Confirmed: true,
		}); err != nil {
			return b, err
		}
	}

	b.Round++
	b.State = store.StateActive

	// The new round has not been sent yet, so it has no deadline: leaving the
	// old round's start in place would time the next round out against a clock
	// that started before the planner had even seen this report. Send stamps a
	// fresh RoundStartedAt when it hands the round over.
	b.RoundStartedAt = time.Time{}

	// A halt notified for the old round says nothing about the new one, so the
	// next round that goes wrong gets its own single notification.
	b.HaltNotifiedRound = 0
	b.Halt = ""
	b.HaltAt = time.Time{}
	// A switch counted against the old round says nothing about the new one,
	// and neither does an exclusion recorded against it (#191).
	b.RoundSwitches = 0
	b.RoundExcluded = nil
	b.RoundOOMKills = 0
	// The round's verify flag has been acted on by the close (#144): the
	// consult, when there is one, is already running.
	b.RoundVerify = false
	b.RoundTier = ""
	b.RoundCPU = nil
	b.RoundBaselineTree = ""
	b.RoundBaselineHead = ""
	b.RoundClosedTree = closed
	// The stream id was the closed round's; the next round's process
	// announces its own (a pane builder has none) (#147).
	b.Builder.StreamSessionID = ""
	b.GateRun = nil
	// A passing gate clears the repair bookkeeping (#132 part 2): the next
	// failing gate gets a fresh budget and a fresh stall comparison, whatever
	// the previous repair rounds cost.
	if gate != nil && gate.Result == "pass" {
		b.RepairCount = 0
		b.LastGateSig = ""
	}
	// A closed round is over: a stall stamped against it says nothing about
	// the next one (#252), and neither do the progress sample or the stale
	// clock (#135).
	b.StalledSince = time.Time{}
	// A request to stop belonged to the round that just closed (#138): the
	// stop bookkeeping never outlives it.
	b.StopRequestedAt = time.Time{}
	b.StopGraceMS = 0
	b.Progress = nil
	b.ExploringSince = time.Time{}
	b.StaleSince = time.Time{}
	b.StaleNotifiedAt = time.Time{}

	// §3.4: a reader round whose artifact directory is over
	// policy.artifact_max_mb closes as usual -- the summary is written and
	// the report is queued -- but the binding asks for a human, and nothing
	// is deleted: sealRounds holds the round back until the cap is raised.
	if b.Shape == store.ShapeReader {
		if over, total := artifactCapExceeded(rt.Store, b, closedRound, rt.Policy.ArtifactMaxBytes()); over {
			b, _ = haltBinding(ctx, rt, b, artifactCapReason(total, rt.Policy.ArtifactMaxBytes()))
		}
	}

	return b, nil
}

// deliverAndSettle attempts any pending delivery. The binding is left pending
// when no route can take the payload -- DeliverPending records why, and
// `relevo wait` or the channel's own poll delivers it later (#303 §5.4).
func deliverAndSettle(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, error) {
	if b.Owner != "" {
		// Owned by a remote client: there is no planner. Payloads stay
		// queued; the owner reads them over the wire (remote-builders spec §6.2).
		return b, nil
	}
	next, got, err := delivery.DeliverPending(ctx, deliveryDeps(rt), tx, b)
	if err != nil {
		return b, err
	}
	b = next

	if got.Delivered && got.Reason != "" {
		slog.Info("payload delivered", "binding", b.Name, "round", got.Round, "route", got.Route, "reason", got.Reason)
	}

	return b, nil
}

// HasEntry reports whether the log already contains a message of that shape.
func HasEntry(entries []store.LogEntry, round int, dir store.Direction, kind store.Kind) bool {
	for _, e := range entries {
		if e.Round == round && e.Direction == dir && e.Kind == kind && e.Note != nudgeNote {
			return true
		}
	}
	return false
}
