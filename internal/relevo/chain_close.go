package relevo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainCloseWF is the close of a workflow chain's member round: the facts the
// workflow engine needs to build the step_closed its state awaits.
type chainCloseWF struct {
	Body    []byte
	Path    string
	Outcome string
	Stopped bool
	// Note is the close's own note from the runner, folded into a non-done
	// builder's one-line reason when its report names no source.
	Note string
	// Round is the round that closed. The close's caller advances the binding
	// before the chain moves, so the closing binding's own round is no longer
	// the closed one by then; the event must name the round the step awaited.
	Round int
}

// chainEventFromCloseWF maps one member close onto the workflow event the
// engine reads. The step and member come from the chain state's Awaiting; the
// closing binding's actor comes from its chain_member row. A stopped close is
// the engine's stopped event. Otherwise it parses the actor's declared
// outcomes, keys its artifacts and closes the step halted when a declared
// artifact is missing.
func chainEventFromCloseWF(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, b store.Binding, wf chainCloseWF) (workflow.Event, error) {
	st, err := chainWorkflowState(c)
	if err != nil {
		return workflow.Event{}, err
	}
	actor := chainFlowActor(tx, c, b.Name)
	if actor == "" {
		actor = BindingRole(b)
	}
	round := wf.Round
	if round == 0 {
		round = b.Round
	}
	ev := workflow.Event{Step: st.Awaiting.Step, Member: actor, Round: round}
	if wf.Stopped {
		ev.Kind = workflow.EventStopped
		return ev, nil
	}
	ev.Kind = workflow.EventStepClosed

	info, _ := rt.RoleRegistry().ActorInfo(actor)
	outs := info.Outputs
	bodies := chainOutcomeBodies(rt, b, round, wf.Body)
	values, reason := workflow.ParseOutcomes(outs, bodies...)
	if reason != "" {
		ev.Status = reporttail.OutcomeHalted
		ev.Reason = reason
		return ev, nil
	}
	ev.Outcomes = values
	// A writer's report status is the round's own outcome. A reader's output is
	// a summary whose block the close already stripped, so its saved outcome
	// says nothing about the step: the reader routes on the status its own
	// block carries -- done when it carries outcomes without one.
	if b.Shape == store.ShapeReader {
		ev.Status = chainReaderStatus(bodies, outs)
	} else {
		ev.Status = wf.Outcome
		// A builder close that is not done carries why in one line from its
		// report tail, under the plan the chain halted on; the caller-rendered
		// wording is what the engine's unmatched-run halt uses verbatim.
		if wf.Outcome != reporttail.OutcomeDone {
			tail, _, _ := reporttail.ParseWithReason(wf.Body)
			ev.Reason = chain.BuilderHaltReason(tail, wf.Note, wf.Outcome)
			ev.HaltReason = fmt.Sprintf("builder halted on plan %d: %s", c.Plan, ev.Reason)
			return ev, nil
		}
		// A done close over a tree that still holds uncommitted work carries no
		// round forward: the diff it seals is not the work the builder left, so
		// advancing would hand the next member a step that looks complete and is
		// not. The gate names where the work actually is.
		if reason := chainDirtyCloseReason(ctx, rt, c, b, round); reason != "" {
			ev.Status = reporttail.OutcomeHalted
			ev.Reason = reason
			ev.HaltReason = reason
			return ev, nil
		}
	}

	artifacts := chainCloseArtifacts(rt, b, round, wf.Path, outs)
	if miss := workflow.MissingArtifact(outs, chainArtifactSizes(rt, artifacts)); miss != "" {
		ev.Status = reporttail.OutcomeHalted
		ev.Reason = miss
		return ev, nil
	}
	ev.Artifacts = artifacts
	return ev, nil
}

// chainDirtyCloseReason is the dirty gate's answer for one writer close: the
// halt wording when the round must not advance, and "" when it may. A clean
// tree, a member the gate does not cover, and a clean read all give "".
//
// A git error is not a clean tree. The gate decides in band and never fails the
// close it reads, exactly as escapeCheck does: the wording says the tree could
// not be read, so the trace never records an unread tree as a finished round.
func chainDirtyCloseReason(ctx context.Context, rt Runtime, c db.ChainRow, b store.Binding, round int) string {
	if !chainDirtyGateApplies(rt, b) {
		return ""
	}
	dirty, err := rt.Git.Dirty(ctx, b.CWD)
	if err != nil {
		slog.Warn("chain dirty gate skipped", "binding", b.Name, "round", round, "err", err)
		return chainDirtyUnreadReason(c, b, round, err)
	}
	if !dirty {
		return ""
	}
	return chainDirtyReason(rt, c, b, round, chainDirtySummary(ctx, rt, b))
}

