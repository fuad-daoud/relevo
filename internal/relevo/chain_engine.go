package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// A chain that runs a user workflow stores its definition and engine state
// on the row: a non-empty workflow column selects the engine path.

// chainResumeWorkflowRow is the resume path's read of that definition and
// state. A row the migration sweep has not reached yet converts here, through
// the same single-row conversion; whatever still lacks a workflow or a state
// is refused in the input class, naming the row's own reason and the command
// that reads it.
func chainResumeWorkflowRow(rt Runtime, c db.ChainRow) (db.ChainRow, workflow.Definition, workflow.State, error) {
	if len(c.WorkflowJSON) == 0 {
		fresh, err := convertLegacyChainRow(rt, c.Name)
		if err != nil {
			return c, workflow.Definition{}, workflow.State{}, err
		}
		c = fresh
	}
	def, derr := chain.WorkflowDef(c)
	st, serr := chain.WorkflowState(c)
	if derr == nil && serr == nil {
		return c, def, st, nil
	}
	why := derr
	if why == nil {
		why = serr
	}
	if c.Reason != "" {
		return c, workflow.Definition{}, workflow.State{}, refusal.New(
			"chain %s cannot be resumed: %v; its own reason reads %q -- `relevo status %s` shows it",
			c.Name, why, c.Reason, c.Name)
	}
	return c, workflow.Definition{}, workflow.State{}, refusal.New(
		"chain %s cannot be resumed: %v; this row predates the workflow engine and carries nothing to migrate -- `relevo status %s` shows what it holds",
		c.Name, why, c.Name)
}

// chainFlowMembers returns a chain's member rows, in creation order.
func chainFlowMembers(tx *store.Tx, c db.ChainRow) ([]db.ChainMemberRow, error) {
	return tx.ChainMembers(c.Name)
}

func chainFlowMemberName(tx *store.Tx, c db.ChainRow, actor string) (string, error) {
	rows, err := chainFlowMembers(tx, c)
	if err != nil {
		return "", err
	}
	for _, m := range rows {
		if chain.FlowActorMatch(m.Actor, actor) {
			return m.Binding, nil
		}
	}
	return "", fmt.Errorf("chain %s has no member for actor %q", c.Name, actor)
}

func chainFlowActor(tx *store.Tx, c db.ChainRow, binding string) string {
	rows, err := chainFlowMembers(tx, c)
	if err != nil {
		return ""
	}
	for _, m := range rows {
		if m.Binding == binding {
			return chain.FlowActorName(m.Actor)
		}
	}
	return ""
}

// chainAdvance moves a workflow chain by one event, in the caller's critical
// section. A close that is not the awaited one changes nothing and writes no
// trace row.
func chainAdvance(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, ev workflow.Event) error {
	def, err := chain.WorkflowDef(c)
	if err != nil {
		return err
	}
	before, err := chain.WorkflowState(c)
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
// section.
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

// chainFlowSend renders a run step's seed and starts the member's round. A
// member that cannot start halts the chain with its own reason.
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

// chainFlowSendText starts a member round with the text it is handed: the
// rendered seed, or the staged prompt for a resume re-sending a round under
// way. A member that cannot start halts the chain with its own reason.
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
	// A send that opens a plan records the commit it began at, so the plan's
	// review can diff its whole span.
	if chainFlowPlanStart(def, before, act.Step) {
		c.PlanStartCommit = sent.RoundBaselineHead
	}
	return chainSaveFlow(rt, tx, c, def, before, *next, ev, act, name)
}

// chainFixSeedName is the shipped seed the fix planner renders; the send
// after it opens a new plan scope.
const chainFixSeedName = "shipped:fix"

// chainFlowPlanStart reports whether a send opens a plan.
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

func chainNextPlacedCheckRun(rt Runtime, c db.ChainRow) int {
	rows, err := rt.Store.ChainEvents(c.Name)
	if err != nil {
		return 1
	}
	maxRun := 0
	for _, r := range rows {
		ev, derr := workflow.DecodeEvent(r.Event)
		if derr == nil && ev.Kind == workflow.EventCheckClosed {
			if ev.Run > maxRun {
				maxRun = ev.Run
			}
		}
	}
	return maxRun + 1
}

// chainCheckID formats the client-minted id for a check run.
func chainCheckID(run int) string {
	return fmt.Sprintf("chk-%d", run)
}

