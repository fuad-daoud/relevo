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

// chainResumeReroute names the step a resume re-enters instead of the stopped
// or halted one. A gate-replacing resume on a repair arm -- any step a check's
// red edge names -- goes back to the feeding check, so the check runs again
// under the new command: re-entering the arm would re-send a builder round
// naming the old failure, or halt on the arm's zero budget short of the check.
// A plain resume on a repair arm whose tree made no commit since the check ran
// goes back to that check too, without granting a fresh repair budget: the
// check re-runs on the true tree, and a repeat red re-halts without opening a
// repair round.
//
// Every other resume re-enters what it asked for. An explicit --from wins; a
// chain no red edge reaches resumes as before; and several feeding checks keep
// the refusal naming --from. The bool reports a no-commit reroute, so the
// caller keeps the arm's visits while a gate change keeps the fresh reset.
func chainResumeReroute(ctx context.Context, rt Runtime, c db.ChainRow, def workflow.Definition, st workflow.State, opts ResumeOptions) (string, bool, error) {
	if opts.From != "" {
		return opts.From, false, nil
	}
	if st.Status != workflow.StatusHalted && st.Status != workflow.StatusStopped {
		return opts.From, false, nil
	}
	checks := workflow.RedArmChecks(def, st.At)
	switch len(checks) {
	case 0:
		return opts.From, false, nil
	case 1:
	default:
		return "", false, refusal.New(
			"chain %s at %s, and %d checks reach it (%s); resume with --from <step> to name the one to rerun",
			st.Status, st.At, len(checks), strings.Join(checks, ", "))
	}
	if chainResumeChangesGate(opts) {
		return checks[0], false, nil
	}
	if next, ok := chainResumeNoCommitCheck(ctx, rt, c, def, st, checks[0]); ok {
		return next, true, nil
	}
	return "", false, nil
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
// budget it halted on.
func chainResumeVisits(before workflow.State, noCommit bool) map[string]int {
	if !noCommit {
		return map[string]int{}
	}
	out := map[string]int{}
	if before.Visits != nil {
		if v, ok := before.Visits[before.At]; ok {
			out[before.At] = v
		}
	}
	return out
}
