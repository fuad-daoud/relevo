package relevo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"encoding/json"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// ErrNotAChain is `show --trace`'s refusal of a name that is not a chain: only
// a chain has a trace.
var ErrNotAChain = errors.New("not a chain")

// ChainTraceDoc is one chain's state and its ordered trace, the document
// `relevo show <n> --trace` renders and `--json` prints.
type ChainTraceDoc struct {
	Name        string
	Status      string
	Phase       string
	Step        string
	Plan        int
	Plans       int
	Corrections int
	Events      []ChainTraceEvent `json:"events"`
}

// ChainTraceEvent is one trace row: the transition's order and time, the phase
// and step before the event, the member whose round closed and its round, the
// decoded event and action, and the halt reason when there is one.
type ChainTraceEvent struct {
	Seq    int
	TS     time.Time
	Phase  string
	Step   string
	Member string
	Round  int
	Plan   int
	Event  chain.Event
	Action chain.Action
	Reason string
	// Flow and FlowAction are a workflow row's decoded event and action; both
	// are nil on a row the fixed state machine wrote.
	Flow       *workflow.Event
	FlowAction *workflow.Action
}

// ChainTrace reads one chain's state and its trace rows, in seq order. A stored
// event or action that does not decode is an error rather than a guess, so a
// trace never renders a transition it cannot name.
func ChainTrace(ctx context.Context, rt Runtime, name string) (ChainTraceDoc, error) {
	c, err := rt.Store.Chain(name)
	if err != nil {
		return ChainTraceDoc{}, err
	}
	if chainOnServer(c) {
		return chainServerTrace(ctx, rt, c)
	}
	rows, err := rt.Store.ChainEvents(name)
	if err != nil {
		return ChainTraceDoc{}, err
	}

	doc := ChainTraceDoc{
		Name: c.Name, Status: c.Status, Phase: c.Phase, Step: c.Step,
		Plan: c.Plan, Plans: c.Plans, Corrections: c.Corrections,
	}
	// A chain that carries a workflow wrote every one of its rows as workflow
	// events; a chain that does not wrote them as the fixed state machine's.
	// The row's own event encoding cannot tell the two apart: their stopped and
	// needs_you kinds share a name.
	flow := len(c.WorkflowJSON) > 0
	for _, r := range rows {
		if flow {
			fev, ferr := workflow.DecodeEvent(r.Event)
			if ferr != nil {
				return ChainTraceDoc{}, fmt.Errorf("chain %s: %w", name, ferr)
			}
			act, aerr := workflow.DecodeAction(r.Action)
			if aerr != nil {
				return ChainTraceDoc{}, fmt.Errorf("chain %s: %w", name, aerr)
			}
			doc.Events = append(doc.Events, ChainTraceEvent{
				Seq: r.Seq, TS: r.TS, Step: r.Step,
				Member: r.Member, Round: r.Round, Plan: r.Plan, Reason: r.Reason,
				Flow: &fev, FlowAction: &act,
			})
			continue
		}
		ev, err := chain.DecodeEvent(r.Event)
		if err != nil {
			return ChainTraceDoc{}, fmt.Errorf("chain %s: %w", name, err)
		}
		act, err := chain.DecodeAction(r.Action)
		if err != nil {
			return ChainTraceDoc{}, fmt.Errorf("chain %s: %w", name, err)
		}
		doc.Events = append(doc.Events, ChainTraceEvent{
			Seq: r.Seq, TS: r.TS, Phase: r.Phase, Step: r.Step,
			Member: r.Member, Round: r.Round, Plan: r.Plan, Event: ev, Action: act, Reason: r.Reason,
		})
	}
	return doc, nil
}

