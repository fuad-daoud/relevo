package relevo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainPlanDiff captures the plan's cumulative diff -- the plan-start commit to
// the closing round's closed tree -- as a round_file beside the round diff, and
// returns its key, or "" when there is nothing to capture. It NEVER fails the
// close: an empty plan-start commit, a builder whose closed tree is unknown, no
// git and a git failure all read as "not captured", the same shape capture's
// own diff results use.
func chainPlanDiff(rt Runtime, tx *store.Tx, c db.ChainRow, builder string, builderRound int) string {
	if c.PlanStartCommit == "" {
		return ""
	}
	b, err := tx.Load(builder)
	if err != nil || b.RoundClosedTree == "" {
		return ""
	}
	path := rt.Store.PlanDiffPath(builder, builderRound)
	res := capture.PlanDiff(context.Background(), captureDeps(rt), tx, capture.PlanDiffSpec{
		Name: builder, Round: builderRound,
		Dir: b.CWD, From: c.PlanStartCommit, End: b.RoundClosedTree, Path: path,
	})
	if res.Path == "" {
		return ""
	}
	return path
}

// chainBranchDiff captures the whole branch's diff -- the chain's base commit to
// the builder's newest closed tree -- as a row-only round_file key and returns
// that key, or "" when there is nothing to capture. It is chainPlanDiff's twin
// for the span that judges the branch as a whole: the base the builder's
// worktree was cut from to the tree the store snapshotted when the builder round
// closed, not the worktree's moving HEAD. It NEVER fails the close: no base, a
// round below the first, no builder record, an empty closed tree, no git and a
// git failure all read as "not captured".
func chainBranchDiff(rt Runtime, tx *store.Tx, c db.ChainRow, builder string, builderRound int) string {
	if c.Base == "" || builderRound < 1 {
		return ""
	}
	b, err := tx.Load(builder)
	if err != nil || b.RoundClosedTree == "" {
		return ""
	}
	path := rt.Store.ChainDiffPath(builder, builderRound)
	res := capture.PlanDiff(context.Background(), captureDeps(rt), tx, capture.PlanDiffSpec{
		Name: builder, Round: builderRound,
		Dir: b.CWD, From: c.Base, End: b.RoundClosedTree, Path: path,
	})
	if res.Path == "" {
		return ""
	}
	return path
}

// chainSeedPlanView fills the plan copies a seed may name: every plan copy the
// chain holds, in plan order, made seed-openable by chainSeedInput, and the copy
// for the current plan. A decode error leaves both empty, as the plan block did
// before it moved here.
func chainSeedPlanView(rt Runtime, c db.ChainRow, plan int, v *workflow.SeedView) {
	paths, err := chainPlanPaths(c)
	if err != nil {
		return
	}
	for _, p := range paths {
		v.PlanPaths = append(v.PlanPaths, chainSeedInput(rt, c, p))
	}
	if plan >= 1 && plan <= len(paths) {
		v.PlanPath = chainSeedInput(rt, c, paths[plan-1])
	}
}

// chainOutcomeBodies orders a closing round's bodies for the outcome parse: the
// in-memory body first, then the round's stream newest assistant message first.
// round names the round that closed, which is not always the binding's own: a
// close advances the binding before the chain moves.
func chainOutcomeBodies(rt Runtime, b store.Binding, round int, body []byte) [][]byte {
	bodies := [][]byte{body}
	if stream, err := rt.Store.ReadFile(rt.Store.StreamPath(b.Name, round)); err == nil {
		texts := transcript.Texts(view.LastStreamKind(b), stream)
		for i := len(texts) - 1; i >= 0; i-- {
			bodies = append(bodies, []byte(texts[i]))
		}
	}
	return bodies
}

// chainParseOutcomes reads the declared outcomes of a chain reader round, trying
// the output body first, then the round's stream newest assistant message first.
// round names the round that closed: a close advances the binding before the
// chain moves, so the binding's own round is no longer the closed one.
func chainParseOutcomes(rt Runtime, b store.Binding, round int, body []byte, outputs workflow.Outputs) (map[string]string, string) {
	return workflow.ParseOutcomes(outputs, chainOutcomeBodies(rt, b, round, body)...)
}

