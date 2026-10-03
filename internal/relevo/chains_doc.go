package relevo

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// ChainsDoc is the read model of all chains, presenting a unified view of local,
// placed and remote chains for status displays and cockpit views.
type ChainsDoc struct {
	Chains []ChainEntry `json:"chains"`
}

// ChainEntry is one chain's status, steps, members, placement and hierarchy position.
type ChainEntry struct {
	Name      string               `json:"name"`
	Parent    string               `json:"parent,omitempty"`
	Depth     int                  `json:"depth"`
	Status    string               `json:"status"`
	Reason    string               `json:"reason,omitempty"`
	Step      string               `json:"step,omitempty"`
	PlanPos   int                  `json:"plan_pos"`
	PlanTotal int                  `json:"plan_total"`
	StartedAt time.Time            `json:"started_at,omitempty"`
	Elapsed   time.Duration        `json:"elapsed"`
	Where     string               `json:"where"`
	Stale     string               `json:"stale,omitempty"`
	Steps     []ChainStep          `json:"steps,omitempty"`
	Members   []view.BindingStatus `json:"-"`
	Children  []string             `json:"children,omitempty"`
}

// ChainStep is one workflow step in the read model.
type ChainStep struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Actor       string `json:"actor,omitempty"`
	Member      string `json:"member,omitempty"`
	Visits      int    `json:"visits"`
	LastOutcome string `json:"last_outcome,omitempty"`
	InFlight    bool   `json:"in_flight"`
	Round       int    `json:"round"`
}

// ReadChains reads every stored chain and returns the complete read model,
// querying live trace and status for reachable remote chains.
func ReadChains(ctx context.Context, rt Runtime) (ChainsDoc, error) {
	return ReadChainsScope(ctx, rt, Scope{})
}

// ReadChainsScope is ReadChains narrowed by sc: one mastermind's chains and the
// children hanging under them, under the same scope rule the binding listing
// uses. A chain is a builder's row, so a chain belongs to the mastermind that
// owns it and a chain listing follows the same scope as a binding listing.
func ReadChainsScope(ctx context.Context, rt Runtime, sc Scope) (ChainsDoc, error) {
	chains, err := rt.Store.Chains()
	if err != nil {
		return ChainsDoc{}, err
	}
	chains = scopedChains(chains, sc)
	if len(chains) == 0 {
		return ChainsDoc{Chains: []ChainEntry{}}, nil
	}

	bindings, err := rt.Store.List()
	if err != nil {
		return ChainsDoc{}, err
	}
	rep, _ := buildReport(ctx, rt, bindings)
	repByName := make(map[string]view.BindingStatus, len(rep.Bindings))
	for _, b := range rep.Bindings {
		repByName[b.Name] = b
	}
	storeBindings := make(map[string]store.Binding, len(bindings))
	for _, b := range bindings {
		storeBindings[b.Name] = b
	}

	entries := make([]ChainEntry, 0, len(chains))
	now := rt.Now().UTC()
	for _, c := range chains {
		entry := chainDocEntry(ctx, rt, c, now, repByName, storeBindings)
		entries = append(entries, entry)
	}

	return assembleChainsDoc(entries), nil
}

