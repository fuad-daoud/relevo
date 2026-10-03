package relevo

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// A chain that runs a user workflow stores its definition and its engine state
// on the row: a non-empty workflow column selects the engine path, an empty one
// the chain's own fixed state machine.

// chainWorkflowDef decodes a chain's stored workflow definition. The stored
// form is the workflow package's own JSON, so it is read back through its
// parser, which owns the step and target shapes.
func chainWorkflowDef(c db.ChainRow) (workflow.Definition, error) {
	if len(c.WorkflowJSON) == 0 {
		return workflow.Definition{}, fmt.Errorf("chain %s carries no workflow", c.Name)
	}
	def, err := workflow.Parse(c.WorkflowJSON)
	if err != nil {
		return workflow.Definition{}, fmt.Errorf("chain %s workflow: %w", c.Name, err)
	}
	return def, nil
}

// chainWorkflowState decodes a chain's stored engine state.
func chainWorkflowState(c db.ChainRow) (workflow.State, error) {
	var st workflow.State
	if len(c.StateJSON) == 0 {
		return st, fmt.Errorf("chain %s has no engine state", c.Name)
	}
	if err := json.Unmarshal(c.StateJSON, &st); err != nil {
		return workflow.State{}, fmt.Errorf("chain %s state: %w", c.Name, err)
	}
	return st, nil
}

// chainResumeWorkflowRow is the resume path's read of a chain row's engine
// definition and state. The sweep that migrates pre-engine rows runs at daemon
// start, so a verb can meet a row the sweep could not finish: it is halted with
// the reason the conversion named and carries neither a workflow nor a state.
// Reading such a row as a plain error left `chain --resume` answering internal
// on a chain the human can still look at.
//
// A row with no workflow is migrated here, through the same single-row
// conversion the sweep runs, so a chain the sweep simply has not reached yet
// resumes as any other. Whatever still lacks a workflow or a state is refused in
// the input class instead, naming the row's own reason and the command that
// reads it.
func chainResumeWorkflowRow(rt Runtime, c db.ChainRow) (db.ChainRow, workflow.Definition, workflow.State, error) {
	if len(c.WorkflowJSON) == 0 {
		fresh, err := convertLegacyChainRow(rt, c.Name)
		if err != nil {
			return c, workflow.Definition{}, workflow.State{}, err
		}
		c = fresh
	}
	def, derr := chainWorkflowDef(c)
	st, serr := chainWorkflowState(c)
	if derr == nil && serr == nil {
		return c, def, st, nil
	}
	why := derr
	if why == nil {
		why = serr
	}
	if c.Reason != "" {
		return c, workflow.Definition{}, workflow.State{}, refuse(
			"chain %s cannot be resumed: %v; its own reason reads %q -- `relevo status %s` shows it",
			c.Name, why, c.Reason, c.Name)
	}
	return c, workflow.Definition{}, workflow.State{}, refuse(
		"chain %s cannot be resumed: %v; this row predates the workflow engine and carries nothing to migrate -- `relevo status %s` shows what it holds",
		c.Name, why, c.Name)
}

// chainFlowMembers returns a chain's member rows, in creation order: the actors
// it runs and the bindings that run them.
func chainFlowMembers(tx *store.Tx, c db.ChainRow) ([]db.ChainMemberRow, error) {
	return tx.ChainMembers(c.Name)
}

// chainFlowMemberName returns the binding name that runs an actor on a chain.
func chainFlowMemberName(tx *store.Tx, c db.ChainRow, actor string) (string, error) {
	rows, err := chainFlowMembers(tx, c)
	if err != nil {
		return "", err
	}
	for _, m := range rows {
		if chainFlowActorMatch(m.Actor, actor) {
			return m.Binding, nil
		}
	}
	return "", fmt.Errorf("chain %s has no member for actor %q", c.Name, actor)
}

// chainFlowActor returns the actor the named binding runs on a chain.
func chainFlowActor(tx *store.Tx, c db.ChainRow, binding string) string {
	rows, err := chainFlowMembers(tx, c)
	if err != nil {
		return ""
	}
	for _, m := range rows {
		if m.Binding == binding {
			return chainFlowActorName(m.Actor)
		}
	}
	return ""
}

