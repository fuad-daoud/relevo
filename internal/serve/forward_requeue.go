package serve

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The two paths that give a round its node lost back to the head: requeueLost
// during the tick, adoptSending once before the first tick after a restart.
// Both apply only while the stored forward still matches what was read, so a
// late result for a superseded claim never lands.

// requeueLost returns every forward on a dead node to the local queue at the
// FIFO place it held before the claim. A node is dead when the last good
// response stopped listing it, or when it has stayed unreachable past the
// configured bound. A requeued round keeps its candidate and excludes
// nothing: the loss says where the round ran, not who builds it, so a
// requeue is not a switch. The cleared forward also ends the episode: a
// late node result can no longer match, and the same loss cannot re-fire.
//
// Each node that gave rounds back gets one burst_node_lost naming them,
// dispatched after the walk the way the verify pass dispatches, so no hook
// script runs while a store is being iterated.
func (s *Server) requeueLost(ctx context.Context, now time.Time) {
	lostAfter := s.live().Policy.BurstNodeLostAfter()
	rows := map[string]burst.NodeRow{}
	if t := s.copyBurstTable(); t != nil {
		for _, r := range t.Nodes {
			rows[r.ID] = r
		}
	}
	names := map[string][]string{}
	var order []string
	_ = s.walkOwners(func(id remote.ClientID, ownerPath string, bindings []store.Binding) {
		for _, b := range bindings {
			fwd := burstStoredForward(b)
			if fwd == nil {
				continue
			}
			if !burstRowDead(rows, fwd.Node, now, lostAfter) {
				continue
			}
			if fwd.Phase == "closed" {
				s.dropLostAck(id, ownerPath, b.Name, *fwd)
				continue
			}
			if !s.requeueOne(id, ownerPath, b.Name, *fwd, now) {
				continue
			}
			if _, seen := names[fwd.Node]; !seen {
				order = append(order, fwd.Node)
			}
			names[fwd.Node] = append(names[fwd.Node], b.Name)
		}
	})
	for _, node := range order {
		if s.cfg.Hooks != nil {
			s.cfg.Hooks.Dispatch(ctx, hooks.Event{
				Type:      hooks.EventBurstNodeLost,
				BindingID: node,
				State:     strings.Join(names[node], ","),
				Timestamp: now,
			})
		}
	}
}

// burstRowDead reports whether the node holding a forward is lost: missing
// from the last good response, marked gone, or unreachable past the bound.
func burstRowDead(rows map[string]burst.NodeRow, node string, now time.Time, lostAfter time.Duration) bool {
	row, ok := rows[node]
	if !ok || row.Gone {
		return true
	}
	return row.Problem == burst.ReportProblemUnreachable && now.Sub(row.ProblemSince) >= lostAfter
}

// dropLostAck clears a closed forward whose node is lost. The head already
// holds that round's result, so there is nothing to run again; only the ack
// is owed, and a lost node can never take it. Left in place, the forward
// would count unacked forever and keep the binding from bursting again.
func (s *Server) dropLostAck(id remote.ClientID, ownerPath, name string, fwd store.Forward) {
	st := s.ownerStore(ownerPath)
	cur, err := st.Load(name)
	if err != nil {
		slog.Warn("burst: drop lost ack: load failed", "owner", string(id), "binding", name, "err", err)
		return
	}
	if !observeClosedAckMatches(cur, fwd) {
		return
	}
	cur.Burst.Forward = nil
	if len(cur.Burst.Shipped) == 0 && len(cur.Burst.NodeBindings) == 0 {
		cur.Burst = nil
	}
	if err := st.Save(cur); err != nil {
		slog.Warn("burst: drop lost ack: save failed", "owner", string(id), "binding", name, "err", err)
		return
	}
	slog.Info("burst: dropped the ack a lost node owed", "owner", string(id), "binding", name, "node", fwd.Node)
}

// requeueOne returns the named round to the queue at its FIFO place. The
// binding is re-read and the requeue lands only while the forward still
// matches, so a late result for a superseded forward never applies. It
// reports whether the round came back, so a caller naming what a lost node
// gave up lists only rounds it actually returned.
func (s *Server) requeueOne(id remote.ClientID, ownerPath, name string, fwd store.Forward, now time.Time) bool {
	st := s.ownerStore(ownerPath)
	cur, err := st.Load(name)
	if err != nil {
		slog.Warn("burst: requeue lost round: load failed", "owner", string(id), "binding", name, "err", err)
		return false
	}
	have := burstStoredForward(cur)
	if have == nil || have.Node != fwd.Node || cur.Round != fwd.Round {
		return false
	}
	since := have.QueuedSince
	if since.IsZero() {
		since = now
	}
	cur.QueuedAt = since
	cur.Burst.Forward = nil
	if len(cur.Burst.Shipped) == 0 && len(cur.Burst.NodeBindings) == 0 {
		cur.Burst = nil
	}
	if err := st.Save(cur); err != nil {
		slog.Warn("burst: requeue lost round: save failed", "owner", string(id), "binding", name, "err", err)
		return false
	}
	slog.Info("burst: requeued a forwarded round", "owner", string(id), "binding", name, "node", fwd.Node)
	return true
}

