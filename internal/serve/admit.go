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
	Planner   bool // the binding is a planner round, admitted against its own cap
}

type census struct {
	// Running counts running builder rounds; planners run under their own
	// cap and are counted in PlannersRunning, so a builder seat never
	// holds a planner round.
	Running         int
	PlannersRunning int
	Queued          []queuedRound // sorted by QueuedAt, then Owner, then Name
}

// plannersQueued counts the census's queued planner rounds; the rest of
// Queued are builders.
func (c census) plannersQueued() int {
	n := 0
	for _, q := range c.Queued {
		if q.Planner {
			n++
		}
	}
	return n
}

// buildersViewFor stamps the census counts onto a fresh builders view: the
// builder queue is the FIFO length minus queued planners, and planners read
// their own class counts, so each surface reports its own cap.
func (s *Server) buildersViewFor(c census) remote.BuildersView {
	v := s.isolationView(remote.BuildersView{Cap: s.cap()})
	pq := c.plannersQueued()
	v.Running = c.Running
	v.Queued = len(c.Queued) - pq
	v.PlannersRunning = c.PlannersRunning
	v.PlannersQueued = pq
	v.PlannersCap = s.plannerCap()
	return v
}

// queueViewFor is the queued position row for one census entry: a planner
// reads its own class count and cap while Position and Ahead keep their
// global FIFO meaning.
func queueViewFor(q queuedRound, i int, c census, v remote.BuildersView) *remote.QueueView {
	if q.Planner {
		return &remote.QueueView{Position: i + 1, Ahead: i, Running: c.PlannersRunning, Cap: v.PlannersCap, Since: q.QueuedAt}
	}
	return &remote.QueueView{Position: i + 1, Ahead: i, Running: c.Running, Cap: v.Cap, Since: q.QueuedAt}
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

// plannerCap is the planner cap in effect: the reloadable serve.max_planners,
// so a policy edit changes the cap on the next tick or admission. Planners
// never take the --max-builders flag: that flag caps builder seats only.
func (s *Server) plannerCap() int {
	return s.live().Policy.MaxPlannersOrDefault()
}

// walkOwners lists every owner's bindings and hands them to visit. A per-owner
// List error is logged and skipped; the first is returned after the walk.
func (s *Server) walkOwners(visit func(id remote.ClientID, ownerPath string, bindings []store.Binding)) error {
	var firstErr error

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
			warnStrayBindingsEntry(entry.Name())
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
		visit(id, ownerPath, bindings)
	}
	return firstErr
}

// inFlightFor is the machine-wide live-round count per candidate for a runtime
// rooted at root: every owner's bindings, with the binding named by except
// left out only in root's own store, because binding names repeat across
// owners.
func (s *Server) inFlightFor(root string) func(string) map[string]int {
	return func(except string) map[string]int {
		out := map[string]int{}
		_ = s.walkOwners(func(_ remote.ClientID, ownerPath string, bindings []store.Binding) {
			skip := ""
			if ownerPath == root {
				skip = except
			}
			for tok, n := range relevo.CountInFlight(bindings, skip) {
				out[tok] += n
			}
		})
		return out
	}
}

// census counts running headless builders and queued rounds across all owners.
// A per-owner List error is logged and skipped; the first is returned after the
// walk, and the census is still usable. The registry is the live snapshot's,
// resolved once, so every binding in the walk classifies against one registry.
func (s *Server) census() (census, error) {
	var c census
	reg := s.live().Registry
	firstErr := s.walkOwners(func(id remote.ClientID, ownerPath string, bindings []store.Binding) {
		for _, b := range bindings {
			switch {
			case relevo.LiveBuilder(b):
				if relevo.IsPlannerRound(reg, b) {
					c.PlannersRunning++
				} else {
					c.Running++
				}
			case !b.QueuedAt.IsZero() && b.State == store.StateActive:
				c.Queued = append(c.Queued, queuedRound{
					Owner: id, OwnerRoot: ownerPath, Name: b.Name, QueuedAt: b.QueuedAt,
					Planner: relevo.IsPlannerRound(reg, b),
				})
			}
		}
	})

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
	planners := c.PlannersRunning
	limit := s.cap()
	plannerLimit := s.plannerCap()

	for _, q := range c.Queued {
		// Each class holds its own cap, and a capped class only skips its
		// own rounds: the queue stays one FIFO, so a planner behind queued
		// builders still starts while builders wait on their cap.
		if q.Planner {
			if planners >= plannerLimit {
				continue
			}
		} else if running >= limit {
			continue
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
			if q.Planner {
				planners++
			} else {
				running++
			}
			slog.Info(fmt.Sprintf("admitted owner=%s binding=%s running=%d/%d planners=%d/%d", q.Owner, q.Name, running, limit, planners, plannerLimit))
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
			// The viewed round reads its own class's count and cap; Position
			// and Ahead keep their global FIFO meaning, which the single
			// pass over the shared queue preserves.
			if q.Planner {
				return &remote.QueueView{Position: i + 1, Ahead: i, Running: c.PlannersRunning, Cap: s.plannerCap(), Since: b.QueuedAt}
			}
			return &remote.QueueView{Position: i + 1, Ahead: i, Running: c.Running, Cap: s.cap(), Since: b.QueuedAt}
		}
	}
	return nil
}
