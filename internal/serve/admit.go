package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// queuedRound is one queued round the census found, enough to start it. Base
// and Burst are its seat from cost history, so the pass decides without a
// second lookup.
type queuedRound struct {
	Owner     remote.ClientID
	OwnerRoot string // the owner's store root, what runtimeAt takes
	Name      string
	QueuedAt  time.Time
	Planner   bool // the binding is a planner round, admitted against its own cap
	Base      int64
	Burst     int64
}

// runningRound is one running round the census found: its seat for the
// memory math and its pid for the budget's non-round reading.
type runningRound struct {
	PID     int // the builder's pid, what the round's cgroup dir resolves from
	Planner bool
	Base    int64
	Burst   int64
}

type census struct {
	// Running counts running builder rounds; planners run under their own
	// cap and are counted in PlannersRunning, so a builder seat never
	// holds a planner round.
	Running         int
	PlannersRunning int
	// BuilderCap and PlannerCap snapshot the caps this pass decides
	// against, so the decision and the queued waits read one pair of
	// numbers.
	BuilderCap int
	PlannerCap int
	// RunningRounds carries every running round's seat and pid: the pass's
	// memory math and the budget's non-round reading share one lookup.
	RunningRounds []runningRound
	Queued        []queuedRound // sorted by QueuedAt, then Owner, then Name
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
// their own class counts, so each surface reports its own cap. The seat
// reservation rides along from the caller's own admission inputs, so the view
// and the decision read one budget and K.
func (s *Server) buildersViewFor(c census, budget int64, k int) remote.BuildersView {
	v := s.isolationView(remote.BuildersView{Cap: s.cap()})
	pq := c.plannersQueued()
	v.Running = c.Running
	v.Queued = len(c.Queued) - pq
	v.PlannersRunning = c.PlannersRunning
	v.PlannersQueued = pq
	v.PlannersCap = s.plannerCap()
	v.ReservedBytes = reservedBytes(c.RunningRounds, k)
	v.BudgetBytes = budget
	v.K = k
	return v
}

// queueViewFor is the queued position row for one census entry: a planner
// reads its own class count and cap while Position and Ahead keep their
// global FIFO meaning. waits runs aligned with c.Queued: a held round stamps
// its reason and shortfall, and a round the pass would start keeps none, so
// no stale wait survives a start.
func queueViewFor(q queuedRound, i int, c census, v remote.BuildersView, waits []queueWait) *remote.QueueView {
	var view *remote.QueueView
	if q.Planner {
		view = &remote.QueueView{Position: i + 1, Ahead: i, Running: c.PlannersRunning, Cap: v.PlannersCap, Since: q.QueuedAt}
	} else {
		view = &remote.QueueView{Position: i + 1, Ahead: i, Running: c.Running, Cap: v.Cap, Since: q.QueuedAt}
	}
	if i < len(waits) && waits[i].Wait != "" {
		view.Wait = waits[i].Wait
		view.WaitBytes = waits[i].WaitBytes
	}
	return view
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

// pressureMaxAvg10 is the memory some-stall level that holds admission: at a
// tenth of the window stalled the host is already saturated, so a new round
// would only add pressure.
const pressureMaxAvg10 = 10.0

// liveBudgetDir answers the cgroup dir the memory budget reads: the serve
// runs in its own service cgroup while the limit sits on the parent slice,
// so the walk starts at the serve's own cgroup and takes the nearest ancestor
// holding a numeric memory.max. A variable so tests fake the tree.
var liveBudgetDir = func() (string, error) {
	rel, err := proc.CgroupRelPath(os.Getpid())
	if err != nil {
		return "", err
	}
	return findBudgetDir(filepath.Join("/sys/fs/cgroup", rel)), nil
}

// liveRoundCgroupDir answers one running round's cgroup dir from its pid. A
// variable so tests fake pids no /proc entry names.
var liveRoundCgroupDir = func(pid int) (string, error) {
	rel, err := proc.CgroupRelPath(pid)
	if err != nil {
		return "", err
	}
	return filepath.Join("/sys/fs/cgroup", rel), nil
}

// liveMemTotal reads machine memory. A variable so tests fake the host.
var liveMemTotal = proc.MemTotal

// findBudgetDir walks from start up to the nearest ancestor, itself included,
// whose memory.max holds a number. A tree with no numeric limit reads as
// start itself, so the caller falls back to the machine-memory budget with
// the serve's own pressure.
func findBudgetDir(start string) string {
	dir := start
	for {
		if _, limited, err := proc.MemoryMax(dir); err == nil && limited {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// underDir reports whether path lies inside dir. A round scoped outside the
// budget slice must not shrink this slice's non-round reading.
func underDir(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// anonMinusRounds answers the budget dir's anon bytes minus the anon of each
// running round scoped inside it. Page cache is reclaimable and never counts
// against rounds, so only anon reads on either side. An unreadable dir reads
// as zero: a transient read failure must not wedge the next pass, which
// re-reads.
func anonMinusRounds(dir string, running []runningRound) int64 {
	anon, err := proc.MemoryStatAnon(dir)
	if err != nil {
		return 0
	}
	for _, r := range running {
		rdir, rerr := liveRoundCgroupDir(r.PID)
		if rerr != nil || !underDir(rdir, dir) {
			continue
		}
		if ra, raerr := proc.MemoryStatAnon(rdir); raerr == nil {
			anon -= ra
		}
	}
	return max(anon, 0)
}

// admissionInputs reads one pass's memory budget, parallelism and pressure
// brake. The override wins; otherwise the budget slice minus the non-round
// anon inside it, else machine memory minus the reserve. The brake reads the
// budget dir's some-stall average, and any read failure there is brake-off:
// a missing pressure file must not hold every round. Container rounds have
// no scope and their pid's cgroup may be unreadable; that path subtracts
// nothing and never fails the pass.
func (s *Server) admissionInputs(running []runningRound) (budget int64, k int, pressureOK bool) {
	override := s.live().Policy.ServeMemoryBudgetBytes()
	var sliceMax, nonRound int64
	dir, derr := liveBudgetDir()
	if derr == nil {
		if limit, limited, merr := proc.MemoryMax(dir); merr == nil && limited {
			sliceMax = limit
		}
		nonRound = anonMinusRounds(dir, running)
	}
	memTotal, merr := liveMemTotal()
	if merr != nil {
		memTotal = 0
	}
	budget = seatBudget(sliceMax, nonRound, memTotal, override)
	pressureOK = true
	if derr == nil {
		if avg, perr := proc.MemoryPressureSomeAvg10(dir); perr == nil {
			pressureOK = avg < pressureMaxAvg10
		}
	}
	// K rides the bursting-count history, so the burst term tracks live
	// parallelism instead of the cold-start 2. An unreadable file reads as no
	// history: the pass keeps its old K rather than failing.
	var counts []int
	if samples, err := loadKHistory(s.cfg.Root); err != nil {
		slog.Warn("k-history unreadable, using no counts", "err", err)
	} else {
		counts = kCounts(samples)
	}
	// A thin history reads as no history: a few quiet minutes are not
	// evidence of low parallelism, the same way lookupSeat sits on its
	// defaults below seatMinSamples. Without this the first quiet minutes
	// would collapse the cold-start K before rounds ever burst together.
	if len(counts) < kMinSamples {
		counts = nil
	}
	k = estimateK(counts, s.cap()+s.plannerCap())
	return budget, k, pressureOK
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
// Costs come from each binding's repo-role history, looked up once here, so
// the pass and the views never re-read a cost file.
func (s *Server) census() (census, error) {
	var c census
	c.BuilderCap = s.cap()
	c.PlannerCap = s.plannerCap()
	reg := s.live().Registry
	firstErr := s.walkOwners(func(id remote.ClientID, ownerPath string, bindings []store.Binding) {
		for _, b := range bindings {
			planner := relevo.IsPlannerRound(reg, b)
			switch {
			case relevo.LiveBuilder(b):
				base, burst := seatCostFor(s.cfg.Root, reg, b)
				c.RunningRounds = append(c.RunningRounds, runningRound{PID: b.Builder.PID, Planner: planner, Base: base, Burst: burst})
				if planner {
					c.PlannersRunning++
				} else {
					c.Running++
				}
			case !b.QueuedAt.IsZero() && b.State == store.StateActive:
				base, burst := seatCostFor(s.cfg.Root, reg, b)
				c.Queued = append(c.Queued, queuedRound{
					Owner: id, OwnerRoot: ownerPath, Name: b.Name, QueuedAt: b.QueuedAt,
					Planner: planner, Base: base, Burst: burst,
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
	runningCosts := runningCostsOf(c.RunningRounds)
	budget, k, pressureOK := s.admissionInputs(c.RunningRounds)
	now := s.cfg.Now()
	var blockedBuilder, blockedPlanner bool

	for _, q := range c.Queued {
		// Each class holds its own cap, and a capped class only skips its
		// own rounds: the queue stays one FIFO, so a planner behind queued
		// builders still starts while builders wait on their cap.
		classCount, ceiling := running, c.BuilderCap
		blocked := blockedBuilder
		if q.Planner {
			classCount, ceiling = planners, c.PlannerCap
			blocked = blockedPlanner
		}
		if blocked {
			continue
		}
		start, wait, _ := decide(runningCosts, seatCost{Base: q.Base, Burst: q.Burst}, budget, k, classCount, ceiling, pressureOK)
		if !start {
			if wait == waitMemory && waitedTooLong(q.QueuedAt, now) {
				if q.Planner {
					blockedPlanner = true
				} else {
					blockedBuilder = true
				}
			}
			continue
		}
		// The count follows the binding's own state after the admit: a
		// process is what census counts as a builder, so a start that
		// produced none advances neither the counts nor the memory slice.
		if !s.admitOne(ctx, q) {
			continue
		}
		if q.Planner {
			planners++
		} else {
			running++
		}
		runningCosts = append(runningCosts, seatCost{Base: q.Base, Burst: q.Burst})
		slog.Info(fmt.Sprintf("admitted owner=%s binding=%s running=%d/%d planners=%d/%d", q.Owner, q.Name, running, c.BuilderCap, planners, c.PlannerCap))
	}

	return cerr
}

// admitOne starts one queued round, reporting whether a process came up. An
// admit for a round it started nothing for is not a start: a switch whose
// replacement could not be resolved leaves the binding broken with no process
// and returns no error, because that fault is a halt entry owed and not a
// return value. Counting it would under-admit the rest of the pass by one.
func (s *Server) admitOne(ctx context.Context, q queuedRound) bool {
	err := relevo.Admit(ctx, s.runtimeAt(q.OwnerRoot), q.Name)
	switch {
	case err == nil:
		after, lerr := s.ownerStore(q.OwnerRoot).Load(q.Name)
		if lerr != nil {
			slog.Warn(fmt.Sprintf("admit owner=%s binding=%s: could not re-read the binding: %v", q.Owner, q.Name, lerr))
			return false
		}
		if after.Builder.PID == 0 {
			slog.Warn(fmt.Sprintf("admit owner=%s binding=%s: no process started; the slot stays free", q.Owner, q.Name))
			return false
		}
		return true
	case errors.Is(err, relevo.ErrNotQueued):
		return false
	default:
		slog.Warn(fmt.Sprintf("admit owner=%s binding=%s: %v", q.Owner, q.Name, err))
		return false
	}
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
			budget, k, pressureOK := s.admissionInputs(c.RunningRounds)
			waits := queueWaits(c, budget, k, pressureOK, s.cfg.Now())
			return queueViewFor(q, i, c, s.buildersViewFor(c, budget, k), waits)
		}
	}
	return nil
}
