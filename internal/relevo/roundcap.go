package relevo

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrRoundCap reports that a binding has already used every round its cap
// allows, so no further round can start until a human starts a fresh builder.
// A caller probes it with errors.Is.
var ErrRoundCap = errors.New("hit the round cap")

// roundCapError is ErrRoundCap carrying the binding and its cap, so the
// refusal keeps the numbers a human needs while errors.Is still finds the
// sentinel underneath.
type roundCapError struct {
	name string
	cap  int
}

func (e *roundCapError) Error() string {
	return fmt.Sprintf("binding %q %s of %d", e.name, ErrRoundCap, e.cap)
}

func (e *roundCapError) Unwrap() error { return ErrRoundCap }

func roundCapRefusal(name string, cap int) error { return &roundCapError{name: name, cap: cap} }

// chainMemberCapSlack is the headroom above a chain's own plan, correction and
// repair count: a member's first and last rounds, plus the round the chain
// spends before its first plan lands.
const chainMemberCapSlack = 2

// chainMemberRoundCap is the cap one chain member runs under, never below the
// default an ordinary binding gets. Each part spends rounds at its own rate per
// plan: the builder builds once, rebuilds per correction and repairs per
// regate; the reviewer reviews once per builder round; the planner plans once
// per correction plus the first; security reviews once, then once per
// correction. A part this table does not know takes the builder's count.
func chainMemberRoundCap(part string, plans int, set chain.Settings) int {
	var perPlan int
	switch part {
	case chain.MemberPlanner:
		perPlan = set.MaxCorrections + 1
	case chain.MemberSecurity:
		perPlan = 1 + set.MaxCorrections
	default:
		perPlan = 1 + set.MaxCorrections + set.Regate
	}
	cap := plans*perPlan + chainMemberCapSlack
	if cap < store.DefaultRoundCap {
		return store.DefaultRoundCap
	}
	return cap
}
