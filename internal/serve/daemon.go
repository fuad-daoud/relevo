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
func (s *Server) Tick(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			slog.Warn("unexpected entry in bindings dir", "entry", entry.Name())
			continue
		}
		ownerPath := filepath.Join(bindingsDir, entry.Name())
		rt := s.runtimeAt(ownerPath)
		d := relevo.NewDaemon(rt, s.cfg.Interval)
		if err := d.Tick(ctx); err != nil {
			slog.Error("tick owner failed", "owner", id, "err", err)
		}
	}

	// Every owner is reconciled, so admit can start queued rounds into the slots
	// that freed up. collectSettled runs first, so a settled binding's slot and
	// worktree are released before reuse. All three run under s.mu.
	if _, err := s.collectSettled(ctx); err != nil {
		slog.Warn("collect settled failed", "err", err)
	}
	s.pruneUnusedRepos(ctx)
	if err := s.admit(ctx); err != nil {
		slog.Warn("admit failed", "err", err)
	}
	return nil
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

	s.mu.Lock()
	if err := s.settleAllServed(); err != nil {
		slog.Warn("settle served reports failed", "err", err)
	}
	s.mu.Unlock()

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
