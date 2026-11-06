package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The burst hook runs at the end of every tick, after admit, still under
// admitMu: local seats fill first and a placement never races a concurrent
// admission. The provider exchange runs without s.mu -- a hung provider must
// not wedge the request handlers -- while every state change applies under
// the lock only after re-checking the table it was built on.

// burstOn reports whether the hook may act. No provider means no command was
// configured; no client key means New refused the configured command, so an
// injected provider alone never turns the loop on.
func (s *Server) burstOn() bool {
	return s.burstProvider != nil && len(s.cfg.ClientKey) > 0
}

// burstTick requeues what dead nodes lost, places waiting rounds onto live
// nodes as sending forwards, sends and observes those, checks new nodes, and
// reports the load. The order is the pipeline: a round the requeue just
// freed can be placed this tick, what placement just claimed is sent and
// observed this tick, and a node the verify pass just marked is reported to
// the provider on this same answer. Callers hold admitMu; the provider and
// node calls keep it while dropping s.mu, so a hung peer stalls the tick up
// to its own timeout after local admit already ran.
func (s *Server) burstTick(ctx context.Context) {
	if !s.burstOn() {
		return
	}
	now := s.cfg.Now().UTC()
	s.requeueLost(ctx, now)
	placed := s.placeForwards(now)
	confirmed := s.sendForwards(ctx, now)
	closed := s.observeForwards(ctx)
	s.verifyNodes(ctx)
	s.reportCycle(now, placed+confirmed, closed)
}

// placeForwards claims queued rounds for live nodes after admit ran, so the
// head's own seats fill first. The first localFree queued rounds are the
// head's: they emit no placement. Each claim records a sending forward and
// clears QueuedAt, keeping the FIFO place in QueuedSince for the requeue.
// Nothing here dials a node; lane C sends what this marks.
func (s *Server) placeForwards(now time.Time) int {
	t := s.copyBurstTable()
	if t == nil {
		return 0
	}
	c, _ := s.census()
	if len(c.Queued) == 0 {
		return 0
	}
	waiting, index := s.burstWaiting(c)
	waiting = s.withoutHeldRefusals(waiting, now)
	counts := map[string]int{}
	queued := map[string]int{}
	for _, f := range s.burstForwards() {
		switch f.State {
		case burst.ForwardRunning:
			counts[f.NodeID]++
		case burst.ForwardQueued:
			queued[f.NodeID]++
		}
	}
	nodes := make([]burst.PlaceNode, 0, len(t.Nodes))
	for _, r := range t.Nodes {
		if r.Gone {
			continue
		}
		nodes = append(nodes, burst.PlaceNode{
			ID: r.ID, Verified: r.Verified, Problem: r.Problem,
			State: r.State, Cap: r.Cap, Running: counts[r.ID], Queued: queued[r.ID],
		})
	}
	placed := 0
	for _, p := range burst.Place(s.cap()-c.Running, waiting, nodes) {
		if s.placeOne(index[p.Round.ID], p.NodeID, now) {
			placed++
		}
	}
	return placed
}

// placeOne claims q for nodeID. The binding is re-read and the claim lands
// only while the round is still queued where the census saw it.
func (s *Server) placeOne(q queuedRound, nodeID string, now time.Time) bool {
	st := s.ownerStore(q.OwnerRoot)
	b, err := st.Load(q.Name)
	if err != nil {
		slog.Warn("burst: place round: load failed", "owner", string(q.Owner), "binding", q.Name, "err", err)
		return false
	}
	if b.State != store.StateActive || b.QueuedAt.IsZero() || !b.QueuedAt.Equal(q.QueuedAt) {
		return false
	}
	if burstStoredForward(b) != nil {
		return false
	}
	if b.Burst == nil {
		b.Burst = &store.BurstFacts{}
	}
	b.Burst.Forward = &store.Forward{
		Node: nodeID, NodeBinding: burstNodeBinding(q.Owner, q.Name),
		Round: b.Round, Phase: "sending", ClaimedAt: now, QueuedSince: b.QueuedAt,
	}
	b.QueuedAt = time.Time{}
	if err := st.Save(b); err != nil {
		slog.Warn("burst: place round: save failed", "owner", string(q.Owner), "binding", q.Name, "err", err)
		return false
	}
	slog.Info("burst: placed a round on a node", "owner", string(q.Owner), "binding", q.Name, "node", nodeID)
	return true
}

