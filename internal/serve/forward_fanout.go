package serve

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// A stop or an unbind the head decides for a forwarded round reaches the
// nodes best-effort: the request never waits for it, a failure only logs,
// and for stops the observe retry carries on. The *Async helpers queue a
// detached attempt on the goroutine the drain waits for; the attempt itself
// dials, records and logs. Stop attempts share the stop marks beside
// burstHealth, so the first landed stop ends the episode; unbind and
// cleared-forward stops are one-shot best efforts with no marks behind
// them.

// fanoutTimeout bounds one node-stop or node-unbind attempt. The client's
// own verbs carry the same bound; the outer one states it here so the
// goroutine can never outlive the drain that waits for it.
const fanoutTimeout = 30 * time.Second

// fanoutDetached queues one best-effort node attempt on the goroutine the
// server's drain waits for and reports its outcome to the join hook when
// the attempt lands. The caller's context is already gone by the time a
// fan-out runs -- the handler answered -- so each attempt carries a
// detached timeout instead, and no fan-out ever holds s.mu or admitMu.
func (s *Server) fanoutDetached(hook func(error), attempt func() error) {
	s.fanout.Add(1)
	go func() {
		defer s.fanout.Done()
		err := attempt()
		if hook != nil {
			hook(err)
		}
	}()
}

// fanoutStopAsync sends Stop to the node running a stop-requested forward.
func (s *Server) fanoutStopAsync(ownerPath, name string, fwd store.Forward) {
	hook := s.stopFanoutHook
	s.fanoutDetached(hook, func() error { return s.stopForwardOnNode(ownerPath, name, fwd) })
}

// stopForwardOnNode sends one node stop for a stop-requested forward. A
// landed stop (an answer, or the node's nothing-to-stop) ends the episode; a
// failure stamps the attempt and only logs, leaving the retry to the tick.
func (s *Server) stopForwardOnNode(ownerPath, name string, fwd store.Forward) error {
	now := s.cfg.Now().UTC()
	key := stopMarkKey(fwd)
	row := stopRowFor(s.copyBurstTable(), fwd.Node)
	if row == nil {
		return nil
	}
	cur, err := s.ownerStore(ownerPath).Load(name)
	if err != nil {
		return err
	}
	if !forwardStopMatches(cur, fwd) {
		return nil
	}
	cl, err := s.burstNodeClient([]burst.NodeRow{*row})
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(context.Background(), fanoutTimeout)
	defer cancel()
	_, serr := cl.Stop(callCtx, row.ID, fwd.NodeBinding)
	if serr == nil || isStopNothingToStop(serr) {
		s.burstHealth.noteStopDone(key)
		return nil
	}
	s.burstHealth.noteStopSent(key, now)
	slog.Warn("burst: forward stop fan-out failed, the observe retry carries on",
		"binding", name, "node", fwd.Node, "err", serr)
	return serr
}

// retryForwardStop resends Stop for a forward the head already asked to stop
// whose node view is still open: the stamp a mid-flight send missed, or a
// best-effort send that failed. At most one attempt per stopRetryEvery; a
// nothing-to-stop answer ends the episode, since the node has nothing left
// to stop.
func (s *Server) retryForwardStop(ctx context.Context, cl *client.Client, row burst.NodeRow, w observeWork, now time.Time) {
	if !w.fwd.StopRequested {
		return
	}
	key := stopMarkKey(w.fwd)
	if !s.burstHealth.stopDue(key, now) {
		return
	}
	_, err := cl.Stop(ctx, row.ID, w.fwd.NodeBinding)
	if err == nil || isStopNothingToStop(err) {
		s.burstHealth.noteStopDone(key)
		return
	}
	s.burstHealth.noteStopSent(key, now)
	slog.Warn("burst: forward stop retry failed, the next tick tries again",
		"binding", w.name, "node", w.fwd.Node, "err", err)
}

// stopRowFor resolves the node a fan-out may dial: a verified row that is
// still taking work. Anything else -- missing, unverified, draining or gone
// -- takes no new fan-out traffic: a stop waits for the observe retry
// instead, and an unbind leaves that node's binding to the node's own
// release.
func stopRowFor(t *burst.NodeTable, node string) *burst.NodeRow {
	if t == nil {
		return nil
	}
	row := burstTableRow(t, node)
	if row == nil || !row.Verified || row.Gone || row.State != "ready" {
		return nil
	}
	return row
}

