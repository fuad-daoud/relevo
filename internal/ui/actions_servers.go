package ui

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// ServerProbes checks every configured server off the update loop. The
// servers come from the runtime's config store; a nil adapter, a runtime with
// no config store, or a store that will not load answers with no servers
// rather than an error, so the view can say the section is empty instead of
// failing.
func (a *mastermindActions) ServerProbes(ctx context.Context) []relevo.ServerProbe {
	if a == nil || a.live == nil {
		return nil
	}
	rt := a.runtime()
	if rt.Config == nil {
		return nil
	}
	loaded, err := rt.Config.Load()
	if err != nil {
		return nil
	}
	return relevo.ProbeServers(ctx, rt, loaded.Servers, "")
}
