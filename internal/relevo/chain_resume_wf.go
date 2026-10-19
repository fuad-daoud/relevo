package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainResumePostWriteHook is nil outside tests; a test sets it to fail a
// resume after its first write, the one seam that reaches the undo path.
var chainResumePostWriteHook func(tx *store.Tx, c db.ChainRow) error

// chainResumeWorkflow continues a halted or stopped workflow chain: it
// re-enters the step the engine is on -- or the one --from names -- applies
// --param and the old flags to the stored definition, creates the members the
// params brought in, and runs the resumed action.
func chainResumeWorkflow(ctx context.Context, rt Runtime, c db.ChainRow, opts ResumeOptions) (ChainResult, error) {
	if err := resumeRefusal(c); err != nil {
		return ChainResult{}, err
	}
	// A child's resume is only meaningful while its parent can follow it back
	// into the fork, so the parent is asked first and nothing is written on a
	// refusal.
	if err := chainForkParentGate(rt, c); err != nil {
		return ChainResult{}, err
	}
	// A gate change on a chain whose writer runs on a server has to reach that
	// server's binding, so it is refused there when the server cannot take it.
	// The push that delivers it waits for every other refusal below to clear.
	if chainResumeChangesGate(opts) {
		if err := resumeRemoteGateRefusal(ctx, rt, c); err != nil {
			return ChainResult{}, err
		}
	}
	// A workflow chain whose awaited member's round was left open by the halt --
	// no report, and no close -- would otherwise be refused for that open round.
	// A resume is the human ending it, so it is closed the way a stop does, and
	// the resume can open the next one.
	if err := closeDeadMemberRound(ctx, rt, c); err != nil {
		return ChainResult{}, err
	}
	// The row's engine definition and state, migrated onto the engine when the
	// row still predates it and refused in the input class when it carries
	// neither, so a resume never answers internal on a row a human can read.
	c, def, before, err := chainResumeWorkflowRow(rt, c)
	if err != nil {
		return ChainResult{}, err
	}
	set, err := resumeSettings(rt, c, opts)
	if err != nil {
		return ChainResult{}, err
	}
	// A parent standing on a fork cannot re-enter the fork step -- entering it
	// starts the children a second time. So a fork with a child still standing is
	// refused here, before anything is written and before the chain's own end
	// delivery is superseded: the human resumes the child instead.
	if err := chainForkResumeRefusal(c, before); err != nil {
		return ChainResult{}, err
	}
	// The old flags fill the workflow's params exactly as a start's do, so a
	// resume can replace the check or the budgets the chain runs under.
	flagParams, err := chainResumeFlagParams(def, rt, opts)
	if err != nil {
		return ChainResult{}, err
	}
	merged := map[string]string{}
	for key, value := range flagParams {
		merged[key] = value
	}
	for key, value := range opts.Params {
		merged[key] = value
	}
	if len(merged) > 0 {
		if def, err = workflow.WithParams(def, merged); err != nil {
			return ChainResult{}, refuse("%v", err)
		}
		given := workflow.Given{Plans: len(before.Iter["plans"].Items) > 0}
		if err := chainValidateWorkflow(rt, def, given); err != nil {
			return ChainResult{}, err
		}
	}
	// A resume that replaces the check on a halt a zero-budget repair arm
	// caused goes back to that arm's check rather than to the arm itself, so
	// the check runs again under the new command instead of halting on the very
	// budget it had before. Every other resume re-enters what it asked for.
	// The refusal lands here, before the delivery below is superseded and
	// before the lock writes anything.
	from, err := chainResumeReroute(def, before, opts)
	if err != nil {
		return ChainResult{}, err
	}
	// A human has looked at the plan: every budget starts fresh, exactly as a
	// resume resets the correction count on the fixed state machine.
	before.Visits = map[string]int{}

	var out ChainResult
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		res, lerr := chainResumeLocked(ctx, rt, tx, opts, def, before, from, set, merged)
		if lerr != nil {
			return lerr
		}
		out = res
		return nil
	})
	if err != nil {
		return ChainResult{}, err
	}
	// A resume whose send could not start halts the chain, exactly as the fixed
	// state machine's resume returns the send's own error. The halt is the
	// answer -- nothing about the resume was malformed and nothing of relevo's
	// own failed -- so it is refused, not reported as an internal fault: the
	// CLI reads a refusal as exit 2 with the reason to act on, and an internal
	// as "send this to bugreport" on a row the human can read and fix.
	if out.Chain.Status == string(workflow.StatusHalted) {
		return out, refuse("chain %s is still halted: %s", out.Chain.Name, out.Chain.Reason)
	}
	// The resume itself has committed and nothing refused it, so the gate change
	// a placed writer's server is owed can go out now. Any later abort of this
	// resume -- the shipped round -- can no longer leave the server ahead of a
	// chain whose own settings already carry the new gate.
	if chainResumeChangesGate(opts) {
		if err := resumeRemoteGatePush(ctx, rt, out.Chain, opts); err != nil {
			return ChainResult{}, err
		}
	}
	return out, nil
}

