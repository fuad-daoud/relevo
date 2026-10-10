package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// idlePoll is how often the idle watcher samples the daemon's activity.
const idlePoll = 30 * time.Second

// daemonActivity is one sample of what the daemon has to do: live owner
// connections, a locally running builder or gate, a queued round, whether sync
// is turned on, and whether the state root still exists.
type daemonActivity struct {
	Conns   int
	Running bool
	Queued  bool
	// SyncOn keeps the daemon alive: a machine whose sync is on needs a process
	// to export and pull, and an idle exit would stop both until the next
	// command happened to start one.
	SyncOn bool
	// Joining is an enable's join running in this process. The mark goes on
	// only when the join ends, and a first join that uploads every large body
	// can outlast the idle period with no client connected.
	Joining    bool
	RootExists bool
}

// busy is whether anything keeps the daemon from idling out.
func (a daemonActivity) busy() bool {
	return a.Conns > 0 || a.Running || a.Queued || a.SyncOn || a.Joining
}

// idleExit decides whether an idle daemon should exit: the root is gone, or
// nothing is busy and the idle period has elapsed. It is pure, so a table test
// can pin every condition.
func idleExit(a daemonActivity, idleFor, after time.Duration) bool {
	if !a.RootExists {
		return true
	}
	if a.busy() {
		return false
	}
	if after <= 0 {
		return false
	}
	return idleFor >= after
}

// daemonActivityNow samples the daemon's current activity. The root is stat'ed
// first: a missing root is what a deleted throwaway state root looks like, and
// must exit the daemon at once. A nil owner server counts as no connections --
// there is no socket a client could use, and the sampler must never
// dereference it -- and a store read that fails is not idle: an unreadable root
// must never read as "nothing to do".
func daemonActivityNow(root string, srv *owner.Server, st *store.Store) daemonActivity {
	var a daemonActivity
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return a
		}
		slog.Warn("relevo daemon: idle stat root", "root", root, "err", err)
		a.Running = true
		return a
	}
	a.RootExists = true

	if srv != nil {
		a.Conns = srv.ConnCount()
	}
	if st != nil {
		bindings, err := st.List()
		if err != nil {
			slog.Warn("relevo daemon: idle read bindings", "err", err)
			a.Running = true
			return a
		}
		for _, b := range bindings {
			if b.Builder.Headless() && b.Builder.PID != 0 {
				a.Running = true
			}
			if b.GateRun != nil {
				a.Running = true
			}
			if !b.QueuedAt.IsZero() || b.Builder.RemoteQueue != nil {
				a.Queued = true
			}
		}
		a.SyncOn = syncSampledOn(st)
	}
	return a
}

// syncSampledOn reads the machine-local enabled marker. A machine with no
// database, or no local file beside it, cannot be syncing. A read that fails is
// busy rather than idle, for the same reason an unreadable root is: the daemon
// must not exit on a sample it could not take.
func syncSampledOn(st *store.Store) bool {
	mdb, err := st.DBIfExists()
	if err != nil {
		slog.Warn("relevo daemon: idle open database", "err", err)
		return true
	}
	if mdb == nil {
		return false
	}
	local, err := relevosync.LocalHandle(mdb)
	if err != nil {
		return false
	}
	state, err := relevosync.ReadState(local)
	if err != nil {
		slog.Warn("relevo daemon: idle read sync marker", "err", err)
		return true
	}
	return state.Enabled
}

// watchDaemonIdle cancels the daemon's own context once it has been idle for
// after. The first idle sample starts the clock, any busy sample resets it, and
// the exit lands on the first sample at or after the period. ctx done returns.
// A nil now is time.Now, so the caller may pass the runtime's clock as-is.
func watchDaemonIdle(ctx context.Context, cancel context.CancelFunc, after, poll time.Duration, now func() time.Time, sample func() daemonActivity) {
	if now == nil {
		now = time.Now
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	var idleSince time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a := sample()
			if a.RootExists && a.busy() {
				idleSince = time.Time{}
				continue
			}
			if idleSince.IsZero() {
				idleSince = now()
			}
			if idleExit(a, now().Sub(idleSince), after) {
				slog.Info("relevo daemon: idle, exiting", "after", after)
				cancel()
				return
			}
		}
	}
}