// forwardStopMatches reports whether cur still holds the forward the stop
// was recorded for: same node, node binding and round, still stop-requested,
// so an answer for a superseded claim never applies.
func forwardStopMatches(cur store.Binding, fwd store.Forward) bool {
	have := burstStoredForward(cur)
	if have == nil || !have.StopRequested {
		return false
	}
	return have.Node == fwd.Node && have.NodeBinding == fwd.NodeBinding &&
		have.Round == fwd.Round && cur.Round == fwd.Round
}

// isStopNothingToStop reports whether err is the node's answer that the
// round is already gone there: the stop landed by another route, so the
// retry ends instead of redialling a round that will never open.
func isStopNothingToStop(err error) bool {
	var herr *client.HTTPError
	return errors.As(err, &herr) && herr.Status == http.StatusConflict && herr.Body.Code == remote.CodeNothingToStop
}

// A released head binding leaves node bindings behind unless the release
// dials every node it shipped to. The head record is the only list of
// those nodes, and it changes with the release, so the fan-out runs on the
// record captured before the settle: Done and UnbindWith call the
// UnbindNodes hook with it once the local step landed, never under the
// store lock, and failures only log -- the node's own release cleans up
// whatever this missed.

// unbindFanoutTargets names every node the record may still hold a binding
// on: each node it shipped to, plus the node of a forward still live --
// a sending claim has no ship entry yet, and a forced release of it must
// reach that node too. The node binding repeats one name per node, so the
// ship map's keys and the forward's node drive this, deduplicated; a closed
// forward's node is already in the ship map.
func unbindFanoutTargets(b store.Binding) []string {
	if b.Burst == nil {
		return nil
	}
	live := b.Burst.Forward != nil &&
		(b.Burst.Forward.Phase == "sending" || b.Burst.Forward.Phase == "running")
	ids := make([]string, 0, len(b.Burst.Shipped)+1)
	for node := range b.Burst.Shipped {
		ids = append(ids, node)
	}
	if live {
		if _, shipped := b.Burst.Shipped[b.Burst.Forward.Node]; !shipped {
			ids = append(ids, b.Burst.Forward.Node)
		}
	}
	return ids
}

// fanoutUnbindAsync sends one node unbind per target node, each on its own
// goroutine the drain waits for. The node binding carries the head's own
// name under the node-side namespace, the one name every target shares.
func (s *Server) fanoutUnbindAsync(owner remote.ClientID, headName string, nodeIDs []string) {
	hook := s.unbindFanoutHook
	nodeBinding := burstNodeBinding(owner, headName)
	for _, id := range nodeIDs {
		s.fanoutDetached(hook, func() error { return s.unbindNode(headName, nodeBinding, id) })
	}
}

// unbindNode releases one node's copy of a head binding, forced: the head
// has decided, and the node may hold an open round the release must not
// wait on. A 404 is the node's answer that the binding is already gone --
// unbound by this release through another route -- so it lands, not fails.
func (s *Server) unbindNode(headName, nodeBinding, nodeID string) error {
	row := stopRowFor(s.copyBurstTable(), nodeID)
	if row == nil {
		return nil
	}
	cl, err := s.burstNodeClient([]burst.NodeRow{*row})
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(context.Background(), fanoutTimeout)
	defer cancel()
	uerr := cl.Unbind(callCtx, row.ID, nodeBinding, true)
	if uerr == nil || isUnbindGone(uerr) {
		return nil
	}
	slog.Warn("burst: node unbind fan-out failed", "binding", headName, "node", nodeID, "err", uerr)
	return uerr
}

// isUnbindGone reports whether err is the node's 404 for a binding that is
// no longer there: there is nothing left to unbind, so the fan-out landed.
func isUnbindGone(err error) bool {
	return burstHTTPStatus(err, http.StatusNotFound)
}

// A server-wide gate the head records reaches every verified live node
// best-effort: the request never waits for it and a failure only logs.
// The attempt carries the gate as the head received it, so the node resolves
// it against its own candidates the way the head did -- an account key keeps
// its account scope there. A node verified after the gate hears nothing;
// its own builder gates itself on its next limit instead.

// gateRowFor resolves a node a gate fan-out may dial: the stop filter plus
// problem-free, the placement eligibility a gate needs -- a node the head
// already marked unreachable takes no new gate traffic, the same way it
// takes no new round.
func gateRowFor(t *burst.NodeTable, node string) *burst.NodeRow {
	row := stopRowFor(t, node)
	if row == nil || row.Problem != "" {
		return nil
	}
	return row
}

