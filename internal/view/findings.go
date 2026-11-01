package view

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/refusal"
)

// ErrNoFindings is Show's error when --findings <id> without --round finds no
// round holding that consult's findings.
var ErrNoFindings = errors.New("no findings for that consult")

// FindingsRound resolves which round holds a consult's findings for
// `show --findings <id>`: requested names it, 0 scans for the newest round
// holding the findings. With none found it returns ErrNoFindings.
func FindingsRound(requested, upper int, exists func(round int) (bool, error)) (int, error) {
	if requested > 0 {
		if requested > upper {
			return 0, refusal.Rangef("round %d: binding has %d rounds", requested, upper)
		}
		return requested, nil
	}
	for round := upper; round >= 1; round-- {
		ok, err := exists(round)
		if err != nil {
			return 0, err
		}
		if ok {
			return round, nil
		}
	}
	return 0, ErrNoFindings
}

// FindingsRoundFor is the --findings resolution showLive and showArchived
// share: FindingsRound over the highest round a consult can be recorded on --
// counter (b.Round, or ab.Binding.Round) or the plan-round count, whichever is
// larger -- with the consult named when no round holds the findings.
func FindingsRoundFor(requested, counter, rounds int, name, id string, exists func(round int) (bool, error)) (int, error) {
	round, err := FindingsRound(requested, max(counter, rounds), exists)
	if errors.Is(err, ErrNoFindings) {
		return 0, fmt.Errorf("consult %s on %s: %w", id, name, err)
	}
	return round, err
}