// chainBuilderPlanView is the plan view the reviewer and correction seeds carry:
// which kind the closing builder round is when it is not the plan's first, the
// round it sits on top of, every builder round of the plan with its openable
// paths, and the commit to diff the plan from when no cumulative diff was
// captured. The plan's first builder round is the newest round at or below
// builderRound whose staged prompt equals the chain's staged plan copy: a
// start/advance send hands the builder that copy, while a correction, a repair
// and a human round carry other bytes. A closing round that is the plan's first
// (or a chain whose copy cannot be read) gets no kind and no list.
func chainBuilderPlanView(rt Runtime, tx *store.Tx, c db.ChainRow, s chain.State, act chain.Action, builder string, builderRound int) (kind string, on int, rounds []workflow.SeedRound, diffFrom string) {
	diffFrom = chain.PlanDiffFrom(c, s)
	paths, err := chainPlanPaths(c)
	if err != nil || s.Plan < 1 || s.Plan > len(paths) {
		return "", 0, nil, diffFrom
	}
	plan, err := rt.Store.ReadFile(paths[s.Plan-1])
	if err != nil {
		return "", 0, nil, diffFrom
	}
	first := 0
	for r := builderRound; r >= 1; r-- {
		staged, err := rt.Store.ReadFile(rt.Store.PromptPath(builder, r))
		if err != nil {
			continue
		}
		if bytes.Equal(staged, plan) {
			first = r
			break
		}
	}
	if first == 0 || first == builderRound {
		return "", 0, nil, diffFrom
	}
	kind = chainBuilderRoundKind(rt, tx, c, s, builder, builderRound)
	on = builderRound - 1
	for r := first; r <= builderRound; r++ {
		rounds = append(rounds, workflow.SeedRound{
			Round:      r,
			PromptPath: chainSeedInput(rt, c, rt.Store.PromptPath(builder, r)),
			ReportPath: chainSeedInput(rt, c, rt.Store.ReportPath(builder, r)),
		})
	}
	return kind, on, rounds, diffFrom
}

// chainBuilderRoundKind names the closing builder round: a repair round when
// its prompt entry says so, else a correction or fix-plan round when it ran the
// planner's newest plan, else a round a human sent.
func chainBuilderRoundKind(rt Runtime, tx *store.Tx, c db.ChainRow, s chain.State, builder string, builderRound int) string {
	if chainBuilderRoundIsRepair(rt, tx, builder, builderRound) {
		return workflow.BuilderRoundRepair
	}
	if chainBuilderRanThePlannerPlan(rt, tx, c, builder, builderRound) {
		if s.Phase == chain.PhaseSecurity {
			return workflow.BuilderRoundFix
		}
		return workflow.BuilderRoundCorrection
	}
	return workflow.BuilderRoundHuman
}

// chainBuilderRoundIsRepair reports whether the builder's prompt entry for the
// round carries a repair note: the repair round is the only send that writes
// one.
func chainBuilderRoundIsRepair(rt Runtime, tx *store.Tx, builder string, round int) bool {
	// An engine chain's repair send writes the repair text but not a legacy
	// "repair k/M" note, so the staged prompt itself identifies the round.
	if staged, err := rt.Store.ReadFile(rt.Store.PromptPath(builder, round)); err == nil && isRepairPlan(string(staged)) {
		return true
	}
	entries, err := tx.ReadLog(builder)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Direction == store.DirToBuilder && e.Kind == store.KindPrompt && e.Round == round &&
			strings.HasPrefix(e.Note, "repair ") {
			return true
		}
	}
	return false
}

// chainBuilderRanThePlannerPlan reports whether the builder's staged prompt for
// the round is the planner's newest closed plan: a correction or fix-plan send
// hands the builder the planner's own text, so its bytes match.
func chainBuilderRanThePlannerPlan(rt Runtime, tx *store.Tx, c db.ChainRow, builder string, builderRound int) bool {
	planRound := chain.NewestClosedRound(tx, c.Planner)
	if planRound == 0 {
		return false
	}
	path, err := chain.MemberReportPath(tx, c.Planner, planRound)
	if err != nil {
		return false
	}
	plan, err := rt.Store.ReadFile(path)
	if err != nil {
		return false
	}
	staged, err := rt.Store.ReadFile(rt.Store.PromptPath(builder, builderRound))
	if err != nil {
		return false
	}
	return bytes.Equal(staged, plan)
}

// chainRoundPromptPath is the round's own staged prompt when it is not the plan
// copy itself, and "" when the round ran the plan copy: a builder round handed
// the plan's own bytes needs no prompt named beside it, while a correction or
// fix round, whose prompt is the planner's text, does.
func chainRoundPromptPath(rt Runtime, planPath, builder string, builderRound int) string {
	promptPath := rt.Store.PromptPath(builder, builderRound)
	staged, perr := rt.Store.ReadFile(promptPath)
	plan, lerr := rt.Store.ReadFile(planPath)
	if perr != nil || lerr != nil || !bytes.Equal(staged, plan) {
		return promptPath
	}
	return ""
}

// chainSeedInput is the one path a seed may name for one input. An empty path
// stays empty. A binding round file is always copied under the chain's own
// directory and the copy is named, whether it is a reserved key, a sealed row or
// a file still on disk: a seal moves an on-disk round file into its row and
// removes it, and it can do so between this seed being written and the runner
// opening it. Any other regular file the store reads as itself, such as the
// chain's own plan copy, is named as it is.
//
// A read, a key or a write that fails yields chain.SeedMissing's one path-free
// clause, so a seed never names a path a runner cannot open.
func chainSeedInput(rt Runtime, c db.ChainRow, path string) string {
	if path == "" {
		return ""
	}
	dest, roundFile := rt.Store.ChainInputPath(c.Name, path)
	if !roundFile && rt.Store.DiskRegularFile(path) {
		return path
	}
	body, err := rt.Store.ReadFile(path)
	if err != nil {
		return chain.SeedMissing(err)
	}
	if !roundFile {
		return chain.SeedMissing(errors.New("not a binding round file"))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return chain.SeedMissing(err)
	}
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return chain.SeedMissing(err)
	}
	return dest
}
