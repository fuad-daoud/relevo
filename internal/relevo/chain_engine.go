package relevo

import (
	"context"
	"encoding/json"
	"fmt"

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
	next, acts := workflow.Next(def, before, ev)
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
		return fmt.Errorf("chain %s: fork steps are not run by this engine", c.Name)
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
	return chainSaveFlow(rt, tx, c, def, before, *next, ev, act, name)
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
	run, err := chainStartCheck(ctx, rt, tx, c, act.Step, act.Command)
	if err != nil {
		next.Status = workflow.StatusHalted
		next.Reason = fmt.Sprintf("check %s could not start: %v", act.Step, err)
		return chainTerminalWF(ctx, rt, tx, c, def, before, *next, ev, workflow.Action{Kind: workflow.ActionHalt, Reason: next.Reason})
	}
	next.Awaiting.Run = run
	return chainSaveFlow(rt, tx, c, def, before, *next, ev, act, member)
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
	// exactly as the fixed state machine's start keeps no trace row.
	if ev.Kind == "" {
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
// column.
func flowPlanPos(st workflow.State) int {
	plan := st.Iter["plans"].Index + 1
	if plan < 1 {
		return 1
	}
	return plan
}
