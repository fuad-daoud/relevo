package relevo

import (
	"bytes"
	"context"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
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

// plannerReportPath is the path the planning member's report entry for round
// carries: the correction or fix plan artifact the planner wrote, which is what
// a builder round on that plan must be handed. A planner with no such entry, or
// one with no path, is a failure of the send rather than a silent plan copy.
func plannerReportPath(tx *store.Tx, planner string, round int) (string, error) {
	entries, err := tx.ReadLog(planner)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Kind == store.KindReport && e.Round == round && e.Path != "" {
			return e.Path, nil
		}
	}
	return "", fmt.Errorf("chain planner %s has no plan for round %d", planner, round)
}
