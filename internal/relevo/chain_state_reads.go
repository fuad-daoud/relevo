package relevo

import (
	"sort"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainMemberLister is the member read the store and a tx both answer: the
// chain_member rows that name a chain's members.
type chainMemberLister interface {
	ChainMembers(name string) ([]db.ChainMemberRow, error)
}

// chainAwaited resolves the member a chain waits on and the round it awaits.
// A row that carries an engine state answers from the state's awaiting actor
// through chain_member, so a workflow whose actor fills no legacy part still
// resolves; a row without a state keeps the legacy awaiting columns.
func chainAwaited(m chainMemberLister, c db.ChainRow) (name string, round int, ok bool) {
	if len(c.StateJSON) > 0 {
		return chainStateAwaited(m, c)
	}
	name = chainMemberName(c, c.AwaitingMember)
	if name == "" {
		return "", 0, false
	}
	return name, c.AwaitingRound, true
}

// chainStateAwaited is chainAwaited's state arm: the state's awaiting actor
// through chain_member. ok is false for a state that awaits a check run, which
// names no member, or an actor no member runs.
func chainStateAwaited(m chainMemberLister, c db.ChainRow) (name string, round int, ok bool) {
	st, err := chainWorkflowState(c)
	if err != nil || st.Awaiting.Member == "" {
		return "", 0, false
	}
	rows, err := m.ChainMembers(c.Name)
	if err != nil {
		return "", 0, false
	}
	for _, row := range rows {
		if chainFlowActorMatch(row.Actor, st.Awaiting.Member) {
			return row.Binding, st.Awaiting.Round, true
		}
	}
	return "", 0, false
}

// chainReadMembers names a chain's members for a read surface: the bindings its
// engine recorded, in creation order, when the row carries a state; the legacy
// part columns otherwise. A part the row leaves empty is skipped.
func chainReadMembers(s *store.Store, c db.ChainRow) []string {
	if len(c.WorkflowJSON) > 0 {
		rows, err := s.ChainMembers(c.Name)
		if err != nil {
			return nil
		}
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			if row.Binding != "" {
				out = append(out, row.Binding)
			}
		}
		return out
	}
	return chainMembersOf(c)
}

// chainPendingMembers names, in member order, the members of a chain that still
// hold a payload the MasterMind has not collected: an unconfirmed to-mastermind
// entry nobody has read back.
//
// It reads each member's own log rather than the roll-up's, because the roll-up
// is exactly what no longer shows those members: a chain row replaces them, so
// this is the only place that can still say which one is holding something.
//
// A member whose record is gone answers nothing and is not named, and a log that
// cannot be read names nothing: a chain row must not claim a member is stranded
// on the strength of a read that failed.
func chainPendingMembers(s *store.Store, c db.ChainRow) []string {
	var out []string
	for _, member := range chainReadMembers(s, c) {
		entries, err := s.ReadLog(member)
		if err != nil {
			continue
		}
		if chainHoldsUncollected(entries) {
			out = append(out, member)
		}
	}
	return out
}

// chainHoldsUncollected reports whether the log holds an entry still waiting on
// the MasterMind. An admitted entry is excluded along with a confirmed one: a
// push route has taken it, so nobody is waiting on it here.
func chainHoldsUncollected(entries []store.LogEntry) bool {
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && !e.Confirmed && e.AdmittedAt == nil {
			return true
		}
	}
	return false
}

// chainEndMembers names the members a chain's end verbs walk: the bindings its
// engine recorded in chain_member, in creation order, when the row carries a
// workflow; the legacy part columns otherwise. A binding the row leaves empty
// is skipped. A read failure keeps the legacy path, so an end verb on a legacy
// row is unchanged.
func chainEndMembers(m chainMemberLister, c db.ChainRow) []string {
	if len(c.WorkflowJSON) == 0 {
		return chainMembersOf(c)
	}
	rows, err := m.ChainMembers(c.Name)
	if err != nil {
		return chainMembersOf(c)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Binding != "" {
			out = append(out, row.Binding)
		}
	}
	return out
}

// chainFindingsOfState reads a chain's security finding count from its engine
// state: the `findings` outcome the workflow's scan step recorded in Results. A
// row with no engine state, or a scan that never closed, carries 0.
func chainFindingsOfState(c db.ChainRow) int {
	st, err := chainWorkflowState(c)
	if err != nil {
		return 0
	}
	return flowFindings(st)
}

// flowFindings is the finding count a state recorded: the newest step result
// whose outcomes carry a `findings` count. Only a security run step declares
// one, so this is that step's count; the round breaks a tie when a workflow
// runs more than one.
func flowFindings(st workflow.State) int {
	steps := make([]string, 0, len(st.Results))
	for step := range st.Results {
		steps = append(steps, step)
	}
	sort.Strings(steps)
	findings, round := 0, -1
	for _, step := range steps {
		val, ok := st.Results[step].Outcomes["findings"]
		if !ok {
			continue
		}
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			continue
		}
		if r := st.Results[step].Round; r >= round {
			findings, round = n, r
		}
	}
	return findings
}

// chainStoredFacts is a chain's fixed-column facts as a read surface shows
// them: the engine state's legacy projection when the row carries a state, the
// stored columns otherwise.
type chainStoredFacts struct {
	Phase       string
	Step        string
	Plan        int
	Plans       int
	Corrections int
	Awaiting    string
}

// chainStoredFactsOf projects a chain's facts onto the legacy columns a read
// surface shows. A row with a state reads them from the state through
// workflow.LegacyView, so the columns themselves stay unread; the plan count is
// kept at least one, as the row's own validity asks.
func chainStoredFactsOf(c db.ChainRow) chainStoredFacts {
	if len(c.StateJSON) > 0 {
		def, derr := chainWorkflowDef(c)
		st, serr := chainWorkflowState(c)
		if derr == nil && serr == nil {
			lf := workflow.LegacyView(def, st)
			plan, plans := lf.Plan, lf.Plans
			if plans < 1 {
				plan, plans = 1, 1
			}
			return chainStoredFacts{
				Phase: lf.Phase, Step: lf.Step, Plan: plan, Plans: plans,
				Corrections: lf.Corrections, Awaiting: lf.AwaitingMember,
			}
		}
	}
	return chainStoredFacts{
		Phase: c.Phase, Step: c.Step, Plan: c.Plan, Plans: c.Plans,
		Corrections: c.Corrections, Awaiting: c.AwaitingMember,
	}
}
