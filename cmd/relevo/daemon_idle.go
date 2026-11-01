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

const idlePoll = 30 * time.Second

// daemonActivity is one sample of what the daemon has to do: live owner
// connections, a locally running builder or gate, a queued round, whether sync
// is turned on, and whether the state root is still the one this daemon locked.
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
	Joining  bool
	RootHeld bool
}

func (a daemonActivity) busy() bool {
	return a.Conns > 0 || a.Running || a.Queued || a.SyncOn || a.Joining
}

// idleExit decides whether an idle daemon exits: the root is gone or
// displaced, or nothing is busy and the idle period elapsed. It is pure, so a
// table test pins every condition.
func idleExit(a daemonActivity, idleFor, after time.Duration) bool {
	if !a.RootHeld {
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

// daemonActivityNow samples the daemon's activity, stat'ing the root first: a
// missing root or a displaced lock must exit at once, and an unreadable root
// must never read as idle, so a failed read counts as busy.
func daemonActivityNow(root string, lock *store.DaemonLock, srv *owner.Server, st *store.Store) daemonActivity {
	var a daemonActivity
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return a
		}
		slog.Warn("relevo daemon: idle stat root", "root", root, "err", err)
		a.Running = true
		return a
	}
	if lock.Displaced() {
		slog.Warn("relevo daemon: state root re-created under the daemon", "root", root)
		return a
	}
	a.RootHeld = true

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

// syncSampledOn reads the machine-local enabled marker; a failed read counts
// as busy, so the daemon never exits on a sample it could not take.
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

// watchDaemonIdle cancels the daemon's context once it has been idle for
// after: the first idle sample starts the clock and any busy sample resets
// it. A nil now is time.Now.
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
			if a.RootHeld && a.busy() {
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