// chainFlowActorMatch reports whether a stored actor names the actor a
// workflow's run step asked for. A builder binding stores no role word, so an
// empty stored actor matches the builder.
func chainFlowActorMatch(stored, actor string) bool {
	return chainFlowActorName(stored) == actor
}

// chainFlowActorName is the actor a stored chain-member role names.
func chainFlowActorName(stored string) string {
	if stored == "" {
		return "builder"
	}
	return stored
}

// chainAdvance moves a workflow chain by one event: it runs the pure transition
// and performs every action it produced in the caller's critical section. A
// close that is not the awaited one -- or that arrives once the chain is
// terminal -- changes nothing and writes no trace row.
func chainAdvance(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, ev workflow.Event) error {
	def, err := chainWorkflowDef(c)
	if err != nil {
		return err
	}
	before, err := chainWorkflowState(c)
	if err != nil {
		return err
	}
	// The transition mutates the state's maps in place, so it runs on a clone:
	// before stays the state the trace row records, including its plan.
	next, acts := workflow.Next(def, cloneFlowState(before), ev)
	if len(acts) == 0 {
		return nil
	}
	for _, act := range acts {
		if err := chainRunAction(ctx, rt, tx, c, def, before, &next, ev, act); err != nil {
			return err
		}
		if chainFlowTerminal(next.Status) {
			return nil
		}
	}
	return nil
}

// chainFlowTerminal reports whether a run status ends the chain.
func chainFlowTerminal(st workflow.Status) bool {
	return st == workflow.StatusHalted || st == workflow.StatusStopped || st == workflow.StatusDone
}

// cloneFlowState copies a state's maps, so a transition can mutate the clone
// without touching the state a caller still reads.
func cloneFlowState(s workflow.State) workflow.State {
	out := s
	if s.Visits != nil {
		out.Visits = maps.Clone(s.Visits)
	}
	if s.Iter != nil {
		out.Iter = maps.Clone(s.Iter)
	}
	if s.Results != nil {
		out.Results = maps.Clone(s.Results)
	}
	return out
}

// chainRunAction performs one transition action in the caller's critical
// section. A send renders the seed and hands it to the member; a run_check
// starts one check run; finish, halt and stop end the chain.
func chainRunAction(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State, next *workflow.State, ev workflow.Event, act workflow.Action) error {
	switch act.Kind {
	case workflow.ActionSend:
		return chainFlowSend(ctx, rt, tx, c, def, before, next, ev, act)
	case workflow.ActionRunCheck:
		return chainFlowCheck(ctx, rt, tx, c, def, before, next, ev, act)
	case workflow.ActionFinish, workflow.ActionHalt, workflow.ActionStop:
		return chainTerminalWF(ctx, rt, tx, c, def, before, *next, ev, act)
	case workflow.ActionFork:
		return chainFlowFork(ctx, rt, tx, c, def, before, next, ev, act)
	case workflow.ActionMerge:
		return chainFlowMerge(ctx, rt, tx, c, def, before, next, ev, act)
	default:
		return fmt.Errorf("chain %s: unhandled action %q", c.Name, string(act.Kind))
	}
}

// chainFlowSend renders a run step's seed and starts the actor member's round.
// A member that cannot start halts the chain in the same critical section, with
// the member's own reason.
func chainFlowSend(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State, next *workflow.State, ev workflow.Event, act workflow.Action) error {
	name, err := chainFlowMemberName(tx, c, act.Actor)
	if err != nil {
		return err
	}
	text, err := chainRenderSeed(rt, tx, c, def, *next, act.Seed)
	if err != nil {
		return err
	}
	return chainFlowSendText(ctx, rt, tx, c, def, before, next, ev, act, name, text)
}

// chainFlowSendText starts a run step's member round with the text it is handed:
// the rendered seed for an ordinary send, the round's own staged prompt for a
// resume that re-sends a round already under way. A member that cannot start
// halts the chain in the same critical section, with the member's own reason.
func chainFlowSendText(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State, next *workflow.State, ev workflow.Event, act workflow.Action, name, text string) error {
	member, err := tx.Load(name)
	if err != nil {
		return err
	}
	sent, err := chainSendMember(ctx, rt, tx, member, text)
	if err != nil {
		next.Status = workflow.StatusHalted
		next.Reason = fmt.Sprintf("member %s could not start: %v", name, err)
		return chainTerminalWF(ctx, rt, tx, c, def, before, *next, ev, workflow.Action{Kind: workflow.ActionHalt, Reason: next.Reason})
	}
	next.Awaiting.Round = sent.Round
	// A resume's needs_you row names the round the resume sent, so the trace
	// records the round the member was handed.
	if ev.Kind == workflow.EventNeedsYou {
		ev.Round = sent.Round
	}
	// A send that opens a plan records the commit the plan began at, so the
	// plan's review can diff its whole span. The send that follows a next from
	// the plans for-each is the one that opens a plan on the engine path, and
	// so does the security phase's fix plan after the planner writes it.
	if chainFlowPlanStart(def, before, act.Step) {
		c.PlanStartCommit = sent.RoundBaselineHead
	}
	return chainSaveFlow(rt, tx, c, def, before, *next, ev, act, name)
}

