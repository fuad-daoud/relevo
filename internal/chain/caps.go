package chain

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// NameCap is the longest chain name a set of members allows: the binding
// name cap, less the longest member suffix. The members' own names decide it,
// because a member name is the chain's name plus a fixed suffix: "-plan" for a
// default chain's planner, an actor's own name for a custom workflow's reader.
func NameCap(name string, members []PlannedMember) int {
	return store.MaxAgentNameLen - len(LongestMemberSuffix(name, members))
}

// LongestMemberSuffix is the longest suffix beyond the chain's own name that a
// set of member names carries: what the chain-name cap leaves room for, and
// the suffix a refusal names.
func LongestMemberSuffix(name string, members []PlannedMember) string {
	longest := ""
	for _, m := range members {
		if len(m.Name) < len(name) {
			continue
		}
		if suffix := m.Name[len(name):]; len(suffix) > len(longest) {
			longest = suffix
		}
	}
	return longest
}
