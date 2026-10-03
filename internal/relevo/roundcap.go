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

// roundCapRefusal builds the typed cap refusal for one binding.
func roundCapRefusal(name string, cap int) error { return &roundCapError{name: name, cap: cap} }

// chainWriterCapSlack is the headroom above a chain's own plan, correction and
// repair count: the writer's first and last rounds, plus the round the chain
// spends before its first plan lands.
const chainWriterCapSlack = 2

// chainWriterRoundCap is the cap a chain's writer member runs under: one build
// round per plan plus the chain's correction and repair budgets, with slack,
// never below the default an ordinary binding gets. The writer carries every
// plan plus its correction and repair rounds, so an ordinary cap would refuse
// a long chain mid-flight.
func chainWriterRoundCap(plans int, set chain.Settings) int {
	cap := plans*(1+set.MaxCorrections+set.Regate) + chainWriterCapSlack
	if cap < store.DefaultRoundCap {
		return store.DefaultRoundCap
	}
	return cap
}