// chainDocEntry builds one ChainEntry from stored data and optional remote view.
//
// `stale` means one thing only: this machine could not ask the server. A 404 is
// the server's answer that it no longer holds the chain, which is not a fault
// to report and never overwrites the stored status. A chain the stored row
// already holds as over keeps that status with an empty `Stale`: the pull wrote
// the row's end from the same view that closed it, so there is nothing left to
// ask the server. A chain the row still holds as open reads as gone instead,
// carrying the gone wording the pull writes for a chain the server dropped.
func chainDocEntry(ctx context.Context, rt Runtime, c db.ChainRow, now time.Time, repByName map[string]view.BindingStatus, storeBindings map[string]store.Binding) ChainEntry {
	var liveTrace []db.ChainEventRow
	var staleErr string

	if chainOnServer(c) && rt.Remote != nil {
		if v, err := chainGetView(ctx, rt, c); err != nil {
			switch {
			case is404(err) && chainTerminalStatus(c.Status):
				// Released and already over: the stored row is the answer, and
				// a server that no longer holds it is not stale.
			case is404(err):
				c.Status = string(chain.StatusGone)
				c.Reason = chainGoneReason(c.Name, c.Server)
			default:
				staleErr = err.Error()
			}
		} else {
			if mirrored, merr := chainRowFromView(c, v, now); merr == nil {
				c = mirrored
			}
			for _, r := range v.Trace {
				liveTrace = append(liveTrace, db.ChainEventRow{
					ChainID: c.ID, Seq: r.Seq, TS: r.TS, Phase: r.Phase, Step: r.Step,
					Member: r.Member, Round: r.Round, Plan: r.Plan, Event: r.Event,
					Action: r.Action, Reason: r.Reason,
				})
			}
		}
	}

	var traceRows []db.ChainEventRow
	if len(liveTrace) > 0 {
		traceRows = liveTrace
	} else {
		traceRows, _ = rt.Store.ChainEvents(c.Name)
	}

	memberNames := chainReadMembers(rt.Store, c)
	members := make([]view.BindingStatus, 0, len(memberNames))
	for _, mName := range memberNames {
		if bs, ok := repByName[mName]; ok {
			members = append(members, bs)
		}
	}

	var memberRows []db.ChainMemberRow
	if len(c.WorkflowJSON) > 0 {
		memberRows, _ = rt.Store.ChainMembers(c.Name)
	}

	steps, currentStep, planPos, planTotal := chainDocStepsAndFacts(c, memberRows, traceRows)
	where := chainWhere(c, memberNames, repByName, storeBindings)
	elapsed := chainElapsed(c, now)

	return ChainEntry{
		Name:      c.Name,
		Parent:    c.Parent,
		Status:    c.Status,
		Reason:    c.Reason,
		Step:      currentStep,
		PlanPos:   planPos,
		PlanTotal: planTotal,
		StartedAt: c.CreatedAt,
		Elapsed:   elapsed,
		Where:     where,
		Stale:     staleErr,
		Steps:     steps,
		Members:   members,
	}
}

// chainDocStepsAndFacts extracts steps and plan facts for a chain row without panicking
// when workflow or engine state JSON is missing.
func chainDocStepsAndFacts(c db.ChainRow, memberRows []db.ChainMemberRow, traceRows []db.ChainEventRow) ([]ChainStep, string, int, int) {
	if len(c.StateJSON) == 0 || len(c.WorkflowJSON) == 0 {
		planPos := c.Plan
		if planPos < 1 {
			planPos = 1
		}
		planTotal := c.Plans
		if planTotal < 1 {
			planTotal = 1
		}
		return nil, c.Step, planPos, planTotal
	}

	st, err := chainWorkflowState(c)
	if err != nil {
		return nil, c.Step, c.Plan, c.Plans
	}
	def, err := chainWorkflowDef(c)
	if err != nil {
		return nil, c.Step, c.Plan, c.Plans
	}

	currentStep := st.Awaiting.Step
	if currentStep == "" {
		currentStep = st.At
	}
	if currentStep == "" {
		currentStep = c.Step
	}

	it := st.Iter["plans"]
	planTotal := len(it.Items)
	planPos := it.Index + 1
	if planPos < 1 {
		planPos = 1
	}
	if planTotal == 0 {
		planTotal = c.Plans
		planPos = c.Plan
	}

	steps := chainDocSteps(c, def, st, memberRows, traceRows)
	return steps, currentStep, planPos, planTotal
}

// chainDocSteps builds the ordered steps for a workflow definition and state.
func chainDocSteps(c db.ChainRow, def workflow.Definition, st workflow.State, memberRows []db.ChainMemberRow, traceRows []db.ChainEventRow) []ChainStep {
	orderedIDs := orderWorkflowSteps(def)
	out := make([]ChainStep, 0, len(orderedIDs))

	for _, id := range orderedIDs {
		s := def.Steps[id]
		kinds := s.Kinds()
		kind := ""
		if len(kinds) == 1 {
			kind = kinds[0]
		}

		actor := ""
		if s.Run != "" {
			actor = workflow.RenderParams(def, s.Run)
		}

		member := ""
		if actor != "" {
			member = stepMemberBinding(c, def, actor, memberRows)
		}

		inFlight := st.Awaiting.Step == id && id != ""
		round := 0
		if r, ok := st.Results[id]; ok {
			round = r.Round
		}
		if inFlight {
			if st.Awaiting.Round != 0 {
				round = st.Awaiting.Round
			} else if st.Awaiting.Run != 0 {
				round = st.Awaiting.Run
			}
		}

		outcome := chainStepOutcome(id, s, st, traceRows)
		visits := st.Visits[id]

		out = append(out, ChainStep{
			ID:          id,
			Kind:        kind,
			Actor:       actor,
			Member:      member,
			Visits:      visits,
			LastOutcome: outcome,
			InFlight:    inFlight,
			Round:       round,
		})
	}
	return out
}

