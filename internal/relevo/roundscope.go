package relevo

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// scopeRunning reports whether unit's scope is loaded and not yet gone. It is
// the single gate for "scopes are off": false when the runtime has no scope
// template, when the runner cannot probe one, and when the probe fails (the
// error is logged). A caller that needs only this answer never repeats the
// gate.
func scopeRunning(ctx context.Context, rt Runtime, unit string) bool {
	if rt.Scope == nil {
		return false
	}
	p, ok := rt.Runner.(spawn.ScopeProber)
	if !ok {
		return false
	}
	active, err := p.ScopeActive(ctx, unit)
	if err != nil {
		slog.Debug("scope probe", "unit", unit, "err", err)
		return false
	}
	return active
}

// endScope ends unit's scope when it is loaded, and reports whether it ended
// one. A scope that is not loaded, and a host with no systemctl, are (false,
// nil): there is nothing to end. A runner that can see a scope but not end one
// is an error, because the caller cannot otherwise free the unit.
func endScope(ctx context.Context, rt Runtime, unit string) (bool, error) {
	if !scopeRunning(ctx, rt, unit) {
		return false, nil
	}
	stopper, ok := rt.Runner.(spawn.ScopeStopper)
	if !ok {
		return false, fmt.Errorf("scope %s.scope is still running and this runner cannot end it", unit)
	}
	if err := stopper.StopScope(ctx, unit); err != nil {
		return false, fmt.Errorf("scope %s.scope: %w", unit, err)
	}
	return true, nil
}

// endRoundScope ends the scope of a round that is already over, best effort:
// the round must not fail to close because a scope would not die, so a failure
// only warns. It is a no-op when that round's scope is not loaded.
func endRoundScope(ctx context.Context, rt Runtime, b store.Binding, round int) {
	unit := scopeUnitNameFor(scopeRound, b.Owner, b.Name, round, "")
	if _, err := endScope(ctx, rt, unit); err != nil {
		slog.Warn("round scope not ended", "binding", b.Name, "round", round, "unit", unit, "err", err)
	}
}

// roundRunnerAlive reports whether the round still has a live runner. A pid of
// zero and a missing runner are not alive; an unreadable liveness check counts
// as alive, so relevo never ends a scope that might hold a working builder.
func roundRunnerAlive(ctx context.Context, rt Runtime, b store.Binding) bool {
	if b.Builder.PID == 0 || rt.Runner == nil {
		return false
	}
	alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
	if err != nil {
		slog.Warn("round liveness check failed; treating as alive", "binding", b.Name, "pid", b.Builder.PID, "err", err)
		return true
	}
	return alive
}

// endEarlierRoundScope ends the scope of the round before the one being sent,
// so a scope a dead supervisor left behind cannot run beside the new round. It
// refuses with ErrScopeActive when that scope is still loaded and could not be
// ended. A send of round 1, and a binding with scopes off, are no-ops.
func endEarlierRoundScope(ctx context.Context, rt Runtime, b store.Binding) error {
	if b.Round <= 1 {
		return nil
	}
	unit := scopeUnitNameFor(scopeRound, b.Owner, b.Name, b.Round-1, "")
	reaped, err := endScope(ctx, rt, unit)
	if err != nil {
		return fmt.Errorf("binding %q round %d: the previous round's scope %s.scope is still running and could not be ended; stop it with systemctl --user stop %s.scope, then send again: %w",
			b.Name, b.Round, unit, unit, ErrScopeActive)
	}
	if reaped {
		slog.Info("ended the previous round's scope before starting a new round", "binding", b.Name, "round", b.Round-1, "unit", unit)
	}
	return nil
}
