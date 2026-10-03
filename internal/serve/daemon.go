package serve

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// Tick advances every binding across all client owners.
//
// The owner list is snapshotted first and each owner is then reconciled outside
// s.mu: a slow or blocked owner therefore cannot stall another owner's poll,
// ack or round start. s.mu is not held across any Daemon.Tick call, so the only
// per-owner exclusion in the reconcile is each owner's own Store lock, which the
// daemon already takes per binding. A failing owner is still isolated: its error
// is logged and the walk continues.
func (s *Server) Tick(ctx context.Context) error {
	owners, err := s.ownerRoots()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	// Reconcile each owner on its own, with no server-wide lock held, so one
	// owner's slow builder cannot hold up another's.
	for _, owner := range owners {
		d := relevo.NewDaemon(s.runtimeAt(owner.root), s.cfg.Interval)
		if err := d.Tick(ctx); err != nil {
			slog.Error("tick owner failed", "owner", owner.id, "err", err)
		}
	}

	// Every owner is reconciled, so admit can start queued rounds into the slots
	// that freed up. collectSettled runs first, so a settled binding's slot and
	// worktree are released before reuse, and pruneUnusedRepos sees the
	// post-collect live set. The three run in that order under one admitMu hold,
	// which also keeps a concurrent round start's admit from interleaving with
	// this count-and-start.
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	if _, err := s.collectSettled(ctx); err != nil {
		slog.Warn("collect settled failed", "err", err)
	}
	s.pruneUnusedRepos(ctx)
	if err := s.admitLocked(ctx); err != nil {
		slog.Warn("admit failed", "err", err)
	}
	return nil
}

// ownerRoot is one owner the tick walks: the client id its directory names and
// the root its Store is rooted at.
type ownerRoot struct {
	id   remote.ClientID
	root string
}

// ownerRoots lists the owner roots under the bindings directory. A missing
// directory is not an error: a server with no owner bound yet has nothing to
// tick. The walk holds no lock -- the bindings directory is the source of truth
// for which owners exist, and it is read here exactly as every other per-owner
// walk reads it.
func (s *Server) ownerRoots() ([]ownerRoot, error) {
	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		return nil, err
	}

	owners := make([]ownerRoot, 0, len(entries))
	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			slog.Warn("unexpected entry in bindings dir", "entry", entry.Name())
			continue
		}
		owners = append(owners, ownerRoot{id: id, root: filepath.Join(bindingsDir, entry.Name())})
	}
	return owners, nil
}

// Run executes the daemon tick loop until ctx is cancelled. A tick in flight
// when ctx is cancelled runs to completion on a context cancellation cannot
// reach: a tick cut between Runner.Start and tx.Save would leave a live builder
// while the round still looked queued. tickFn, when non-nil, replaces Tick.
func (s *Server) Run(ctx context.Context) error {
	interval := s.cfg.Interval
	if interval < 500*time.Millisecond {
		interval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	tick := s.tickFn
	if tick == nil {
		tick = s.Tick
	}

	// settleAllServed takes each owner's own store lock in turn; no server-wide
	// lock is needed or wanted here, so a slow owner cannot hold up startup.
	if err := s.settleAllServed(); err != nil {
		slog.Warn("settle served reports failed", "err", err)
	}

	ticks := 0
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return ctx.Err()
		case <-ticker.C:
			ticks++
			s.mu.Lock()
			insecure := s.insecureHTTP
			s.mu.Unlock()
			// "every tick logs it" is read as once a minute (60 ticks at the
			// 1s floor): a warning per second would drown the log.
			if insecure && ticks%60 == 0 {
				slog.Warn("serving plain HTTP; every client request is readable on the network")
			}
			if err := tick(context.WithoutCancel(ctx)); err != nil {
				slog.Error("server tick failed", "err", err)
			}
			if ctx.Err() != nil {
				return nil
			}
		}
	}
}
