package relevo

import (
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainGateResult is the builder close's gate word: none when no check ran,
// green when one passed, red otherwise. A red gate has already spent the
// regate budget, so it still reaches the reviewer rather than a halt.
func chainGateResult(gate *store.GateRecord) string {
	if gate == nil {
		return chain.GateNone
	}
	if gate.Result == "pass" {
		return chain.GateGreen
	}
	return chain.GateRed
}
