package chain

import "github.com/fuad-daoud/relevo/internal/db"

// MemberLister is the member read the store and a tx both answer: the
// chain_member rows that name a chain's members.
type MemberLister interface {
	ChainMembers(name string) ([]db.ChainMemberRow, error)
}

// Awaited resolves the member a chain waits on and the round it awaits.
// A row that carries an engine state answers from the state's awaiting actor
// through chain_member, so a workflow whose actor fills no legacy part still
// resolves; a row without a state keeps the legacy awaiting columns.
func Awaited(m MemberLister, c db.ChainRow) (name string, round int, ok bool) {
	if len(c.StateJSON) > 0 {
		return chainStateAwaited(m, c)
	}
	name = MemberName(c, c.AwaitingMember)
	if name == "" {
		return "", 0, false
	}
	return name, c.AwaitingRound, true
}

// chainStateAwaited is Awaited's state arm: the state's awaiting actor
// through chain_member. ok is false for a state that awaits a check run, which
// names no member, or an actor no member runs.
func chainStateAwaited(m MemberLister, c db.ChainRow) (name string, round int, ok bool) {
	st, err := WorkflowState(c)
	if err != nil || st.Awaiting.Member == "" {
		return "", 0, false
	}
	rows, err := m.ChainMembers(c.Name)
	if err != nil {
		return "", 0, false
	}
	for _, row := range rows {
		if FlowActorMatch(row.Actor, st.Awaiting.Member) {
			return row.Binding, st.Awaiting.Round, true
		}
	}
	return "", 0, false
}