// stepMemberBinding matches an actor to its member binding name.
func stepMemberBinding(c db.ChainRow, def workflow.Definition, actor string, memberRows []db.ChainMemberRow) string {
	for _, m := range memberRows {
		if chainFlowActorMatch(m.Actor, actor) {
			return m.Binding
		}
	}
	if actor == "builder" || actor == def.Params["builder"].Str {
		if c.Builder != "" {
			return c.Builder
		}
	}
	if actor == "reviewer" || actor == def.Params["reviewer"].Str {
		if c.Reviewer != "" {
			return c.Reviewer
		}
	}
	if actor == "planner" || actor == def.Params["planner"].Str {
		if c.Planner != "" {
			return c.Planner
		}
	}
	if actor == "security" || actor == def.Params["security"].Str {
		if c.Security != "" {
			return c.Security
		}
	}
	return c.Name + "-" + actor
}

// chainStepOutcome formats a step's latest outcome for display.
func chainStepOutcome(stepID string, step workflow.Step, st workflow.State, traceRows []db.ChainEventRow) string {
	res, ok := st.Results[stepID]
	kinds := step.Kinds()
	if len(kinds) == 1 && kinds[0] == "check" {
		return checkStepOutcome(stepID, step, res, traceRows)
	}

	if !ok && st.Visits[stepID] == 0 {
		return ""
	}

	outcomeText := ""
	if len(res.Outcomes) > 0 {
		var pairs []string
		keys := make([]string, 0, len(res.Outcomes))
		for k := range res.Outcomes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			pairs = append(pairs, fmt.Sprintf("%s=%s", k, res.Outcomes[k]))
		}
		outcomeText = strings.Join(pairs, ", ")
	} else if res.Status != "" {
		outcomeText = res.Status
	}

	if res.Round > 0 {
		if outcomeText != "" {
			return fmt.Sprintf("%s r%d · %s", stepID, res.Round, outcomeText)
		}
		return fmt.Sprintf("%s r%d", stepID, res.Round)
	}
	if outcomeText != "" {
		return fmt.Sprintf("%s · %s", stepID, outcomeText)
	}
	return ""
}

// checkStepOutcome formats a check step's latest result and target for display.
func checkStepOutcome(stepID string, step workflow.Step, res workflow.Result, traceRows []db.ChainEventRow) string {
	result := res.Status
	target := ""
	for i := len(traceRows) - 1; i >= 0; i-- {
		tr := traceRows[i]
		if tr.Step != stepID {
			continue
		}
		if tr.Event != "" {
			if ev, err := workflow.DecodeEvent(tr.Event); err == nil && ev.Kind == workflow.EventCheckClosed {
				if ev.Result != "" {
					result = ev.Result
				}
			}
		}
		if tr.Action != "" {
			if act, err := workflow.DecodeAction(tr.Action); err == nil {
				if act.Step != "" {
					target = act.Step
				}
			}
		}
		if result != "" && target != "" {
			break
		}
	}
	if target == "" && step.On != nil {
		target = checkTarget(step.On, result)
	}
	if result == "" {
		return ""
	}
	if target != "" {
		return fmt.Sprintf("%s · %s → %s", stepID, result, target)
	}
	return fmt.Sprintf("%s · %s", stepID, result)
}

// checkTarget resolves the step target for a check result from on edges.
func checkTarget(on map[string]workflow.Target, result string) string {
	if t, ok := on["result="+result]; ok && t.Kind == workflow.TargetStep {
		return t.Step
	}
	if t, ok := on[result]; ok && t.Kind == workflow.TargetStep {
		return t.Step
	}
	if t, ok := on["else"]; ok && t.Kind == workflow.TargetStep {
		return t.Step
	}
	return ""
}

