package view

import (
	"sort"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// MembersOf is a chain's member binding names in the order a chain view
// lists them: the builder first, then the reviewer, the planner and the
// security member. A part the row leaves empty is skipped.
func MembersOf(c db.ChainRow) []string {
	var out []string
	for _, name := range []string{c.Builder, c.Reviewer, c.Planner, c.Security} {
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// ReadMembers names a chain's members for a read surface: the bindings its
// engine recorded, in creation order, when the row carries a state; the legacy
// part columns otherwise. A part the row leaves empty is skipped.
func ReadMembers(s *store.Store, c db.ChainRow) []string {
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
	return MembersOf(c)
}

// PendingMembers names, in member order, the members of a chain that still
// hold a payload the MasterMind has not collected: an unconfirmed to-mastermind
// entry nobody has read back.
//
// It reads each member's own log rather than the roll-up's: a chain row
// replaces its members, so this is the only place that can still say which one
// is holding something.
//
// A member whose record is gone answers nothing and is not named, and a log that
// cannot be read names nothing: a chain row must not claim a member is stranded
// on the strength of a read that failed.
func PendingMembers(s *store.Store, c db.ChainRow) []string {
	var out []string
	for _, member := range ReadMembers(s, c) {
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

// EndMembers names the members a chain's end verbs walk: the bindings its
// engine recorded in chain_member, in creation order, when the row carries a
// workflow; the legacy part columns otherwise. A binding the row leaves empty
// is skipped. A read failure keeps the legacy path, so an end verb on a legacy
// row reads the same as ever.
func EndMembers(m chain.MemberLister, c db.ChainRow) []string {
	if len(c.WorkflowJSON) == 0 {
		return MembersOf(c)
	}
	rows, err := m.ChainMembers(c.Name)
	if err != nil {
		return MembersOf(c)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Binding != "" {
			out = append(out, row.Binding)
		}
	}
	return out
}

// FindingsOfState reads a chain's security finding count from its engine
// state: the `findings` outcome the workflow's scan step recorded in Results. A
// row with no engine state, or a scan that never closed, carries 0.
func FindingsOfState(c db.ChainRow) int {
	st, err := chain.WorkflowState(c)
	if err != nil {
		return 0
	}
	return FlowFindings(st)
}

// FlowFindings is the finding count a state recorded: the newest step result
// whose outcomes carry a `findings` count. Only a security run step declares
// one, so this is that step's count; the round breaks a tie when a workflow
// runs more than one.
func FlowFindings(st workflow.State) int {
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