// gateFanoutTargets snapshots the rows a gate fan-out may dial: every
// verified live node the table holds, in table order.
func (s *Server) gateFanoutTargets() []burst.NodeRow {
	t := s.copyBurstTable()
	if t == nil {
		return nil
	}
	var out []burst.NodeRow
	for _, r := range t.Nodes {
		if gateRowFor(t, r.ID) == nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// fanoutGateUnavailableAsync sends one unavailable per live node, each on
// its own goroutine the drain waits for. The empty name is the server-wide
// route: the node records the gate on its own ledger.
func (s *Server) fanoutGateUnavailableAsync(token, reason string) {
	s.fanoutGate(targetGateUnavailable{token: token, reason: reason})
}

// fanoutGateAvailableAsync sends one available per live node, each on its
// own goroutine the drain waits for.
func (s *Server) fanoutGateAvailableAsync(subject string) {
	s.fanoutGate(targetGateAvailable{subject: subject})
}

// targetGate is one gate a fan-out carries to a node: the verb and what it
// sends. The two verbs share the detached send and the log-only failure;
// only the call differs.
type targetGate interface {
	send(ctx context.Context, cl *client.Client, node string) error
	kind() string
}

// targetGateUnavailable carries an unavailable to one node.
type targetGateUnavailable struct {
	token  string
	reason string
}

func (g targetGateUnavailable) kind() string { return "unavailable" }

func (g targetGateUnavailable) send(ctx context.Context, cl *client.Client, node string) error {
	return cl.Unavailable(ctx, node, "", g.token, g.reason)
}

// targetGateAvailable carries an available to one node.
type targetGateAvailable struct {
	subject string
}

func (g targetGateAvailable) kind() string { return "available" }

func (g targetGateAvailable) send(ctx context.Context, cl *client.Client, node string) error {
	_, err := cl.Available(ctx, node, g.subject)
	return err
}

// fanoutGate dials every verified live node with gate, one detached attempt
// per node. The caller already answered, so each attempt carries a detached
// timeout; a failure only logs with the node id, never failing the gate the
// head already recorded.
func (s *Server) fanoutGate(gate targetGate) {
	rows := s.gateFanoutTargets()
	if len(rows) == 0 {
		return
	}
	cl, err := s.burstNodeClient(rows)
	if err != nil {
		slog.Warn("burst: gate fan-out skipped, client key unusable", "gate", gate.kind(), "err", err)
		return
	}
	hook := s.gateFanoutHook
	for _, row := range rows {
		s.fanoutDetached(hook, func() error { return s.gateOnNode(cl, row.ID, gate) })
	}
}

// gateOnNode sends one gate to one node. A failure only logs: the gate is
// recorded on the head either way, and the node's own builder gates itself
// on its next limit.
func (s *Server) gateOnNode(cl *client.Client, node string, gate targetGate) error {
	callCtx, cancel := context.WithTimeout(context.Background(), fanoutTimeout)
	defer cancel()
	if err := gate.send(callCtx, cl, node); err != nil {
		slog.Warn("burst: gate fan-out failed", "gate", gate.kind(), "node", node, "err", err)
		return err
	}
	return nil
}

// fanoutClearedStop sends the node stop for a forward the head has already
// cleared and saved: the round parked on a waiting node came home without
// its builder finishing, so the node's round must end and nothing on the
// head still names it. The stop goes out only once that save landed -- a
// failed save would have stopped a node round the head still thinks is
// running. There is no stored forward to match and no retry mark to keep:
// the observe pass no longer walks this forward, and a node that needs a
// human needs one on its own side. The attempt lands or only logs, on the
// same drained goroutine the stop hook reports through.
func (s *Server) fanoutClearedStop(name string, fwd store.Forward) {
	hook := s.stopFanoutHook
	s.fanoutDetached(hook, func() error { return s.clearedStopOnNode(name, fwd) })
}

func (s *Server) clearedStopOnNode(name string, fwd store.Forward) error {
	row := stopRowFor(s.copyBurstTable(), fwd.Node)
	if row == nil {
		return nil
	}
	cl, err := s.burstNodeClient([]burst.NodeRow{*row})
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(context.Background(), fanoutTimeout)
	defer cancel()
	_, serr := cl.Stop(callCtx, row.ID, fwd.NodeBinding)
	if serr == nil || isStopNothingToStop(serr) {
		return nil
	}
	slog.Warn("burst: waiting-round stop fan-out failed", "binding", name, "node", fwd.Node, "err", serr)
	return serr
}
