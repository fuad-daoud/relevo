package relevo

import (
	"context"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// resumeKeep names how much of the halted state's budget counts a resume
// carries into its re-entry: fresh, the no-commit arm's own count, or every
// count a red-gate reroute stood on.
type resumeKeep int

const (
	keepFresh resumeKeep = iota
	keepArm
	keepAll
)

// chainResumeReroute names the step a resume re-enters instead of the stopped
// or halted one. A gate-replacing resume on a repair arm -- any step a check's
// red edge names -- goes back to the feeding check, so the check runs again
// under the new command: re-entering the arm would re-send a builder round
// naming the old failure, or halt on the arm's zero budget short of the check.
// A plain resume on a halted repair arm whose tree made no commit since the
// check ran goes back to that check too, without granting a fresh repair
// budget: the check re-runs on the true tree, and a repeat red re-halts
// without opening a repair round. A plain resume on a pass-while-red halt --
// a pass that halted on a still-red check -- goes back to that gate, keeping
// every spent visit: the gate is the check's own red, so no worktree
// comparison decides it. A stopped resume keeps the arm so it re-sends the
// stopped round's own staged prompt.
//
// Every other resume re-enters what it asked for. An explicit --from wins; a
// gate-changing resume keeps today's repair-arm routing; a chain no red edge
// reaches resumes as before; and several feeding checks keep the refusal
// naming --from. The keep reports which visits the caller carries forward.
func chainResumeReroute(ctx context.Context, rt Runtime, c db.ChainRow, def workflow.Definition, st workflow.State, opts ResumeOptions) (string, resumeKeep, error) {
	if opts.From != "" {
		return opts.From, keepFresh, nil
	}
	if st.Status != workflow.StatusHalted && st.Status != workflow.StatusStopped {
		return opts.From, keepFresh, nil
	}
	if st.Status == workflow.StatusHalted && !chainResumeChangesGate(opts) {
		if gate := workflow.GatingRedCheck(def, st); gate != "" {
			return gate, keepAll, nil
		}
	}
	checks := workflow.RedArmChecks(def, st.At)
	switch len(checks) {
	case 0:
		return opts.From, keepFresh, nil
	case 1:
	default:
		return "", keepFresh, refusal.New(
			"chain %s at %s, and %d checks reach it (%s); resume with --from <step> to name the one to rerun",
			st.Status, st.At, len(checks), strings.Join(checks, ", "))
	}
	if chainResumeChangesGate(opts) {
		return checks[0], keepFresh, nil
	}
	if st.Status == workflow.StatusHalted {
		if next, ok := chainResumeNoCommitCheck(ctx, rt, c, def, st, checks[0]); ok {
			return next, keepArm, nil
		}
	}
	return "", keepFresh, nil
}

// chainResumeNoCommitCheck names the feeding check a plain resume re-enters
// when the repair arm made no commit since that check ran, and false when the
// resume must re-enter the arm. Every unreadable case keeps the arm: the
// waste is one builder round, never a check run on a misread tree.
func chainResumeNoCommitCheck(ctx context.Context, rt Runtime, c db.ChainRow, def workflow.Definition, st workflow.State, check string) (string, bool) {
	reason := st.Reason
	if reason == "" {
		reason = c.Reason
	}
	if chainHaltedOverUncommittedWork(reason) {
		return "", false
	}
	step, ok := def.Steps[st.At]
	if !ok || step.Run == "" {
		return "", false
	}
	actor := workflow.RenderParams(def, step.Run)
	if actor == "" {
		return "", false
	}
	if rt.Store == nil {
		return "", false
	}
	members, err := rt.Store.ChainMembers(c.Name)
	if err != nil {
		return "", false
	}
	binding := ""
	for _, m := range members {
		if chain.FlowActorMatch(m.Actor, actor) {
			binding = m.Binding
			break
		}
	}
	if binding == "" {
		return "", false
	}
	b, err := rt.Store.Load(binding)
	if err != nil {
		return "", false
	}
	// The gate decides which trees this machine may read: a mirror has no
	// local tree, and a reader runs in a scratch copy, while a served binding
	// reads on the server that decides.
	if !chainDirtyGateApplies(rt, b) {
		return "", false
	}
	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		return "", false
	}
	// A round that never reported gave the builder no chance: re-entering the
	// arm lets it run, rather than checking a tree it never touched.
	if store.RoundOpen(entries, b.Round) {
		return "", false
	}
	if b.RoundClosedBaselineHead == "" {
		return "", false
	}
	head, err := rt.Git.HeadCommit(ctx, b.CWD)
	if err != nil || head == "" {
		return "", false
	}
	if head != b.RoundClosedBaselineHead {
		return "", false
	}
	return check, true
}

// chainResumeVisits is the visits a resume starts from: fresh, except a
// no-commit reroute keeps the arm's count so the re-run check meets the same
// budget it halted on, and a red-gate reroute keeps every count. Entering the
// check resets only budgets naming the check, and the resume zeroes only the
// check's own count, so a full keep neither resets nor spends anything.
func chainResumeVisits(before workflow.State, keep resumeKeep) map[string]int {
	if keep == keepFresh {
		return map[string]int{}
	}
	out := map[string]int{}
	if keep == keepArm {
		if before.Visits != nil {
			if v, ok := before.Visits[before.At]; ok {
				out[before.At] = v
			}
		}
		return out
	}
	for id, v := range before.Visits {
		out[id] = v
	}
	return out
}
