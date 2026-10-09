package ui

import (
	"context"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/view"
)

// liveRuntime is the one runtime a mastermind cockpit reads: the fleet refresh
// and every write go through it, so a config change made in one view, or in
// another terminal, reaches every view without a restart.
type liveRuntime struct {
	mu      sync.Mutex
	rt      relevo.Runtime
	version int64
}

// newLiveRuntime starts a holder on rt. An absent config store, or one that
// cannot report a version, is recorded as -1 so the first Refresh always
// tries to load.
func newLiveRuntime(rt relevo.Runtime) *liveRuntime {
	l := &liveRuntime{rt: rt, version: -1}
	if rt.Config != nil {
		if v, err := rt.Config.Version(); err == nil {
			l.version = v
		}
	}
	return l
}

// Get returns the current snapshot. Renders reach it through Base, so it only
// takes the lock and never touches the store.
func (l *liveRuntime) Get() relevo.Runtime {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rt
}

// Refresh reloads the four config sections when the store's version counter
// moved. Version is read and ReloadConfig runs with mu released, because a
// render calling Get must never wait behind I/O. The swap happens only when
// the version read is greater than the stored one: the poll tick and a config
// write can race, and a reload must never install an older snapshot over a
// newer one. A load error leaves the last good snapshot in place.
func (l *liveRuntime) Refresh() error {
	l.mu.Lock()
	snap, seen := l.rt, l.version
	l.mu.Unlock()

	if snap.Config == nil {
		return nil
	}
	v, err := snap.Config.Version()
	if err != nil {
		return err
	}
	if v == seen {
		return nil
	}
	next, err := relevo.ReloadConfig(snap)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if v > l.version {
		l.rt.Candidates = next.Candidates
		l.rt.Policy = next.Policy
		l.rt.Registry = next.Registry
		l.rt.ConfigWarnings = next.ConfigWarnings
		l.version = v
	}
	return nil
}

// liveSource is the Source `relevo ui` runs on: every read goes through the
// shared runtime, so the fleet refresh and the tab views always agree.
type liveSource struct {
	live *liveRuntime
}

// Status reloads the shared runtime, then reports the fleet. A reload error is
// dropped: the last good snapshot still answers, and the config screens report
// a load failure themselves.
//
// The report is built with the detail figures off -- Live, LiveUsage and
// Headless.Tail -- because the fleet view paints none of them; the detail pane
// fetches its own row with them on. Every other field is what the same store
// read returns with them on.
func (s liveSource) Status(ctx context.Context) (view.Report, error) {
	_ = s.live.Refresh()
	return relevo.Status(ctx, s.live.Get(), relevo.Detail(false))
}

func (s liveSource) Runtime(key string) (relevo.Runtime, string, bool) {
	return s.live.Get(), key, true
}

func (s liveSource) Base() relevo.Runtime {
	return s.live.Get()
}

// MarkViewed writes through to the mastermind's own store, key being the row's
// bare binding name; errors are dropped as on mastermindSource.
func (s liveSource) MarkViewed(key string) {
	_ = s.live.Get().Store.MarkViewed(key, time.Now())
}