// RenderTrace renders a trace document as one line per event, in seq order.
// Each line carries its own row's plan; a row written before the plan column
// existed (plan 0) falls back to the chain's current plan.
func RenderTrace(doc ChainTraceDoc) string {
	var b strings.Builder
	for _, e := range doc.Events {
		if e.Flow != nil && e.FlowAction != nil {
			b.WriteString(flowTraceLine(e))
			b.WriteByte('\n')
			continue
		}
		plan := e.Plan
		if plan == 0 {
			plan = doc.Plan
		}
		b.WriteString(chain.TraceLine{
			Plan: plan, Plans: doc.Plans,
			Phase: chain.Phase(e.Phase), Step: chain.Step(e.Step),
			Member: e.Member, Round: e.Round,
			Event: e.Event, Action: e.Action, Reason: e.Reason,
		}.Line())
		b.WriteByte('\n')
	}
	return b.String()
}

// flowTraceLine renders one workflow trace row: the step it was on, the round
// or check run, the outcomes it carried and the target it chose, for example
// "review r2  verdict=changes → correct".
func flowTraceLine(e ChainTraceEvent) string {
	var b strings.Builder
	switch e.Flow.Kind {
	case workflow.EventCheckClosed:
		fmt.Fprintf(&b, "%s run %d", e.Step, e.Flow.Run)
		if e.Flow.Result != "" {
			b.WriteString("  result=" + e.Flow.Result)
		}
	case workflow.EventStepClosed:
		fmt.Fprintf(&b, "%s r%d", e.Step, e.Round)
		if text := flowOutcomesText(e.Flow.Outcomes); text != "" {
			b.WriteString("  " + text)
		}
	case workflow.EventStopped:
		fmt.Fprintf(&b, "%s stopped", e.Step)
	case workflow.EventNeedsYou:
		fmt.Fprintf(&b, "%s needs you", e.Step)
	default:
		b.WriteString(e.Step)
	}
	b.WriteString(" → " + flowTargetText(*e.FlowAction))
	return b.String()
}

// flowOutcomesText renders a close's outcomes as sorted key=value pairs.
func flowOutcomesText(outcomes map[string]string) string {
	if len(outcomes) == 0 {
		return ""
	}
	keys := make([]string, 0, len(outcomes))
	for key := range outcomes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+outcomes[key])
	}
	return strings.Join(pairs, ", ")
}

// flowTargetText names the target a workflow action chose.
func flowTargetText(act workflow.Action) string {
	switch act.Kind {
	case workflow.ActionSend:
		if act.Step != "" {
			return act.Step
		}
		return "send " + act.Actor
	case workflow.ActionRunCheck:
		return act.Step
	case workflow.ActionFinish:
		return "done"
	case workflow.ActionHalt:
		return "halt"
	case workflow.ActionStop:
		return "stopped"
	default:
		return string(act.Kind)
	}
}

// showTrace answers `show <n> --trace`: the chain's state and its ordered
// trace, rendered for the human and carried as a document for --json. A name
// the store holds no chain for is refused, because only a chain has a trace.
func showTrace(ctx context.Context, rt Runtime, opts ShowOptions) (ShowResult, error) {
	if _, err := rt.Store.Chain(opts.Name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ShowResult{}, fmt.Errorf("show: %s: --trace: %w", opts.Name, ErrNotAChain)
		}
		return ShowResult{}, err
	}
	doc, err := ChainTrace(ctx, rt, opts.Name)
	if err != nil {
		return ShowResult{}, err
	}
	return ShowResult{
		Name:    opts.Name,
		Live:    true,
		Section: ShowTrace,
		Text:    RenderTrace(doc),
		Trace:   &doc,
	}, nil
}

// showWorkflow answers `show <n> --workflow`: the definition the chain took at
// start, printed as indented JSON. A name the store holds no workflow chain
// for is refused, because only a workflow chain has one.
func showWorkflow(ctx context.Context, rt Runtime, opts ShowOptions) (ShowResult, error) {
	_ = ctx
	c, err := rt.Store.Chain(opts.Name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ShowResult{}, fmt.Errorf("show: %s: --workflow: %w", opts.Name, ErrNotAChain)
		}
		return ShowResult{}, err
	}
	def, err := chainWorkflowDef(c)
	if err != nil {
		return ShowResult{}, err
	}
	pretty, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return ShowResult{}, err
	}
	return ShowResult{
		Name:    opts.Name,
		Live:    true,
		Section: ShowWorkflow,
		Text:    string(pretty) + "\n",
	}, nil
}
