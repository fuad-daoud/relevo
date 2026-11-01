package relevo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

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
	// Note is the close's own note, folded into a non-done builder's reason
	// when its report names no source.
	Note string
	// Round is the round that closed. The caller advances the binding before
	// the chain moves, so the event names the round the step awaited.
	Round int
	// DirtyCommit is the round's uncommitted-work snapshot commit, from the
	// pulled view the close was built on. "" says only that no fact was
	// carried; the gate decides for a tree this machine does not own.
	DirtyCommit string
}

// chainEventFromCloseWF maps one member close onto the workflow event the
// engine reads. A stopped close is the engine's stopped event; any other
// parses the actor's declared outcomes and halts the step on a missing
// declared artifact.
func chainEventFromCloseWF(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, b store.Binding, wf chainCloseWF) (workflow.Event, error) {
	st, err := chain.WorkflowState(c)
	if err != nil {
		return workflow.Event{}, err
	}
	actor := chainFlowActor(tx, c, b.Name)
	if actor == "" {
		actor = b.WriterRole()
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
	// A writer's report status is the round's outcome. A reader's saved
	// outcome says nothing about the step -- its block was already stripped
	// -- so the reader routes on the status its own block carries.
	if b.Shape == store.ShapeReader {
		ev.Status = chain.ReaderStatus(bodies, outs)
	} else {
		ev.Status = wf.Outcome
		// A builder close that is not done carries why in one line from its
		// report tail; the engine's unmatched-run halt uses it verbatim.
		if wf.Outcome != reporttail.OutcomeDone {
			tail, _, _ := reporttail.ParseWithReason(wf.Body)
			ev.Reason = chain.BuilderHaltReason(tail, wf.Note, wf.Outcome)
			ev.HaltReason = fmt.Sprintf("builder halted on plan %d: %s", c.Plan, ev.Reason)
			return ev, nil
		}
		// A done close over uncommitted work carries no round forward: the
		// sealed diff is not the work the builder left, so advancing would
		// hand on a step that looks complete and is not.
		if reason := chainDirtyCloseReason(ctx, rt, c, b, round); reason != "" {
			ev.Status = reporttail.OutcomeHalted
			ev.Reason = reason
			ev.HaltReason = reason
			return ev, nil
		}
		// A done close whose tree this machine does not own answers from the
		// facts the close was built on: a mirror round reporting uncommitted
		// work halts the same shape, on the pulled word.
		if reason := chainMirrorDirtyCloseReason(rt, c, b, round, wf.DirtyCommit); reason != "" {
			ev.Status = reporttail.OutcomeHalted
			ev.Reason = reason
			ev.HaltReason = reason
			return ev, nil
		}
	}

	if reason := chainPlanStepsReason(rt, c, st.Awaiting.Step, wf); reason != "" {
		ev.Status = reporttail.OutcomeHalted
		ev.Reason = reason
		ev.HaltReason = reason
		return ev, nil
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

// chainPlanStepsReason is the plan gate's answer for a close: the halt
// wording when the awaited step writes a plan but the saved output holds no
// plan steps, "" otherwise. It judges the saved body, not the stream; a
// chain with no readable definition skips the gate.
func chainPlanStepsReason(rt Runtime, c db.ChainRow, step string, wf chainCloseWF) string {
	def, err := chain.WorkflowDef(c)
	if err != nil || !chainStepWritesPlan(def, step) {
		return ""
	}
	body := wf.Body
	if len(body) == 0 && wf.Path != "" {
		body, _ = rt.Store.ReadFile(wf.Path)
	}
	if planHasSteps(body) {
		return ""
	}
	return fmt.Sprintf("step %s of chain %s: the planner's output has no plan steps (it may point at a file instead of containing the plan); nothing was sent to the builder", step, c.Name)
}

// chainDirtyCloseReason is the dirty gate's answer for one writer close: the
// halt wording when the round must not advance, and "" when it may. A git
// error is not a clean tree, and the gate never fails the close it reads:
// it decides in band, exactly as escapeCheck does, so the trace never
// records an unread tree as finished.
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

// chainDirtyGateApplies reports whether the dirty gate covers this close.
// Only the writer close that owns its tree: a reader runs in a scratch copy,
// and a mirror-placed writer's CWD is this machine's own repo, which the gate
// must not probe. A served binding keeps the gate: its close runs on the
// server whose daemon can read its tree.
func chainDirtyGateApplies(rt Runtime, b store.Binding) bool {
	if b.Shape == store.ShapeReader {
		return false
	}
	if b.Builder.Remote() {
		return false
	}
	return rt.Git != nil && b.CWD != ""
}

// chainUncommittedWorkClause is the clause of both dirty-tree wordings that
// a stored halt reason is recognised by; the unread-tree wording leaves it
// out, since no commit recovers an unreadable tree.
const chainUncommittedWorkClause = "the chain does not advance over uncommitted work"

func chainHaltedOverUncommittedWork(reason string) bool {
	return strings.Contains(reason, chainUncommittedWorkClause)
}

// chainDirtyReason is the halt wording for a round that left work behind:
// what is uncommitted, its only durable copy, and the sealed diff.
func chainDirtyReason(rt Runtime, c db.ChainRow, b store.Binding, round int, summary string) string {
	return fmt.Sprintf("round %d of %s closed done with %s left uncommitted in %s and no commit to carry it: %s; commit it, or recover it from %s (sealed diff: %s)",
		round, c.Name, summary, b.CWD, chainUncommittedWorkClause, chain.SnapshotRef(b, round), rt.Store.DiffPath(b.Name, round))
}

// chainDirtyUnreadReason is the halt wording for a tree the gate could not
// read: the tree state is unknown, never reported clean.
func chainDirtyUnreadReason(c db.ChainRow, b store.Binding, round int, err error) string {
	return fmt.Sprintf("round %d of %s closed done but its tree %s could not be read (%v), so the chain cannot tell a finished round from uncommitted work and does not advance on the guess; commit the round, or recover it from %s",
		round, c.Name, b.CWD, err, chain.SnapshotRef(b, round))
}

// chainDirtySummary is the one-line count of what is uncommitted. An
// unreadable stat falls back to "changes", never a number it does not have.
func chainDirtySummary(ctx context.Context, rt Runtime, b store.Binding) string {
	stat, err := rt.Git.DiffWorktreeStat(ctx, b.CWD, b.RoundBaselineTree)
	if err != nil || stat.Empty() {
		return "changes"
	}
	return fmt.Sprintf("%d files changed (+%d/-%d)", stat.FilesChanged, stat.Insertions, stat.Deletions)
}

// chainMirrorDirtyCloseReason is the dirty gate's answer for a close whose
// tree is elsewhere: the halt wording when the pulled facts say the round
// left work behind, "" otherwise.
func chainMirrorDirtyCloseReason(rt Runtime, c db.ChainRow, b store.Binding, round int, dirtyCommit string) string {
	if dirtyCommit == "" || !b.Builder.Remote() {
		return ""
	}
	return chainMirrorDirtyReason(rt, c, b, round)
}

// chainMirrorDirtyReason is the halt wording for a mirror round that closed
// done with uncommitted work on its server: the sealed diff's paths stand in
// for a tree stat the gate cannot read.
func chainMirrorDirtyReason(rt Runtime, c db.ChainRow, b store.Binding, round int) string {
	return fmt.Sprintf("round %d of %s closed done with uncommitted work (%s) left on the server and no commit in %s to carry it: %s; commit it, or recover it from %s (sealed diff: %s)",
		round, c.Name, chainSealedDiffPaths(rt, b, round), b.Branch, chainUncommittedWorkClause, chain.SnapshotRef(b, round), rt.Store.DiffPath(b.Name, round))
}

// chainSealedDiffPaths names the files the round's sealed diff changes. For
// a mirror round the patch is the only copy of what is uncommitted. An
// unreadable diff, or one naming no file, falls back to "changes".
func chainSealedDiffPaths(rt Runtime, b store.Binding, round int) string {
	body, _ := rt.Store.ReadFile(rt.Store.DiffPath(b.Name, round))
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		if p := gitPatchPath(line); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return "changes"
	}
	return strings.Join(out, ", ")
}

// gitPatchPath is the path a `diff --git a/<p> b/<p>` header names, "" for
// any other line. A path with an embedded space is guessed as the shorter
// a-side name: it is only ever wording of a hint.
func gitPatchPath(line string) string {
	const head = "diff --git a/"
	if !strings.HasPrefix(line, head) {
		return ""
	}
	rest := strings.TrimPrefix(line, head)
	if i := strings.Index(rest, " b/"); i > 0 {
		rest = rest[:i]
	}
	return rest
}

// chainCloseArtifacts keys a closing round's artifacts: a writer its report
// and round diff, a reader its saved output for every declared artifact.
// round is the round that closed: the caller already advanced the binding,
// so b.Round is the next one.
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
// records: the status its block carries, or "" when the binding is no chain
// member or its outcomes do not parse -- the close then keeps its own.
func chainReaderCloseOutcome(rt Runtime, tx *store.Tx, b store.Binding, round int, body []byte) string {
	c, err := tx.ChainByMember(b.Name)
	if err != nil || len(c.WorkflowJSON) == 0 {
		return ""
	}
	actor := chainFlowActor(tx, c, b.Name)
	if actor == "" {
		actor = b.WriterRole()
	}
	info, _ := rt.RoleRegistry().ActorInfo(actor)
	bodies := chainOutcomeBodies(rt, b, round, body)
	if _, reason := workflow.ParseOutcomes(info.Outputs, bodies...); reason != "" {
		return ""
	}
	return chain.ReaderStatus(bodies, info.Outputs)
}
