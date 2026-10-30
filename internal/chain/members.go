package chain

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

// PartOf names the part a member fills, from the chain row's columns.
func PartOf(c db.ChainRow, name string) string {
	if name == "" {
		return ""
	}
	switch name {
	case c.Builder:
		return MemberBuilder
	case c.Reviewer:
		return MemberReviewer
	case c.Planner:
		return MemberPlanner
	case c.Security:
		return MemberSecurity
	}
	return ""
}

// MemberName is PartOf's inverse: the binding name the row holds for
// a part. "" when the part is not on this chain (the security member of a chain
// whose phase is off).
func MemberName(c db.ChainRow, part string) string {
	switch part {
	case MemberBuilder:
		return c.Builder
	case MemberReviewer:
		return c.Reviewer
	case MemberPlanner:
		return c.Planner
	case MemberSecurity:
		return c.Security
	}
	return ""
}

// MemberChain resolves member to the chain that names it, reading through
// byMember (production: Store.ChainByMember). ok is false for a name that is
// in no chain and for every read failure alike, so callers treat both as "no
// member row to consult" instead of a fault they must handle -- a hint or a
// status may degrade to the ordinary names, never fail on it.
func MemberChain(byMember func(string) (db.ChainRow, error), member string) (db.ChainRow, bool) {
	c, err := byMember(member)
	if err != nil {
		return db.ChainRow{}, false
	}
	return c, true
}

// MemberHint picks the hint one waiting row names for member. A member whose
// owning chain is finished -- done or stopped -- is no longer the chain's to
// hold: `relevo done <member>` is allowed on such a member, so the hint names
// both the look and the clear, in the order a person runs them. A running or
// halted chain still owns its members' rounds -- done on a running-chain
// member is refused -- so their rows, like every non-member's, keep the
// status-only hint.
func MemberHint(member string, c db.ChainRow, memberOf bool) string {
	if memberOf {
		switch Status(c.Status) {
		case StatusDone, StatusStopped:
			return fmt.Sprintf("relevo status --name %s; relevo done %s clears it", member, member)
		}
	}
	return "relevo status --name " + member
}
