package relevo

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/pathscope"
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

	b, done, rec, verdict, err := runMarkerGateVerdict(ctx, rt, tx, b)
	if err != nil {
		return b, false, false, nil, err
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
		return closeMarkedWithReport(ctx, rt, tx, b, entries, reportPath, note, gateSuffix, rec, verdict)
	}
	return closeMarkedWithoutReport(ctx, rt, tx, b, entries, reportPath, note, gateSuffix, rec, verdict)
}

// runMarkerGateVerdict runs the marker gate once and judges the round scope.
// A reader has no check, so the gate never runs for it: done is true with no
// record, exactly as an ungated writer. Scope fires before the gate: a refusal
// closes the round without running gateStep at all -- no gate record, no
// KindGate entry, no repair round, Regate untouched. The gate runs at most
// once per close.
func runMarkerGateVerdict(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, bool, *store.GateRecord, pathscope.Verdict, error) {
	if b.Shape == store.ShapeReader {
		return b, true, nil, pathscope.Verdict{}, nil
	}
	var verdict pathscope.Verdict
	if b.GateRun == nil {
		verdict = judgeRoundScope(ctx, rt, b)
		if verdict.Refused {
			return b, true, nil, verdict, nil
		}
	}
	b, done, rec, err := gateStep(ctx, rt, tx, b)
	if err != nil {
		return b, false, nil, pathscope.Verdict{}, fmt.Errorf("close round on marker: gate: %w", err)
	}
	if done && rec != nil {
		// The tree can move while the gate runs: judge again on the
		// tick the gate finishes.
		verdict = judgeRoundScope(ctx, rt, b)
	}
	return b, done, rec, verdict, nil
}

// closeMarkedWithReport queues the marked close when the runner left its
// report: the normal round end.
func closeMarkedWithReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, reportPath, note, gateSuffix string, rec *store.GateRecord, verdict pathscope.Verdict) (store.Binding, bool, bool, *store.GateRecord, error) {
	slog.Info("round closed by marker", "binding", b.Name, "round", b.Round)
	next, err := queueReport(ctx, rt, tx, b, entries, reportPath,
		fmt.Sprintf("The runner finished round %d. %s", b.Round, closeClause(rt, b, b.Round))+gateSuffix, joinNotes("", note), rec, nil, nil, nil, "", false, "", verdict)
	if err != nil {
		return b, false, false, nil, fmt.Errorf("close round on marker: %w", err)
	}
	return next, true, false, rec, nil
}

// closeMarkedWithoutReport queues the marked close when the runner left no
// report: the round closes with a noreport note saying so. The terminal is
// never read: the builder said it was done, and a scrape would be a worse
// artefact than an honest gap.
func closeMarkedWithoutReport(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, reportPath, note, gateSuffix string, rec *store.GateRecord, verdict pathscope.Verdict) (store.Binding, bool, bool, *store.GateRecord, error) {
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
	st := &queueReportState{
		closedRound: b.Round,
		path:        path,
		note:        note,
		payload:     payload,
	}
	// The chain this close belongs to, when it belongs to one. It is read
	// before the round advances, so the event names the round that closed;
	// chainRunning is the consumption decision: a running chain's member close
	// is consumed rather than delivered.
	chainRow, chainErr := tx.ChainByMember(b.Name)
	st.chainRunning = chainErr == nil && chainRow.Status == string(chain.StatusRunning)
	if chainErr == nil {
		st.chainName = chainRow.Name
	}
	parseReportOutcome(ctx, rt, tx, b, fallbackOutcome, stopped, dirtyCommit, verdict, chainErr, st)
	if err := appendDiffAndSwitchLines(ctx, rt, tx, b, entries, verdict, st); err != nil {
		return b, err
	}
	if err := queueReportEntry(ctx, rt, tx, b, gate, usage, rusage, prior, st); err != nil {
		return b, err
	}
	return advanceToNextRound(ctx, rt, tx, b, gate, verdict, st)
}