func chainResumeCommitRow(tx *store.Tx, row *db.ChainRow, def workflow.Definition, resumed workflow.State, now time.Time) error {
	defJSON, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("encode chain workflow: %w", err)
	}
	row.WorkflowJSON = defJSON
	row.Status = string(resumed.Status)
	stateJSON, err := json.Marshal(resumed)
	if err != nil {
		return fmt.Errorf("encode chain state: %w", err)
	}
	row.StateJSON = stateJSON
	applyChainLegacy(row, def, resumed)
	row.UpdatedAt = now
	return tx.ChainPut(*row)
}

// chainResumeLocked performs the atomic state mutation under the store lock.
func chainResumeLocked(ctx context.Context, rt Runtime, tx *store.Tx, opts ResumeOptions, def workflow.Definition, before workflow.State, from string, set chain.Settings, merged map[string]string) (ChainResult, error) {
	row, err := tx.Chain(opts.Name)
	if err != nil {
		return ChainResult{}, err
	}
	if err := resumeRefusal(row); err != nil {
		return ChainResult{}, err
	}
	if handled, res, err := chainForkResumeParent(ctx, rt, tx, row, def, before); err != nil {
		return ChainResult{}, err
	} else if handled {
		return res, nil
	}
	snap, err := newChainResumeSnapshot(tx, row)
	if err != nil {
		return ChainResult{}, err
	}
	toConfirm, err := collectPendingChainDeliveries(tx, row)
	if err != nil {
		return ChainResult{}, err
	}
	settingsJSON, err := json.Marshal(set)
	if err != nil {
		return ChainResult{}, fmt.Errorf("encode chain %s settings: %w", row.Name, err)
	}
	row.SettingsJSON = settingsJSON
	closed, err := chainResumeClosed(ctx, rt, tx, row, before)
	if err != nil {
		return ChainResult{}, err
	}
	resumed, acts, err := workflow.Resume(def, before, workflow.ResumeOpts{From: from, Closed: closed})
	if err != nil {
		return ChainResult{}, refuse("%v", err)
	}
	if len(merged) > 0 {
		if row, err = chainResumeAddMembers(ctx, rt, tx, row, def, snap); err != nil {
			return ChainResult{}, snap.restore(tx, err)
		}
	}
	for _, act := range acts {
		if act.Kind != workflow.ActionSend {
			continue
		}
		if name, merr := chainFlowMemberName(tx, row, act.Actor); merr == nil {
			if rerr := resumeOpenRoundRefusal(tx, row, name); rerr != nil {
				return ChainResult{}, snap.restore(tx, rerr)
			}
		}
		break
	}
	if err := chainResumeCommitRow(tx, &row, def, resumed, rt.Now().UTC()); err != nil {
		return ChainResult{}, snap.restore(tx, err)
	}
	if chainResumePostWriteHook != nil {
		if err := chainResumePostWriteHook(tx, row); err != nil {
			return ChainResult{}, snap.restore(tx, err)
		}
	}
	gateChanged := chainResumeChangesGate(opts)
	for _, act := range acts {
		if err := chainResumeRunAction(ctx, rt, tx, row, def, before, &resumed, act, gateChanged); err != nil {
			return ChainResult{}, snap.restore(tx, err)
		}
	}
	if err := confirmPendingChainDeliveries(tx, toConfirm); err != nil {
		return ChainResult{}, snap.restore(tx, err)
	}
	members, err := chainFlowStoredMembers(tx, row)
	if err != nil {
		return ChainResult{}, snap.restore(tx, err)
	}
	if current, cerr := tx.Chain(opts.Name); cerr == nil {
		row = current
	}
	if err := chainForkParentReopen(rt, tx, row); err != nil {
		return ChainResult{}, snap.restore(tx, err)
	}
	return ChainResult{Chain: row, Members: members, Plans: len(before.Iter["plans"].Items)}, nil
}