// orderWorkflowSteps orders workflow steps breadth-first from start, followed
// by sorted unreachable steps.
func orderWorkflowSteps(def workflow.Definition) []string {
	var ordered []string
	seen := map[string]bool{}

	if def.Start != "" && def.Steps != nil {
		if _, ok := def.Steps[def.Start]; ok {
			queue := []string{def.Start}
			for len(queue) > 0 {
				id := queue[0]
				queue = queue[1:]
				if seen[id] {
					continue
				}
				seen[id] = true
				ordered = append(ordered, id)

				s := def.Steps[id]
				var onKeys []string
				for k := range s.On {
					onKeys = append(onKeys, k)
				}
				sort.Strings(onKeys)
				for _, k := range onKeys {
					t := s.On[k]
					if t.Kind == workflow.TargetStep && t.Step != "" {
						if _, exists := def.Steps[t.Step]; exists && !seen[t.Step] {
							queue = append(queue, t.Step)
						}
					}
				}
				if s.Budget != nil && s.Budget.Then.Kind == workflow.TargetStep && s.Budget.Then.Step != "" {
					if _, exists := def.Steps[s.Budget.Then.Step]; exists && !seen[s.Budget.Then.Step] {
						queue = append(queue, s.Budget.Then.Step)
					}
				}
			}
		}
	}

	var unreachable []string
	for id := range def.Steps {
		if !seen[id] {
			unreachable = append(unreachable, id)
		}
	}
	sort.Strings(unreachable)
	ordered = append(ordered, unreachable...)
	return ordered
}

// chainWhere determines where a chain runs: server, placed, or local.
func chainWhere(c db.ChainRow, memberNames []string, repByName map[string]view.BindingStatus, storeBindings map[string]store.Binding) string {
	if c.Server != "" {
		return "server " + c.Server
	}
	for _, m := range memberNames {
		if sb, ok := storeBindings[m]; ok && sb.Link != nil {
			server := sb.Builder.Server
			if server == "" {
				server = sb.Link.Installation
			}
			if server != "" {
				return "placed " + server
			}
			return "placed"
		}
		if bs, ok := repByName[m]; ok && bs.Server != "" {
			return "placed " + bs.Server
		}
	}
	return "local"
}

// chainElapsed computes a chain's elapsed time from creation, using UpdatedAt for terminal states.
func chainElapsed(c db.ChainRow, now time.Time) time.Duration {
	if c.CreatedAt.IsZero() {
		return 0
	}
	end := now
	if chainTerminalStatus(c.Status) && !c.UpdatedAt.IsZero() {
		end = c.UpdatedAt
	}
	d := end.Sub(c.CreatedAt)
	if d < 0 {
		return 0
	}
	return d
}

// chainTerminalStatus reports whether a status is terminal.
func chainTerminalStatus(st string) bool {
	return st == string(chain.StatusHalted) || st == string(chain.StatusStopped) || st == string(chain.StatusDone)
}

// chainStatusRank assigns a sort rank: running and halted first, then stopped,
// then done, then gone. A gone chain ranks last of the words the read model
// knows, because nothing about it can change and nothing is waiting on it; a
// word no rule knows still sorts after every one of them.
func chainStatusRank(status string) int {
	switch status {
	case string(chain.StatusRunning), string(chain.StatusHalted):
		return 0
	case string(chain.StatusStopped):
		return 1
	case string(chain.StatusDone):
		return 2
	case string(chain.StatusGone):
		return 3
	default:
		return 4
	}
}

// assembleChainsDoc groups chains by Parent, sorts roots and children, and nests
// recursively up to a depth of 8.
func assembleChainsDoc(entries []ChainEntry) ChainsDoc {
	byName := make(map[string]ChainEntry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}

	childrenOf := make(map[string][]string)
	var roots []string
	for _, e := range entries {
		if e.Parent != "" {
			if _, ok := byName[e.Parent]; ok {
				childrenOf[e.Parent] = append(childrenOf[e.Parent], e.Name)
				continue
			}
		}
		roots = append(roots, e.Name)
	}

	sortEntries := func(names []string) {
		sort.SliceStable(names, func(i, j int) bool {
			ei, ej := byName[names[i]], byName[names[j]]
			ri, rj := chainStatusRank(ei.Status), chainStatusRank(ej.Status)
			if ri != rj {
				return ri < rj
			}
			return ei.Name < ej.Name
		})
	}

	sortEntries(roots)
	for p := range childrenOf {
		sortEntries(childrenOf[p])
	}

	var ordered []ChainEntry
	visited := make(map[string]bool, len(entries))

	var walk func(name string, depth int)
	walk = func(name string, depth int) {
		if visited[name] {
			return
		}
		visited[name] = true

		if depth > 8 {
			depth = 8
		}
		e := byName[name]
		e.Depth = depth
		e.Children = childrenOf[name]
		ordered = append(ordered, e)

		for _, ch := range childrenOf[name] {
			walk(ch, depth+1)
		}
	}

	for _, r := range roots {
		walk(r, 0)
	}

	return ChainsDoc{Chains: ordered}
}