// chainDirtyGateApplies reports whether the dirty gate covers this close. Only
// the writer close that owns its tree does: a reader runs in a scratch copy, a
// placed writer is mirror-only and its closed tree is the pulled result commit,
// and a served binding's tree belongs to the server that cut the round ref.
func chainDirtyGateApplies(rt Runtime, b store.Binding) bool {
	if b.Shape == store.ShapeReader {
		return false
	}
	if b.Serve != nil || b.Builder.Remote() {
		return false
	}
	return rt.Git != nil && b.CWD != ""
}

// chainDirtyReason is the halt wording for a round that left work behind: what
// is uncommitted, the snapshot ref that is its only durable copy, and the diff
// key the store sealed for the round.
func chainDirtyReason(rt Runtime, c db.ChainRow, b store.Binding, round int, summary string) string {
	return fmt.Sprintf("round %d of %s closed done with %s left uncommitted in %s and no commit to carry it: the chain does not advance over uncommitted work; commit it, or recover it from %s (sealed diff: %s)",
		round, c.Name, summary, b.CWD, chainRoundSnapshotRef(b, round), rt.Store.DiffPath(b.Name, round))
}

// chainDirtyUnreadReason is the halt wording for a tree the gate could not
// read: it names the same evidence, and says plainly that the tree state is
// unknown rather than reporting it clean.
func chainDirtyUnreadReason(c db.ChainRow, b store.Binding, round int, err error) string {
	return fmt.Sprintf("round %d of %s closed done but its tree %s could not be read (%v), so the chain cannot tell a finished round from uncommitted work and does not advance on the guess; commit the round, or recover it from %s",
		round, c.Name, b.CWD, err, chainRoundSnapshotRef(b, round))
}

// chainDirtySummary is the one-line count of what is uncommitted. A stat that
// cannot be read falls back to naming nothing counted rather than a number it
// does not have.
func chainDirtySummary(ctx context.Context, rt Runtime, b store.Binding) string {
	stat, err := rt.Git.DiffWorktreeStat(ctx, b.CWD, b.RoundBaselineTree)
	if err != nil || stat.Empty() {
		return "changes"
	}
	return fmt.Sprintf("%d files changed (+%d/-%d)", stat.FilesChanged, stat.Insertions, stat.Deletions)
}

// chainRoundSnapshotRef is the ref that holds a round's uncommitted work: the
// same namespace the remote allow-list admits and the remote note names, keyed
// on the closing writer and the round that closed.
func chainRoundSnapshotRef(b store.Binding, round int) string {
	return fmt.Sprintf("refs/relevo/%s/round-%d", b.Name, round)
}

// chainCloseArtifacts keys a closing round's artifacts: a writer exposes its
// report and its round diff; a reader exposes its saved output, plus every
// artifact the actor declares, all pointing at the saved output. round is the
// round that closed: the close's caller has already advanced the binding, so
// b.Round is the next round and the diff key must be the closed one's.
func chainCloseArtifacts(rt Runtime, b store.Binding, round int, path string, outs workflow.Outputs) map[string][]string {
	art := map[string][]string{}
	if b.Shape == store.ShapeReader {
		if path != "" {
			art["output"] = []string{path}
			for _, name := range outs.Artifacts() {
				art[name] = []string{path}
			}
		}
		return art
	}
	if path != "" {
		art["report"] = []string{path}
	}
	art["diff"] = []string{rt.Store.DiffPath(b.Name, round)}
	return art
}

// chainArtifactSizes measures every artifact key a close produced, so a
// declared artifact that is missing or empty closes the step halted.
func chainArtifactSizes(rt Runtime, art map[string][]string) map[string]int64 {
	sizes := make(map[string]int64, len(art))
	for name, keys := range art {
		var total int64
		for _, key := range keys {
			if s, _, ok, err := rt.Store.StatFile(key); err == nil && ok {
				total += s
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				continue
			}
		}
		sizes[name] = total
	}
	return sizes
}

// chainReaderCloseOutcome is the outcome a chain reader member's close
// records: the status its block-carrying message carries, read the way the
// chain reads it, or "" when the binding is no workflow chain's member or its
// declared outcomes do not parse (the close then keeps its own outcome).
func chainReaderCloseOutcome(rt Runtime, tx *store.Tx, b store.Binding, round int, body []byte) string {
	c, err := tx.ChainByMember(b.Name)
	if err != nil || len(c.WorkflowJSON) == 0 {
		return ""
	}
	actor := chainFlowActor(tx, c, b.Name)
	if actor == "" {
		actor = BindingRole(b)
	}
	info, _ := rt.RoleRegistry().ActorInfo(actor)
	bodies := chainOutcomeBodies(rt, b, round, body)
	if _, reason := workflow.ParseOutcomes(info.Outputs, bodies...); reason != "" {
		return ""
	}
	return chainReaderStatus(bodies, info.Outputs)
}
