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
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainResumePostWriteHook is nil outside tests; a test sets it to fail a
// resume after its first write, reaching the undo path.
var chainResumePostWriteHook func(tx *store.Tx, c db.ChainRow) error

// chainResumeWorkflow continues a halted or stopped chain: it re-enters the
// engine step -- or the one --from names -- applies --param and the old flags
// to the stored definition, creates the members the params brought in, and
// runs the resumed action.
func chainResumeWorkflow(ctx context.Context, rt Runtime, c db.ChainRow, opts ResumeOptions) (ChainResult, error) {
	def, before, from, set, merged, err := chainResumePreflight(ctx, rt, c, opts)
	if err != nil {
		return ChainResult{}, err
	}
	out, err := chainResumeCommitWorkflow(ctx, rt, opts, def, before, from, set, merged)
	if err != nil {
		return ChainResult{}, err
	}
	return chainResumePostGate(ctx, rt, out, opts)
}

// chainResumePreflight runs every refusal and resolution a resume needs before
// writing: the gates, the row's definition and state, the settings, the
// merged params, and the reroute. Nothing is written.
func chainResumePreflight(ctx context.Context, rt Runtime, c db.ChainRow, opts ResumeOptions) (workflow.Definition, workflow.State, string, chain.Settings, map[string]string, error) {
	fail := func(err error) (workflow.Definition, workflow.State, string, chain.Settings, map[string]string, error) {
		return workflow.Definition{}, workflow.State{}, "", chain.Settings{}, nil, err
	}
	if err := resumeRefusal(c); err != nil {
		return fail(err)
	}
	// A child's resume needs its parent able to follow it back into the fork,
	// so the parent is asked first and nothing is written on a refusal.
	if err := chainForkParentGate(rt, c); err != nil {
		return fail(err)
	}
	// A gate change on a server-run writer must reach that server's binding, so
	// it is refused where the server cannot take it. The delivering push waits
	// for every other refusal below to clear.
	if chainResumeChangesGate(opts) {
		if err := resumeRemoteGateRefusal(ctx, rt, c); err != nil {
			return fail(err)
		}
	}
	// A round the halt left open -- no report, no close -- would otherwise be
	// refused for that open round. A resume is the human ending it, so it
	// closes the way a stop does and the resume can open the next one.
	if err := closeDeadMemberRound(ctx, rt, c); err != nil {
		return fail(err)
	}
	// The row's definition and state, migrated onto the engine when the row
	// predates it, so a resume never answers internal on a human-readable row.
	c, def, before, err := chainResumeWorkflowRow(rt, c)
	if err != nil {
		return fail(err)
	}
	set, err := resumeSettings(rt, c, opts)
	if err != nil {
		return fail(err)
	}
	// A parent on a fork cannot re-enter the fork step: entering it starts the
	// children a second time. So a fork with a child still standing is refused
	// here, before anything is written and before the end delivery is
	// superseded: the human resumes the child instead.
	if err := chainForkResumeRefusal(c, before); err != nil {
		return fail(err)
	}
	// The old flags fill the params exactly as a start's do, so a resume can
	// replace the check or the budgets the chain runs under.
	flagParams, err := chainResumeFlagParams(def, rt, opts)
	if err != nil {
		return fail(err)
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
			return fail(refusal.New("%v", err))
		}
		given := workflow.Given{Plans: len(before.Iter["plans"].Items) > 0}
		if err := chainValidateWorkflow(rt, def, given); err != nil {
			return fail(err)
		}
	}
	// A resume replacing the check on a zero-budget repair-arm halt goes back
	// to that arm's check, so the check runs again under the new command. The
	// refusal lands here, before the delivery below is superseded and before
	// the lock writes anything.
	from, err := chainResumeReroute(def, before, opts)
	if err != nil {
		return fail(err)
	}
	// A human has looked at the plan: every budget starts fresh, as a resume
	// resets the correction count on the fixed state machine.
	before.Visits = map[string]int{}
	return def, before, from, set, merged, nil
}

