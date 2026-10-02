package ui

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// Chains reads the read model of all chains off the update loop.
func (a *mastermindActions) Chains(ctx context.Context) (relevo.ChainsDoc, error) {
	return relevo.ReadChains(ctx, a.runtime())
}

// ChainTrace reads one chain's trace off the update loop. A server chain's
// rows come from the server itself; a server that cannot be reached is an
// error the trace view shows, never a trace guessed from the mirror.
func (a *mastermindActions) ChainTrace(ctx context.Context, name string) (relevo.ChainTraceDoc, error) {
	return relevo.ChainTrace(ctx, a.runtime(), name)
}