// chainResumeFlagParams translates a resume's old flags into workflow params,
// the same slots a start's flags fill. A flag the workflow has no slot for is
// refused, so a human never believes a setting took effect when it did not.
func chainResumeFlagParams(def workflow.Definition, rt Runtime, opts ResumeOptions) (map[string]string, error) {
	out := map[string]string{}
	add := func(flag, param, value string) error {
		if !hasChainParam(def, param) {
			return refuse("%s: workflow %q has no param %q", flag, def.Name, param)
		}
		out[param] = value
		return nil
	}
	if opts.MaxCorrections != nil {
		if err := add("--max-corrections", "max_corrections", strconv.Itoa(*opts.MaxCorrections)); err != nil {
			return nil, err
		}
	}
	if opts.Regate != nil {
		if err := add("--regate", "regate", strconv.Itoa(*opts.Regate)); err != nil {
			return nil, err
		}
	}
	if opts.Security != nil {
		if err := add("--security/--no-security", "scan", strconv.FormatBool(*opts.Security)); err != nil {
			return nil, err
		}
	}
	for _, f := range []struct{ flag, param, value string }{
		{"--reviewer-actor", "reviewer", opts.ReviewerActor},
		{"--planner-actor", "planner", opts.PlannerActor},
		{"--security-actor", "security", opts.SecurityActor},
	} {
		if f.value == "" {
			continue
		}
		if err := add(f.flag, f.param, f.value); err != nil {
			return nil, err
		}
	}
	if opts.Gate != "" || opts.NoGate {
		// The gate flags win over every lower tier, so a resume reads no
		// repository's settings here.
		gate := resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, roleChecks(rt.RoleRegistry(), "builder"), RepoSettings{}).Command
		if err := add("--gate/--no-gate", "gate", gate); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// chainResumeClosed builds the step_closed a resume feeds when the run step the
// chain awaits has a newer closed round on its member: a round sent while the
// chain was halted or stopped, which the engine never saw. It rebuilds the
// event through the same close path a live close uses, reading the round's
// stored report entry and its own stream. No newer round means no event, and
// the resume re-runs the step.
//
// A newer round whose report file is not on disk routes nothing either: a stop
// close records the round as stopped without a report, so that round is already
// closed as stopped and there is no body to review. Such a round leaves no event
// and the resume re-runs the step; only a store failure other than the missing
// report is an error.
func chainResumeClosed(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, st workflow.State) (*workflow.Event, error) {
	if st.Awaiting.Member == "" || st.Awaiting.Round <= 0 {
		return nil, nil
	}
	member, err := chainFlowMemberName(tx, c, st.Awaiting.Member)
	if err != nil {
		return nil, err
	}
	newest := memberNewestClosedRound(tx, member)
	if newest <= st.Awaiting.Round {
		return nil, nil
	}
	entry, ok := memberReportEntry(tx, member, newest)
	if !ok {
		return nil, nil
	}
	body, err := rt.Store.ReadFile(entry.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b, err := tx.Load(member)
	if err != nil {
		return nil, err
	}
	// The close path reads the round a close carries, so the binding is handed
	// the round that closed rather than the round the pull has since advanced
	// it to: a reader's block lives in its stream, and the artifacts belong to
	// the closed round.
	b.Round = newest
	ev, err := chainEventFromCloseWF(ctx, rt, tx, c, b, chainCloseWF{
		Body: body, Path: entry.Path, Outcome: entry.Outcome, Round: newest,
	})
	if err != nil {
		return nil, err
	}
	return &ev, nil
}

// memberReportEntry is the report entry that closed a member's round.
func memberReportEntry(tx *store.Tx, name string, round int) (store.LogEntry, bool) {
	entries, err := tx.ReadLog(name)
	if err != nil {
		return store.LogEntry{}, false
	}
	var found store.LogEntry
	ok := false
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Kind == store.KindReport && e.Round == round {
			found, ok = e, true
		}
	}
	return found, ok
}

// chainResumeRunAction performs one action a resume produced. A send that
// re-runs the very round the chain was stopped on re-hands that round's own
// staged prompt, so the member sees the bytes it was already given; anything
// else is the engine's ordinary action. gateChanged says the resume replaced
// the check, so a stale repair prompt is walked back to the step's own seed.
func chainResumeRunAction(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State, next *workflow.State, act workflow.Action, gateChanged bool) error {
	if act.Kind == workflow.ActionSend {
		if name, text, ok := chainResumeStagedText(rt, tx, c, before, act, gateChanged); ok {
			ev := workflow.Event{Kind: workflow.EventNeedsYou, Step: next.At}
			return chainFlowSendText(ctx, rt, tx, c, def, before, next, ev, act, name, text)
		}
	}
	return chainRunAction(ctx, rt, tx, c, def, before, next, workflow.Event{Kind: workflow.EventNeedsYou, Step: next.At}, act)
}

// chainResumeStagedText returns the staged prompt a resume re-sends: the round
// the state awaited, when the action re-runs that same member's step and the
// round's staged file is still there. A replaced check walks back past any
// repair prompts, because their bytes name the check that failed; where that
// walk ends on a prompt that is not a repair plan there is nothing to re-send.
func chainResumeStagedText(rt Runtime, tx *store.Tx, c db.ChainRow, before workflow.State, act workflow.Action, gateChanged bool) (string, string, bool) {
	if before.Awaiting.Member == "" || before.Awaiting.Round <= 0 || act.Actor != before.Awaiting.Member {
		return "", "", false
	}
	name, err := chainFlowMemberName(tx, c, act.Actor)
	if err != nil {
		return "", "", false
	}
	round := before.Awaiting.Round
	if gateChanged {
		for round > 1 {
			body, berr := rt.Store.ReadFile(rt.Store.PromptPath(name, round))
			if berr != nil || !isRepairPlan(string(body)) {
				break
			}
			round--
		}
	}
	body, err := rt.Store.ReadFile(rt.Store.PromptPath(name, round))
	if err != nil {
		return "", "", false
	}
	// A walk-back that left the awaited round and stopped on a prompt that is
	// not a repair plan has found the build plan or a seed of some other step;
	// handing it over as this step's own would run the wrong work, so the engine
	// renders the step's seed fresh instead.
	if round != before.Awaiting.Round && !isRepairPlan(string(body)) {
		return "", "", false
	}
	return name, string(body), true
}

// chainResumeAddMembers creates the members a resume's params brought in: the
// actors of run steps the new definition reaches that have no chain_member row
// yet, and swaps members whose actor differs.
func chainResumeAddMembers(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, snap *chainResumeSnapshot) (db.ChainRow, error) {
	return chainResumeSyncMembers(ctx, rt, tx, c, def, snap)
}

// chainFlowStoredMembers loads a workflow chain's member bindings in
// chain_member order.
func chainFlowStoredMembers(tx *store.Tx, c db.ChainRow) ([]store.Binding, error) {
	rows, err := chainFlowMembers(tx, c)
	if err != nil {
		return nil, err
	}
	out := make([]store.Binding, 0, len(rows))
	for _, m := range rows {
		b, lerr := tx.Load(m.Binding)
		if lerr != nil {
			return nil, lerr
		}
		out = append(out, b)
	}
	return out, nil
}
