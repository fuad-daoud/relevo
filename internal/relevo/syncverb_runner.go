package relevo

import (
	"context"
	"errors"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// ensureRunner reports the runner push and pull drive, building its client
// the first time through memberOpener. A machine whose mark is off, or whose
// open fails, has no runner and gets false with nothing opened and nothing
// cached. Callers hold the verb guard across the whole verb, so two Ensures
// never overlap on one VerbRunner; the cockpit's instance is never shared.
func (v *VerbRunner) ensureRunner(ctx context.Context) (*relevosync.Runner, bool) {
	if v.Runner == nil {
		v.Runner = &relevosync.Runner{Local: v.Local}
	}
	if !v.Runner.Ensure(ctx, v.memberOpener()) {
		return nil, false
	}
	return v.Runner, true
}

// dropRunner forgets the runner's client so the next attempt rebuilds it.
// Enable calls it after success (the stored token or remote may have changed
// under the old handle) and disable calls it after success (the machine is
// off, and a cached client would outlive the mark that governs it).
func (v *VerbRunner) dropRunner() {
	if v.Runner != nil {
		v.Runner.Client = nil
	}
}

// memberOpener opens the member a push or pull drives: the stored settings
// and token, validated locally before any dial, over the role-gated member
// open. A machine with no remote or no token fails here, fast and without a
// dial, which is what keeps a refusal off the network. A file no driver
// joined fails here too, for the same reason: opening it would dial a remote
// about a file whose membership was never established.
func (v *VerbRunner) memberOpener() func(context.Context) (relevosync.SyncClient, error) {
	return func(ctx context.Context) (relevosync.SyncClient, error) {
		settings, err := relevosync.ReadSettings(v.Local)
		if err != nil {
			return nil, err
		}
		token, ok := v.storedToken()
		if !ok {
			return nil, errors.New("sync: no token stored on this machine")
		}
		if joined, err := relevosync.HasSyncMarker(v.Path); err != nil || !joined {
			return nil, errors.New("sync: this file is not a member of a sync yet")
		}
		return v.openRemote(ctx, v.openConfig(settings, token, false))
	}
}
