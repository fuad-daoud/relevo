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
	"github.com/fuad-daoud/relevo/internal/resolve"
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
	// DirtyCommit is the round's uncommitted-work snapshot commit, from the
	// pulled view the close was built on. Non-empty says the round closed
	// over work the tree it ran in never committed; "" only says no fact was
	// carried, which a clean close and a close whose facts ship neither both
	// are. The gate that reads it decides for a close whose tree this
	// machine does not own.
	DirtyCommit string
}

// chainEventFromCloseWF maps one member close onto the workflow event the
// engine reads. The step and member come from the chain state's Awaiting; the
// closing binding's actor comes from its chain_member row. A stopped close is
// the engine's stopped event. Otherwise it parses the actor's declared
// outcomes, keys its artifacts and closes the step halted when a declared
// artifact is missing.
func chainEventFromCloseWF(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, b store.Binding, wf chainCloseWF) (workflow.Event, error) {
	st, err := chain.WorkflowState(c)
	if err != nil {
		return workflow.Event{}, err
	}
	actor := chainFlowActor(tx, c, b.Name)
	if actor == "" {
		actor = resolve.BindingRole(b)
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
		ev.Status = chain.ReaderStatus(bodies, outs)
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
		// A done close whose tree this machine does not own answers from the
		// facts the close was built on instead: a mirror round that says it
		// left uncommitted work behind halts the same shape, on the pulled
		// word, for the same reason the readable tree gives.
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

// chainPlanStepsReason is the plan gate's answer for a close: the halt wording
// when the awaited step writes a plan and the output the close saved holds no
// plan steps, and "" otherwise. It judges the saved body, as the builder would
// receive it, not the stream. A chain with no readable stored definition skips
// the gate.
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
// the writer close that owns its tree does: a reader runs in a scratch copy,
// and a mirror-placed writer's closed tree is the pulled result commit on the
// server -- its row's CWD is this machine's own repo, which it must not probe.
// A served binding's tree belongs to the server, and the close runs on the
// server whose daemon can read it, so it keeps the gate like any owned tree.
func chainDirtyGateApplies(rt Runtime, b store.Binding) bool {
	if b.Shape == store.ShapeReader {
		return false
	}
	if b.Builder.Remote() {
		return false
	}
	return rt.Git != nil && b.CWD != ""
}

// chainUncommittedWorkClause is the part of both dirty-tree halt wordings that
// a stored halt reason is recognised by; the unread-tree wording leaves it out
// because a commit does not recover a tree that could not be read.
const chainUncommittedWorkClause = "the chain does not advance over uncommitted work"

// chainHaltedOverUncommittedWork reports whether a stored halt reason came from
// the readable-tree or the mirror dirty wording.
func chainHaltedOverUncommittedWork(reason string) bool {
	return strings.Contains(reason, chainUncommittedWorkClause)
}

// chainDirtyReason is the halt wording for a round that left work behind: what
// is uncommitted, the snapshot ref that is its only durable copy, and the diff
// key the store sealed for the round.
func chainDirtyReason(rt Runtime, c db.ChainRow, b store.Binding, round int, summary string) string {
	return fmt.Sprintf("round %d of %s closed done with %s left uncommitted in %s and no commit to carry it: %s; commit it, or recover it from %s (sealed diff: %s)",
		round, c.Name, summary, b.CWD, chainUncommittedWorkClause, chain.SnapshotRef(b, round), rt.Store.DiffPath(b.Name, round))
}

// chainDirtyUnreadReason is the halt wording for a tree the gate could not
// read: it names the same evidence, and says plainly that the tree state is
// unknown rather than reporting it clean.
func chainDirtyUnreadReason(c db.ChainRow, b store.Binding, round int, err error) string {
	return fmt.Sprintf("round %d of %s closed done but its tree %s could not be read (%v), so the chain cannot tell a finished round from uncommitted work and does not advance on the guess; commit the round, or recover it from %s",
		round, c.Name, b.CWD, err, chain.SnapshotRef(b, round))
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

// chainMirrorDirtyCloseReason is the dirty gate's answer for a close whose
// tree is elsewhere: the halt wording when the round the close was built on
// says it left uncommitted work behind, and "" for a close whose facts carry
// nothing or whose tree this machine does own (the readable-tree gate answers
// those).
func chainMirrorDirtyCloseReason(rt Runtime, c db.ChainRow, b store.Binding, round int, dirtyCommit string) string {
	if dirtyCommit == "" || !b.Builder.Remote() {
		return ""
	}
	return chainMirrorDirtyReason(rt, c, b, round)
}

// chainMirrorDirtyReason is the halt wording for a mirror round that closed
// done with uncommitted work on its server: the same wording chainDirtyReason
// gives a readable tree, with the files the round's sealed diff patch names in
// place of a tree stat, the branch it never reached named where a local tree
// would be, and the server's snapshot ref where the work survives.
func chainMirrorDirtyReason(rt Runtime, c db.ChainRow, b store.Binding, round int) string {
	return fmt.Sprintf("round %d of %s closed done with uncommitted work (%s) left on the server and no commit in %s to carry it: %s; commit it, or recover it from %s (sealed diff: %s)",
		round, c.Name, chainSealedDiffPaths(rt, b, round), b.Branch, chainUncommittedWorkClause, chain.SnapshotRef(b, round), rt.Store.DiffPath(b.Name, round))
}

// chainSealedDiffPaths reads the round's sealed diff patch and names the files
// it changes. For a mirror round the patch -- which for a close whose tree
// was dirty includes the worktree -- is the only copy of what is uncommitted,
// so its paths are what the wording can put in a human's hands. A diff that
// cannot be read, or one that names no file, falls back to the count word a
// tree stat without a stat gives, rather than paths it does not have.
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

// gitPatchPath is the path a `diff --git a/<p> b/<p>` header names, "" for any
// other line. A path with no space survives; one with an embedded space is
// guessed as the shorter a-side name, which is only ever wording of a hint.
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
		actor = resolve.BindingRole(b)
	}
	info, _ := rt.RoleRegistry().ActorInfo(actor)
	bodies := chainOutcomeBodies(rt, b, round, body)
	if _, reason := workflow.ParseOutcomes(info.Outputs, bodies...); reason != "" {
		return ""
	}
	return chain.ReaderStatus(bodies, info.Outputs)
}
