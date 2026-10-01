package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainResumeWorkflow continues a halted or stopped workflow chain: it
// re-enters the step the engine is on -- or the one --from names -- applies
// --param and the old flags to the stored definition, creates the members the
// params brought in, and runs the resumed action.
func chainResumeWorkflow(ctx context.Context, rt Runtime, c db.ChainRow, opts ResumeOptions) (ChainResult, error) {
	if err := resumeRefusal(c); err != nil {
		return ChainResult{}, err
	}
	// A workflow chain whose awaited member's round died without a close --
	// its process gone and no report -- would otherwise be refused for an open
	// round. Close it the way a stop does, so the resume can open the next one.
	if err := closeDeadMemberRound(ctx, rt, c); err != nil {
		return ChainResult{}, err
	}
	def, err := chainWorkflowDef(c)
	if err != nil {
		return ChainResult{}, err
	}
	set, err := resumeSettings(rt, c, opts)
	if err != nil {
		return ChainResult{}, err
	}
	before, err := chainWorkflowState(c)
	if err != nil {
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
	// A human has looked at the plan: every budget starts fresh, exactly as a
	// resume resets the correction count on the fixed state machine.
	before.Visits = map[string]int{}
	if err := supersedeChainDelivery(rt, c); err != nil {
		return ChainResult{}, err
	}

	var out ChainResult
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain(opts.Name)
		if err != nil {
			return err
		}
		if err := resumeRefusal(row); err != nil {
			return err
		}
		if len(merged) > 0 {
			if row, err = chainResumeAddMembers(ctx, rt, tx, row, def); err != nil {
				return err
			}
		}
		settingsJSON, err := json.Marshal(set)
		if err != nil {
			return fmt.Errorf("encode chain %s settings: %w", row.Name, err)
		}
		row.SettingsJSON = settingsJSON
		closed, err := chainResumeClosed(rt, tx, row, before)
		if err != nil {
			return err
		}
		resumed, acts, err := workflow.Resume(def, before, workflow.ResumeOpts{From: opts.From, Closed: closed})
		if err != nil {
			return refuse("%v", err)
		}
		// The target member's own round must be closed before anything is
		// written: starting a new one would overwrite the round in flight.
		for _, act := range acts {
			if act.Kind != workflow.ActionSend {
				continue
			}
			if name, merr := chainFlowMemberName(tx, row, act.Actor); merr == nil {
				if rerr := resumeOpenRoundRefusal(tx, row, name); rerr != nil {
					return rerr
				}
			}
			break
		}
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
		applyChainLegacy(&row, def, resumed)
		row.UpdatedAt = rt.Now().UTC()
		if err := tx.ChainPut(row); err != nil {
			return err
		}
		gateChanged := opts.Gate != "" || opts.NoGate
		for _, act := range acts {
			if err := chainResumeRunAction(ctx, rt, tx, row, def, before, &resumed, act, gateChanged); err != nil {
				return err
			}
		}
		members, err := chainFlowStoredMembers(tx, row)
		if err != nil {
			return err
		}
		// The run actions opened the resumed round; the result carries the row
		// as the store now holds it.
		if current, cerr := tx.Chain(opts.Name); cerr == nil {
			row = current
		}
		out = ChainResult{Chain: row, Members: members, Plans: len(before.Iter["plans"].Items)}
		return nil
	})
	if err != nil {
		return ChainResult{}, err
	}
	// A resume whose send could not start halts the chain, exactly as the fixed
	// state machine's resume returns the send's own error.
	if out.Chain.Status == string(workflow.StatusHalted) {
		return out, fmt.Errorf("chain %s: %s", out.Chain.Name, out.Chain.Reason)
	}
	return out, nil
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
		gate := resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, roleChecks(rt.RoleRegistry(), "builder"))
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
func chainResumeClosed(rt Runtime, tx *store.Tx, c db.ChainRow, st workflow.State) (*workflow.Event, error) {
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
	ev, err := chainEventFromCloseWF(rt, tx, c, b, chainCloseWF{
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
// repair prompts, because their bytes name the check that failed.
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
	return name, string(body), true
}

// chainResumeAddMembers creates the members a resume's params brought in: the
// actors of run steps the new definition reaches that have no chain_member row
// yet. Each is built the way a start builds a member, beside the chain's own
// builder, and its chain_member row is written with it.
func chainResumeAddMembers(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition) (db.ChainRow, error) {
	actors := rt.RoleRegistry().WorkflowActors()
	planned, err := chainMemberNames(c.Name, def, actors)
	if err != nil {
		return c, err
	}
	keeper := chainWriterKeeper(def, workflow.UsedActors(def), actors)
	members := chainWorkflowMembers(def, planned, keeper)
	rows, err := chainFlowMembers(tx, c)
	if err != nil {
		return c, err
	}
	have := make(map[string]bool, len(rows))
	for _, m := range rows {
		have[m.Binding] = true
	}
	var add []chainMember
	for _, m := range members {
		if !have[m.name] {
			add = append(add, m)
		}
	}
	if len(add) == 0 {
		return c, nil
	}
	builder, err := tx.Load(c.Builder)
	if err != nil {
		return c, fmt.Errorf("chain %s has no builder member to place the new members beside: %w", c.Name, err)
	}
	// A served chain's new member is built in the served shape, the server
	// picking its own candidate and tier, exactly as the start built the others.
	if builder.Owner != "" {
		return servedChainAddMembers(rt, tx, c, builder, add, len(rows))
	}
	set, err := resumeSettings(rt, c, ResumeOptions{})
	if err != nil {
		return c, err
	}
	resolutions, err := chainResolveActors(rt, add)
	if err != nil {
		return c, err
	}
	base := chainBase{
		cwd: builder.CWD, mastermind: builder.MasterMind, mastermindID: c.MasterMindID,
		repo: c.Repo, repoRef: builder.RepoRef, feature: c.Feature, ticket: c.Ticket,
		worktree: builder.CWD,
	}
	built, err := chainBuildMembers(ctx, rt, add, resolutions, base, set)
	if err != nil {
		return c, err
	}
	newRows := make([]db.ChainMemberRow, 0, len(built))
	for i, b := range built {
		if _, lerr := tx.Load(b.Name); lerr == nil {
			return c, fmt.Errorf("binding %q already exists: `relevo unbind %s` first", b.Name, b.Name)
		} else if !errors.Is(lerr, store.ErrNotFound) {
			return c, lerr
		}
		if err := tx.Save(b); err != nil {
			return c, err
		}
		newRows = append(newRows, db.ChainMemberRow{Binding: b.Name, Actor: b.Role, Seq: len(rows) + i})
		setChainMemberColumn(&c, add[i].part, b.Name)
	}
	if err := tx.ChainMembersPut(c.Name, newRows); err != nil {
		return c, err
	}
	c.UpdatedAt = rt.Now().UTC()
	return c, tx.ChainPut(c)
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