// reportCycle sends the load to the provider when one is owed and folds the
// answer into the node table. The request is built from a table snapshot and
// the provider runs with no lock held; the answer applies under s.mu only
// while the table still carries the snapshot's mark, so a report that raced
// another is dropped instead of merged twice.
func (s *Server) reportCycle(now time.Time, forwarded, closed int) {
	t := s.copyBurstTable()
	if t == nil {
		return
	}
	c, _ := s.census()
	waiting, _ := s.burstWaiting(c)
	forwards := s.burstForwards()
	reason := burst.ReportReasonInterval
	switch {
	case closed > 0:
		reason = burst.ReportReasonClosed
	case forwarded > 0:
		reason = burst.ReportReasonForwarded
	case t.LastReport.IsZero():
		reason = burst.ReportReasonStartup
	}
	if !t.Due(now, len(waiting) > 0, reason) {
		return
	}
	// Local and waiting come from the same planner-free set: census.Running
	// counts builder seats only, and burstWaiting leaves planners out.
	req, notes, err := burst.BuildReport(burst.Census{
		Head: s.burstHead(), Local: burst.ReportLocal{Running: c.Running, Queued: len(waiting), Cap: s.cap()},
		Waiting: waiting,
	}, *t, now, reason, forwards)
	if err != nil {
		slog.Warn("burst: report not built", "err", err)
		return
	}
	for _, n := range notes {
		slog.Info("burst: report normalized a field", "field", n)
	}
	resp, rnotes, rerr := s.burstProvider.Report(req)
	for _, n := range rnotes {
		slog.Info("burst: provider normalized a field", "field", n)
	}
	s.applyReport(t, now, resp, rerr)
}

// applyReport folds one provider answer into the table. A failed call keeps
// the last good list and waits a full interval: RecordFailure advances the
// attempt mark while leaving rows, blocks and cadence on the last good
// response.
func (s *Server) applyReport(snap *burst.NodeTable, now time.Time, resp burst.ReportResponse, rerr error) {
	s.mu.Lock()
	if s.burstTable == nil || !s.burstTable.LastReport.Equal(snap.LastReport) {
		s.mu.Unlock()
		slog.Warn("burst: dropping a report the table moved past")
		return
	}
	var events []burst.NodeEvent
	if rerr != nil {
		s.burstTable.RecordFailure(now, rerr.Error())
	} else {
		events = s.burstTable.Merge(now, resp)
	}
	cp := *s.burstTable
	cp.Nodes = append([]burst.NodeRow(nil), s.burstTable.Nodes...)
	s.mu.Unlock()
	if rerr != nil {
		slog.Warn("burst: report failed, keeping the last good node list", "err", rerr)
	} else {
		for _, e := range events {
			slog.Info("burst: node changed", "kind", e.Kind, "node", e.NodeID)
		}
	}
	if err := saveBurstTable(s.cfg.DB.LocalOrSelf(), &cp); err != nil {
		slog.Warn("burst: node table not saved", "err", err)
	}
}

// burstHead names this head for the report. Memory has no inventory behind
// it, so it stays zero; the contract accepts it.
func (s *Server) burstHead() burst.ReportHead {
	name := s.cfg.Installation.Label
	if name == "" {
		name, _ = os.Hostname()
	}
	if name == "" {
		name = "relevo-head"
	}
	return burst.ReportHead{Name: name, CPUs: runtime.NumCPU()}
}