// chainFixSeedName is the shipped seed the security phase's fix planner
// renders; the builder send that follows it opens a new plan scope.
const chainFixSeedName = "shipped:fix"

// chainFlowPlanStart reports whether a send opens a plan: its step is the
// target the plans for-each routes its next item to, or the send follows the
// fix planner that renders the shipped fix seed.
func chainFlowPlanStart(def workflow.Definition, before workflow.State, step string) bool {
	for _, s := range def.Steps {
		if s.ForEach != "plans" {
			continue
		}
		if t, ok := s.On["next"]; ok && t.Kind == workflow.TargetStep && t.Step == step {
			return true
		}
	}
	prev, ok := def.Steps[before.At]
	return ok && prev.Seed == chainFixSeedName
}

// chainFlowCheck starts one check run for a check step. A check whose writer
// member is placed on a server starts no local run: it answers from the pulled
// gate record on the writer's newest closed round, or waits for one. Every
// other check runs through the gate runner on the chain's tree, and its end
// feeds check_closed through the same driver.
func chainFlowCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State, next *workflow.State, ev workflow.Event, act workflow.Action) error {
	member, err := chainFlowWriterMember(tx, c)
	if err == nil {
		if b, lerr := tx.Load(member); lerr == nil && b.Builder.Remote() {
			return chainFlowPullCheck(ctx, rt, tx, c, def, before, next, ev, act, member)
		}
	}
	run, err := chainStartOrAdoptCheck(ctx, rt, tx, c, act.Step, act.Command)
	if err != nil {
		next.Status = workflow.StatusHalted
		next.Reason = fmt.Sprintf("check %s could not start: %v", act.Step, err)
		return chainTerminalWF(ctx, rt, tx, c, def, before, *next, ev, workflow.Action{Kind: workflow.ActionHalt, Reason: next.Reason})
	}
	next.Awaiting.Run = run
	return chainSaveFlow(rt, tx, c, def, before, *next, ev, act, member)
}

// chainRepeatRedCheck reports whether a red check's output repeats the previous
// red's: the state before the event carries that red's sealed log, and the two
// signatures agree. A green check, no previous red, or an unreadable log is not
// a repeat, so a repair is only ever skipped for a failure already seen.
//
// The previous red must belong to the for-each item the chain is on now: a
// plan's first red is never a repeat, however closely it resembles the red the
// plan before it saw.
func chainRepeatRedCheck(rt Runtime, c db.ChainRow, before workflow.State, step, log string) bool {
	prev, ok := before.Results[step]
	if !ok || prev.Status != chainCheckRed {
		return false
	}
	logs := prev.Artifacts["log"]
	if len(logs) == 0 || logs[0] == "" || log == "" {
		return false
	}
	if !chainRepeatSameItem(rt, c, before, step) {
		return false
	}
	sig := gateSignature(rt.Store.ReadFile, logs[0])
	return sig != "" && sig == gateSignature(rt.Store.ReadFile, log)
}

// chainRepeatSameItem reports whether the previous red of a check step closed
// under the for-each item the state is on. The trace row that recorded that
// close carries the item position as the legacy plan column, so comparing it
// with the state's own position keeps the repeat inside one repair loop.
func chainRepeatSameItem(rt Runtime, c db.ChainRow, before workflow.State, step string) bool {
	rows, err := rt.Store.ChainEvents(c.Name)
	if err != nil {
		return false
	}
	plan := flowPlanPos(before)
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Step != step {
			continue
		}
		ev, derr := workflow.DecodeEvent(rows[i].Event)
		if derr != nil || ev.Kind != workflow.EventCheckClosed {
			continue
		}
		return rows[i].Plan == plan
	}
	return false
}