// adoptSending resolves every forward a crash left in sending: a forward
// the head claimed but no node confirmed. A node whose round is open at the
// forward's round holds it, so the forward turns running; anything else --
// a refused fetch, a missing or different round, a closed one -- returns to
// the local queue at its FIFO place. Fetching runs without s.mu, since a
// node round-trip must never hold the server lock; each answer applies
// under the owner store only while the forward still matches, so a result
// for a superseded claim never lands. Between passes the live fetch rides
// on GetBinding, and the closed rounds it requeues close again on the
// observe path.
func (s *Server) adoptSending(ctx context.Context) {
	if !s.burstOn() {
		return
	}
	now := s.cfg.Now().UTC()
	t := s.copyBurstTable()
	if t == nil {
		return
	}
	rows := map[string]burst.NodeRow{}
	for _, r := range t.Nodes {
		rows[r.ID] = r
	}
	type pending struct {
		ownerPath string
		id        remote.ClientID
		name      string
		fwd       store.Forward
	}
	var work []pending
	_ = s.walkOwners(func(id remote.ClientID, ownerPath string, bindings []store.Binding) {
		for _, b := range bindings {
			if f := burstStoredForward(b); f != nil && f.Phase == "sending" {
				work = append(work, pending{ownerPath: ownerPath, id: id, name: b.Name, fwd: *f})
			}
		}
	})
	if len(work) == 0 {
		return
	}
	cl, err := s.burstNodeClient(t.Nodes)
	if err != nil {
		slog.Warn("burst: adopt skipped, client key unusable", "err", err)
		return
	}
	for _, p := range work {
		if _, ok := rows[p.fwd.Node]; !ok {
			s.requeueOne(p.id, p.ownerPath, p.name, p.fwd, now)
			continue
		}
		view, ferr := cl.GetBinding(ctx, p.fwd.Node, p.fwd.NodeBinding)
		if burstAdoptRunning(p.fwd, view, ferr) {
			s.adoptRunning(p.ownerPath, p.name, p.fwd)
		} else {
			if ferr != nil {
				slog.Info("burst: adopt requeues an unconfirmed forward", "binding", p.name, "node", p.fwd.Node, "err", ferr)
			}
			s.requeueOne(p.id, p.ownerPath, p.name, p.fwd, now)
		}
	}
}

// burstAdoptRunning says whether a sending forward survived the restart: the
// node answered, its round is open, and it is the round the head claimed.
// Every other answer -- a fetch failure, a missing node binding, a different
// or closed round -- requeues, so the round waits locally instead of holding
// a seat on a node that never took it.
func burstAdoptRunning(fwd store.Forward, view remote.BindingView, fetchErr error) bool {
	if fetchErr != nil {
		return false
	}
	if view.Round != fwd.Round {
		return false
	}
	return view.RoundState == remote.RoundRunning || view.RoundState == remote.RoundQueued
}

// adoptRunning turns a confirmed sending forward into a running one. The
// binding is re-read and the mark lands only while the forward still
// matches, so a result for a superseded claim never applies.
func (s *Server) adoptRunning(ownerPath, name string, fwd store.Forward) {
	st := s.ownerStore(ownerPath)
	b, err := st.Load(name)
	if err != nil {
		slog.Warn("burst: adopt confirm failed: load failed", "binding", name, "err", err)
		return
	}
	have := burstStoredForward(b)
	if have == nil || have.Phase != "sending" || have.Node != fwd.Node || b.Round != fwd.Round {
		return
	}
	have.Phase = "running"
	if err := st.Save(b); err != nil {
		slog.Warn("burst: adopt confirm failed: save failed", "binding", name, "err", err)
		return
	}
	// The adopted round ran on the node, so it is not a local start: the
	// note says as much, at the claim time the node's answer proves -- the
	// adopt itself happens after the fact, and now would place the start
	// long after the round really began. "adopted" names a start whose kind
	// nobody measured: it was never a warm or cold send this head confirmed.
	rt := s.runtimeAt(ownerPath)
	if err := rt.Store.AppendLog(name, store.LogEntry{
		TS: fwd.ClaimedAt, Round: fwd.Round, Direction: store.DirToMasterMind,
		Kind: store.KindQueue, Confirmed: true,
		Note: store.ForwardedNotePrefix + fwd.Node + " (adopted)",
	}); err != nil {
		slog.Warn("burst: adopt confirm: send note not appended", "binding", name,
			"round", fwd.Round, "node", fwd.Node, "err", err)
	}
	slog.Info("burst: adopt confirmed a forward its node holds", "binding", name, "node", fwd.Node)
}