// burstWaiting lists the queued FIFO the way the provider counts it. A round
// whose binding vanished since the census still waits as far as the head
// knows; it carries no candidate rather than dropping the load. Planner
// rounds are left out: they run on the head under their own cap and are
// never claimed for a node, so the provider is never asked for capacity
// for rounds that need none.
func (s *Server) burstWaiting(c census) ([]burst.ReportWaiting, map[string]queuedRound) {
	waiting := make([]burst.ReportWaiting, 0, len(c.Queued))
	index := make(map[string]queuedRound, len(c.Queued))
	for _, q := range c.Queued {
		if q.Planner {
			continue
		}
		id := string(q.Owner) + "/" + q.Name
		entry := burst.ReportWaiting{ID: id, Since: q.QueuedAt.UTC()}
		if b, err := s.ownerStore(q.OwnerRoot).Load(q.Name); err == nil && b.BuilderCandidate != "" {
			entry.Candidate = b.BuilderCandidate
			if ref, rerr := candidate.ParseRef(b.BuilderCandidate); rerr == nil {
				entry.Harness = ref.Harness
			}
		}
		waiting = append(waiting, entry)
		index[id] = q
	}
	return waiting, index
}

// burstForwards maps every stored forward onto the report's states. A
// node-view queued nuance belongs to the observe half; here a sending forward
// already holds its seat, so sending counts as running. An unknown phase is
// left out and logged rather than refusing the whole report.
func (s *Server) burstForwards() []burst.Forward {
	var out []burst.Forward
	now := s.cfg.Now().UTC()
	_ = s.walkOwners(func(id remote.ClientID, _ string, bindings []store.Binding) {
		for _, b := range bindings {
			fwd := burstStoredForward(b)
			if fwd == nil {
				continue
			}
			state, ok := burstForwardState(fwd.Phase)
			if !ok {
				slog.Warn("burst: forward in an unknown phase, left out of the report",
					"owner", string(id), "binding", b.Name, "phase", fwd.Phase)
				continue
			}
			started := fwd.ClaimedAt
			if started.IsZero() {
				started = fwd.QueuedSince
			}
			if started.IsZero() {
				started = now
			}
			out = append(out, burst.Forward{
				Round: burst.ReportRound{
					ID: string(id) + "/" + b.Name, Candidate: b.BuilderCandidate, Started: started.UTC(),
				},
				NodeID: fwd.Node, State: state,
			})
		}
	})
	return out
}

// burstForwardState maps the stored phase onto the report's states.
func burstForwardState(phase string) (string, bool) {
	switch phase {
	case "sending", "running":
		return burst.ForwardRunning, true
	case "closed":
		return burst.ForwardUnacked, true
	}
	return "", false
}

// copyBurstTable snapshots the node table for a lock-free build. Nil when
// bursting is off, which keeps every later step a no-op.
func (s *Server) copyBurstTable() *burst.NodeTable {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.burstTable == nil {
		return nil
	}
	cp := *s.burstTable
	cp.Nodes = append([]burst.NodeRow(nil), s.burstTable.Nodes...)
	return &cp
}

// burstStoredForward reads the binding's forward, if any.
func burstStoredForward(b store.Binding) *store.Forward {
	if b.Burst == nil {
		return nil
	}
	return b.Burst.Forward
}

// burstOwner8 is the owner half of a node binding name: the first 8 hex of
// the owning client's id, "local" for a binding with no owner. Two owners
// may bind the same name, so the node side namespaces by this.
func burstOwner8(owner remote.ClientID) string {
	if owner == "" {
		return "local"
	}
	if dir, ok := owner.Dir(); ok {
		return dir[:8]
	}
	return "unknown"
}

// burstNodeBinding names the round's binding on its node. The node holds it
// to the binding-name rule, so the name leads with a letter whatever the
// owner hex starts with, and stays within the length cap: a head name too
// long to fit is cut and given a short hash of the whole name, so two long
// names sharing a prefix still land on different node bindings.
func burstNodeBinding(owner remote.ClientID, name string) string {
	prefix := "h" + burstOwner8(owner) + "-"
	if len(prefix)+len(name) <= store.MaxAgentNameLen {
		return prefix + name
	}
	sum := sha256.Sum256([]byte(name))
	tag := "-" + hex.EncodeToString(sum[:3])
	return prefix + name[:store.MaxAgentNameLen-len(prefix)-len(tag)] + tag
}