// chainFlowPullCheck answers a placed writer's check from the gate record its
// newest closed round carries. No record yet leaves the chain awaiting the
// check, so a later tick answers it once the round is pulled.
func chainFlowPullCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State, next *workflow.State, ev workflow.Event, act workflow.Action, member string) error {
	round := memberNewestClosedRound(tx, member)
	rec, err := chainRoundGate(tx, member, round)
	if err != nil {
		return err
	}
	if rec == nil {
		return chainSaveFlow(rt, tx, c, def, before, *next, ev, act, member)
	}
	next.Awaiting.Run = round
	if err := chainSaveFlow(rt, tx, c, def, before, *next, ev, act, member); err != nil {
		return err
	}
	// The check closes against the state just written, so the row is re-read
	// rather than the stale value carried in from this call's caller.
	saved, err := tx.Chain(c.Name)
	if err != nil {
		return err
	}
	return chainAdvance(ctx, rt, tx, saved, workflow.Event{
		Kind: workflow.EventCheckClosed, Step: act.Step, Run: round,
		Result: chainGateResult(rec), Log: rec.LogPath,
		RepeatRed: chainRepeatRedCheck(rt, c, *next, act.Step, rec.LogPath),
	})
}

// chainFlowWriterMember names the chain's writer member whose tree a check
// runs on: the keeper writer, or the first member when the workflow has none.
func chainFlowWriterMember(tx *store.Tx, c db.ChainRow) (string, error) {
	if c.Builder != "" {
		return c.Builder, nil
	}
	rows, err := chainFlowMembers(tx, c)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("chain %s has no member", c.Name)
	}
	return rows[0].Binding, nil
}

// chainSaveFlow writes a workflow chain's state and its one trace row in a
// single database transaction. The trace names the step it was on and the
// closing member, and carries the encoded workflow event and action.
func chainSaveFlow(rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before, next workflow.State, ev workflow.Event, act workflow.Action, closing string) error {
	now := rt.Now().UTC()
	row := c
	row.Status = string(next.Status)
	row.Reason = next.Reason
	stateJSON, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("chain %s state: %w", c.Name, err)
	}
	row.StateJSON = stateJSON
	applyChainLegacy(&row, def, next)
	// A start's send has no event of its own: the row's state is the record,
	// exactly as the fixed state machine's start keeps no trace row. A fork
	// action writes its trace row so the trace records the fork.
	if ev.Kind == "" && act.Kind != workflow.ActionFork {
		return tx.ChainPut(row)
	}
	trace := db.ChainEventRow{
		TS: now, Step: flowTraceStep(before, ev), Member: closing,
		Round: ev.Round, Plan: flowPlanPos(next),
		Event: workflow.EncodeEvent(ev), Action: workflow.EncodeAction(act), Reason: act.Reason,
	}
	return tx.ChainSaveWithEvent(row, trace)
}

// applyChainLegacy projects a workflow state onto the row's old columns, so
// every surface that still reads them sees the chain's position.
func applyChainLegacy(row *db.ChainRow, def workflow.Definition, st workflow.State) {
	f := workflow.LegacyView(def, st)
	row.Phase = f.Phase
	row.Step = f.Step
	row.Plan = f.Plan
	row.Plans = f.Plans
	row.Corrections = f.Corrections
	row.AwaitingMember = f.AwaitingMember
	row.AwaitingRound = f.AwaitingRound
	// A workflow may take no plans; the row's own validity asks for at least
	// one, so the columns read one plan even then.
	if row.Plans < 1 {
		row.Plans = 1
		row.Plan = 1
	}
}

// flowTraceStep is the step a trace row names: the step whose close or action
// the row records.
func flowTraceStep(before workflow.State, ev workflow.Event) string {
	if ev.Step != "" {
		return ev.Step
	}
	return before.At
}

// flowPlanPos is a state's current plan position for the trace's legacy plan
// column: the plan the transition reached. An exhausted walk holds index -1,
// the same as one that has not started, so Done reads the last plan.
func flowPlanPos(st workflow.State) int {
	it := st.Iter["plans"]
	if it.Done && len(it.Items) > 0 {
		return len(it.Items)
	}
	plan := it.Index + 1
	if plan < 1 {
		return 1
	}
	return plan
}
