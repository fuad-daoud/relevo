package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// queuedRound is one queued round the census found, enough to start it.
type queuedRound struct {
	Owner     remote.ClientID
	OwnerRoot string // the owner's store root, what runtimeAt takes
	Name      string
	QueuedAt  time.Time
}

// census is the server's builder count at a point in time.
type census struct {
	Running int
	Queued  []queuedRound // sorted by QueuedAt, then Owner, then Name
}

// cap is the builder cap in effect: cfg.MaxBuilders if set, else the default.
// The flag wins over the policy because it was given on this process's command
// line; the policy half is the reloadable serve.max_builders, so a policy edit
// changes the cap on the next tick or admission.
func (s *Server) cap() int {
	if s.cfg.MaxBuilders > 0 {
		return s.cfg.MaxBuilders
	}
	return s.live().Policy.MaxBuildersOrDefault()
}

// census counts running headless builders and queued rounds across all owners.
// A per-owner List error is logged and skipped; the first is returned after the
// walk, and the census is still usable.
func (s *Server) census() (census, error) {
	var c census
	var firstErr error

	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}

	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			slog.Warn("unexpected entry in bindings dir", "entry", entry.Name())
			continue
		}
		ownerPath := filepath.Join(bindingsDir, entry.Name())
		bindings, err := s.ownerStore(ownerPath).List()
		if err != nil {
			slog.Warn("census: list owner bindings failed", "owner", id, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		for _, b := range bindings {
			switch {
			case b.Builder.Headless() && b.Builder.PID != 0 && b.State != store.StateDone && b.State != store.StatePaused:
				c.Running++
			case !b.QueuedAt.IsZero() && b.State == store.StateActive:
				c.Queued = append(c.Queued, queuedRound{
					Owner: id, OwnerRoot: ownerPath, Name: b.Name, QueuedAt: b.QueuedAt,
				})
			}
		}
	}

	sort.Slice(c.Queued, func(i, j int) bool {
		a, b := c.Queued[i], c.Queued[j]
		if !a.QueuedAt.Equal(b.QueuedAt) {
			return a.QueuedAt.Before(b.QueuedAt)
		}
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		return a.Name < b.Name
	})

	return c, firstErr
}

// admit starts queued rounds, oldest first, while the running count stays below
// cap. It takes admitMu for the whole count-and-start loop, so the server-wide
// cap is decided against one running count even when a tick's admit and a round
// start's admit overlap. The lock is server-wide on purpose: the cap is
// server-wide, so per-owner exclusion would not decide it.
func (s *Server) admit(ctx context.Context) error {
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	return s.admitLocked(ctx)
}

// admitLocked is admit for a caller already holding admitMu. The tick's
// collectSettled -> pruneUnusedRepos -> admit sequence runs under one hold, so
// Tick's unlocked per-owner reconcile cannot interleave between the count and
// the starts.
func (s *Server) admitLocked(ctx context.Context) error {
	c, cerr := s.census()
	running := c.Running
	limit := s.cap()

	for _, q := range c.Queued {
		if running >= limit {
			break
		}
		err := relevo.Admit(ctx, s.runtimeAt(q.OwnerRoot), q.Name)
		switch {
		case err == nil:
			// Admit succeeds for a round it started nothing for: a switch
			// whose replacement could not be resolved leaves the binding
			// broken with no process and returns no error, because that fault
			// is a halt entry owed and not a return value. Counting it as a
			// start would under-admit the rest of the pass by one, so the
			// count follows the binding's own state after the admit: a
			// process is what census counts as a builder.
			after, lerr := s.ownerStore(q.OwnerRoot).Load(q.Name)
			if lerr != nil {
				slog.Warn(fmt.Sprintf("admit owner=%s binding=%s: could not re-read the binding: %v", q.Owner, q.Name, lerr))
				continue
			}
			if after.Builder.PID == 0 {
				slog.Warn(fmt.Sprintf("admit owner=%s binding=%s: no process started; the slot stays free", q.Owner, q.Name))
				continue
			}
			running++
			slog.Info(fmt.Sprintf("admitted owner=%s binding=%s running=%d/%d", q.Owner, q.Name, running, limit))
		case errors.Is(err, relevo.ErrNotQueued):
			continue
		default:
			slog.Warn(fmt.Sprintf("admit owner=%s binding=%s: %v", q.Owner, q.Name, err))
		}
	}

	return cerr
}

// queuePositionView fills a queued round's place in the queue. A round not found
// in a fresh census (raced -- admitted or unbound since the view) is nil. The
// census is a read-only walk, so it takes no admitMu: a poll must not queue
// behind another owner's admit.
func (s *Server) queuePositionView(b store.Binding, view remote.BindingView, caller remote.ClientID) *remote.QueueView {
	if view.RoundState != remote.RoundQueued {
		return nil
	}
	c, _ := s.census()
	for i, q := range c.Queued {
		if q.Owner == caller && q.Name == b.Name {
			return &remote.QueueView{Position: i + 1, Ahead: i, Running: c.Running, Cap: s.cap(), Since: b.QueuedAt}
		}
	}
	return nil
}