// chainResumeCommitWorkflow applies the resumed engine step under the state
// lock.
func chainResumeCommitWorkflow(ctx context.Context, rt Runtime, opts ResumeOptions, def workflow.Definition, before workflow.State, from string, set chain.Settings, merged map[string]string) (ChainResult, error) {
	var out ChainResult
	err := rt.Store.WithLock(func(tx *store.Tx) error {
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
	return out, nil
}

// chainResumePostGate answers a still-halted resume and pushes an owed gate
// change: the halt is the answer, so it is refused, not reported as an
// internal fault, and the push waits until the resume committed.
func chainResumePostGate(ctx context.Context, rt Runtime, out ChainResult, opts ResumeOptions) (ChainResult, error) {
	// A resume whose send could not start halts the chain, as the fixed state
	// machine's resume returns the send's own error. The CLI reads a refusal
	// as exit 2 with the reason to act on, and an internal as "send this to
	// bugreport" on a human-readable row.
	if out.Chain.Status == string(workflow.StatusHalted) {
		return out, refusal.New("chain %s is still halted: %s", out.Chain.Name, out.Chain.Reason)
	}
	// The resume committed and nothing refused it, so the gate change a placed
	// writer's server is owed can go out now. No later abort of this resume
	// can leave the server ahead of a chain already carrying the new gate.
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

// chainResumePrepared is what the locked resume carries between its phases:
// the row, the resumed engine state with its actions, and the undo facts.
type chainResumePrepared struct {
	row       db.ChainRow
	resumed   workflow.State
	acts      []workflow.Action
	snap      *chainResumeSnapshot
	toConfirm []pendingDelivery
}

func chainResumeLocked(ctx context.Context, rt Runtime, tx *store.Tx, opts ResumeOptions, def workflow.Definition, before workflow.State, from string, set chain.Settings, merged map[string]string) (ChainResult, error) {
	prep, handled, res, err := chainResumePrepareLocked(ctx, rt, tx, opts, def, before, from, set, merged)
	if err != nil {
		return ChainResult{}, err
	}
	if handled {
		return res, nil
	}
	if prep, err = chainResumeCommitLocked(ctx, rt, tx, prep, def, before, set, merged); err != nil {
		return ChainResult{}, err
	}
	return chainResumeApplyActionsLocked(ctx, rt, tx, opts, prep, def, before)
}

// chainResumePrepareLocked loads the row and resolves the resumed engine
// state, still inside the caller's lock. Nothing is written yet, so its
// errors return plain.
func chainResumePrepareLocked(ctx context.Context, rt Runtime, tx *store.Tx, opts ResumeOptions, def workflow.Definition, before workflow.State, from string, set chain.Settings, merged map[string]string) (chainResumePrepared, bool, ChainResult, error) {
	fail := func(err error) (chainResumePrepared, bool, ChainResult, error) {
		return chainResumePrepared{}, false, ChainResult{}, err
	}
	row, err := tx.Chain(opts.Name)
	if err != nil {
		return fail(err)
	}
	if err := resumeRefusal(row); err != nil {
		return fail(err)
	}
	if handled, res, err := chainForkResumeParent(ctx, rt, tx, row, def, before); err != nil {
		return fail(err)
	} else if handled {
		return chainResumePrepared{}, true, res, nil
	}
	snap, err := newChainResumeSnapshot(tx, row)
	if err != nil {
		return fail(err)
	}
	toConfirm, err := collectPendingChainDeliveries(tx, row)
	if err != nil {
		return fail(err)
	}
	settingsJSON, err := json.Marshal(set)
	if err != nil {
		return fail(fmt.Errorf("encode chain %s settings: %w", row.Name, err))
	}
	row.SettingsJSON = settingsJSON
	closed, err := chainResumeClosed(ctx, rt, tx, row, before)
	if err != nil {
		return fail(err)
	}
	sameRound := false
	if closed == nil && opts.From == "" {
		if closed, err = chainResumeDirtyRecovery(ctx, rt, tx, row, before); err != nil {
			return fail(err)
		}
		sameRound = closed != nil
	}
	resumed, acts, err := workflow.Resume(def, before, workflow.ResumeOpts{From: from, Closed: closed, SameRound: sameRound})
	if err != nil {
		return fail(refusal.New("%v", err))
	}
	return chainResumePrepared{row: row, resumed: resumed, acts: acts, snap: snap, toConfirm: toConfirm}, false, ChainResult{}, nil
}

// chainResumeCommitLocked writes the resumed row, still inside the caller's
// lock: the added members, the raised caps, and the committed engine state.
// Every error goes through snap.restore.
func chainResumeCommitLocked(ctx context.Context, rt Runtime, tx *store.Tx, prep chainResumePrepared, def workflow.Definition, before workflow.State, set chain.Settings, merged map[string]string) (chainResumePrepared, error) {
	row := prep.row
	if len(merged) > 0 {
		var err error
		if row, err = chainResumeAddMembers(ctx, rt, tx, row, def, prep.snap); err != nil {
			return chainResumePrepared{}, prep.snap.restore(tx, err)
		}
	}
	if err := chainResumeRaiseCaps(tx, row, len(before.Iter["plans"].Items), set); err != nil {
		return chainResumePrepared{}, prep.snap.restore(tx, err)
	}
	for _, act := range prep.acts {
		if act.Kind != workflow.ActionSend {
			continue
		}
		if name, merr := chainFlowMemberName(tx, row, act.Actor); merr == nil {
			if rerr := resumeOpenRoundRefusal(tx, row, name); rerr != nil {
				return chainResumePrepared{}, prep.snap.restore(tx, rerr)
			}
		}
		break
	}
	if err := chainResumeCommitRow(tx, &row, def, prep.resumed, rt.Now().UTC()); err != nil {
		return chainResumePrepared{}, prep.snap.restore(tx, err)
	}
	if chainResumePostWriteHook != nil {
		if err := chainResumePostWriteHook(tx, row); err != nil {
			return chainResumePrepared{}, prep.snap.restore(tx, err)
		}
	}
	prep.row = row
	return prep, nil
}

// chainResumeApplyActionsLocked runs the resume's actions and settles its
// deliveries, still inside the caller's lock. Every error goes through
// snap.restore.
func chainResumeApplyActionsLocked(ctx context.Context, rt Runtime, tx *store.Tx, opts ResumeOptions, prep chainResumePrepared, def workflow.Definition, before workflow.State) (ChainResult, error) {
	row := prep.row
	resumed := prep.resumed
	gateChanged := chainResumeChangesGate(opts)
	for _, act := range prep.acts {
		if err := chainResumeRunAction(ctx, rt, tx, row, def, before, &resumed, act, gateChanged); err != nil {
			return ChainResult{}, prep.snap.restore(tx, err)
		}
	}
	if err := confirmPendingChainDeliveries(tx, prep.toConfirm); err != nil {
		return ChainResult{}, prep.snap.restore(tx, err)
	}
	members, err := chainFlowStoredMembers(tx, row)
	if err != nil {
		return ChainResult{}, prep.snap.restore(tx, err)
	}
	if current, cerr := tx.Chain(opts.Name); cerr == nil {
		row = current
	}
	if err := chainForkParentReopen(rt, tx, row); err != nil {
		return ChainResult{}, prep.snap.restore(tx, err)
	}
	return ChainResult{Chain: row, Members: members, Plans: len(before.Iter["plans"].Items)}, nil
}

// chainResumeFlagParams translates a resume's old flags into workflow params,
// the same slots a start's flags fill. A flag the workflow has no slot for is
// refused, so no setting silently takes no effect.
func chainResumeFlagParams(def workflow.Definition, rt Runtime, opts ResumeOptions) (map[string]string, error) {
	out := map[string]string{}
	add := func(flag, param, value string) error {
		if !chain.HasParam(def, param) {
			return refusal.New("%s: workflow %q has no param %q", flag, def.Name, param)
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
		// The gate flags win over every lower tier, so no repository's
		// settings are read here.
		gate := resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, resolve.RoleChecks(rt.RoleRegistry(), "builder"), RepoSettings{}).Command
		if err := add("--gate/--no-gate", "gate", gate); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// chainResumeClosed builds the step_closed a resume feeds when the awaited run
// step has a newer closed round on its member: a round sent while the chain
// was down, which the engine never saw. It rebuilds the event through the live
// close path, reading the round's stored report entry and its own stream. No
// newer round means no event, and the resume re-runs the step.
//
// A newer round whose report file is not on disk routes nothing either: a
// stop close records the round as stopped without a report, so it is already
// closed as stopped with no body to review. Only a store failure other than
// the missing report is an error.
func chainResumeClosed(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, st workflow.State) (*workflow.Event, error) {
	if st.Awaiting.Member == "" || st.Awaiting.Round <= 0 {
		return nil, nil
	}
	member, err := chainFlowMemberName(tx, c, st.Awaiting.Member)
	if err != nil {
		return nil, err
	}
	newest := chain.NewestClosedRound(tx, member)
	if newest <= st.Awaiting.Round {
		return nil, nil
	}
	return chainResumeRoundClose(ctx, rt, tx, c, member, newest)
}

// chainResumeRoundClose rebuilds the step_closed of one closed member round
// from its stored report entry; a round with no report on disk gives no event.
func chainResumeRoundClose(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, member string, newest int) (*workflow.Event, error) {
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
	// The close path reads the carried round, so the binding is handed the
	// round that closed rather than the pull-advanced round: a reader's block
	// lives in its stream, and the artifacts belong to the closed round.
	b.Round = newest
	ev, err := chainEventFromCloseWF(ctx, rt, tx, c, b, chainCloseWF{
		Body: body, Path: entry.Path, Outcome: entry.Outcome, Round: newest,
	})
	if err != nil {
		return nil, err
	}
	return &ev, nil
}

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

// chainResumeRunAction performs one action a resume produced. A send re-running
// the stopped-on round re-hands that round's staged prompt, so the member sees
// the bytes already given; anything else is the engine's ordinary action.
// gateChanged walks a stale repair prompt back to the step's own seed.
func chainResumeRunAction(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State, next *workflow.State, act workflow.Action, gateChanged bool) error {
	if act.Kind == workflow.ActionSend {
		if name, text, ok := chainResumeStagedText(rt, tx, c, before, act, gateChanged); ok {
			ev := workflow.Event{Kind: workflow.EventNeedsYou, Step: next.At}
			return chainFlowSendText(ctx, rt, tx, c, def, before, next, ev, act, name, text)
		}
	}
	return chainRunAction(ctx, rt, tx, c, def, before, next, workflow.Event{Kind: workflow.EventNeedsYou, Step: next.At}, act)
}

// chainResumeStagedText returns the staged prompt a resume re-sends: the
// awaited round, when the action re-runs that member's step and the staged
// file is still there. A replaced check walks back past any repair prompts,
// whose bytes name the failed check; a walk ending on a non-repair prompt has
// nothing to re-send.
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
	// A walk-back off the awaited round onto a non-repair prompt found the
	// build plan or another step's seed; handing it over would run the wrong
	// work, so the engine renders the step's seed fresh instead.
	if round != before.Awaiting.Round && !isRepairPlan(string(body)) {
		return "", "", false
	}
	return name, string(body), true
}

// chainResumeAddMembers creates the members a resume's params brought in,
// and swaps members whose actor differs.
func chainResumeAddMembers(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, snap *chainResumeSnapshot) (db.ChainRow, error) {
	return chainResumeSyncMembers(ctx, rt, tx, c, def, snap)
}

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
