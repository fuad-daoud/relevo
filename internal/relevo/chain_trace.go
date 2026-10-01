package relevo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
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
	for _, r := range rows {
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