// chainFlowCheck starts one check run for a check step: via the check route
// on a server with check support, from the writer's newest closed round's
// gate record without it, and through the gate runner locally.
func chainFlowCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State, next *workflow.State, ev workflow.Event, act workflow.Action) error {
	member, err := chainFlowWriterMember(tx, c)
	if err == nil {
		if b, lerr := tx.Load(member); lerr == nil && b.Builder.Remote() {
			if rt.Remote != nil {
				who, werr := rt.Remote.WhoAmI(ctx, b.Builder.Server)
				if werr == nil && remote.HasFeature(who, remote.FeatureCheck) {
					run := chainNextPlacedCheckRun(rt, c)
					req := remote.CreateCheckRequest{
						ID:      chainCheckID(run),
						Command: act.Command,
						Step:    act.Step,
					}
					if _, cerr := rt.Remote.CreateCheck(ctx, b.Builder.Server, b.Name, req); cerr != nil {
						next.Status = workflow.StatusHalted
						next.Reason = chainCheckStartReason(act.Step, cerr)
						return chainTerminalWF(ctx, rt, tx, c, def, before, *next, ev, workflow.Action{Kind: workflow.ActionHalt, Reason: next.Reason})
					}
					next.Awaiting.Run = run
					return chainSaveFlow(rt, tx, c, def, before, *next, ev, act, member)
				}
			}
			return chainFlowPullCheck(ctx, rt, tx, c, def, before, next, ev, act, member)
		}
	}
	run, err := chainStartOrAdoptCheck(ctx, rt, tx, c, act.Step, act.Command)
	if err != nil {
		next.Status = workflow.StatusHalted
		next.Reason = chainCheckStartReason(act.Step, err)
		return chainTerminalWF(ctx, rt, tx, c, def, before, *next, ev, workflow.Action{Kind: workflow.ActionHalt, Reason: next.Reason})
	}
	next.Awaiting.Run = run
	return chainSaveFlow(rt, tx, c, def, before, *next, ev, act, member)
}

// chainRepeatRedCheck reports whether a red check's output repeats the
// previous red's, so a repair is only ever skipped for a failure already
// seen. The previous red must belong to the for-each item the chain is on:
// a plan's first red is never a repeat.
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

// chainRepeatSameItem reports whether the previous red closed under the
// for-each item the state is on. The trace row carries the item as the legacy
// plan column, so the repeat stays inside one repair loop.
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

// chainFlowPullCheck answers a placed writer's check from its newest closed
// round's gate record. No record yet leaves the chain awaiting, for a later
// tick. It records no run id: the next check step would inherit it and ask
// the server for a run that was never created.
func chainFlowPullCheck(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, before workflow.State, next *workflow.State, ev workflow.Event, act workflow.Action, member string) error {
	round := chain.NewestClosedRound(tx, member)
	rec, err := chain.RoundGate(tx, member, round)
	if err != nil {
		return err
	}
	// The round is the record's round, not a run id: the close below must name
	// the run the state awaits, or the engine reads another run's close.
	next.Awaiting.Run = 0
	if rec == nil {
		return chainSaveFlow(rt, tx, c, def, before, *next, ev, act, member)
	}
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
		Kind: workflow.EventCheckClosed, Step: act.Step, Run: 0,
		Result: chain.GateResult(rec), Log: rec.LogPath,
		RepeatRed: chainRepeatRedCheck(rt, c, *next, act.Step, rec.LogPath),
	})
}

// chainFlowWriterMember names the member whose tree a check runs on: the
// keeper writer, or the first member when the workflow has none.
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
// single database transaction.
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
	// A start's send has no event of its own: the row's state is the record.
	// A fork writes its trace row so the trace records the fork.
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

func flowTraceStep(before workflow.State, ev workflow.Event) string {
	if ev.Step != "" {
		return ev.Step
	}
	return before.At
}

// flowPlanPos is a state's plan position for the trace's legacy plan column.
// An exhausted walk reads the last plan, like one that has not started.
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

// chainCheckStartReason is the halt reason of a check that could not start.
// A preflight refusal reads as itself, not as a red check.
func chainCheckStartReason(step string, err error) string {
	var envErr *EnvironmentError
	if errors.As(err, &envErr) {
		return envErr.Error()
	}
	return fmt.Sprintf("check %s could not start: %v", step, err)
}
