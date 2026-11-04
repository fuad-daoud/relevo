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

// A stop the head records for a forwarded round reaches the node
// best-effort: the request never waits for it, a failure only logs, and the
// observe retry below carries on. fanoutStopAsync is the hook's detached
// send; retryForwardStop is the tick's backup for a stamp the send missed or
// a send that failed. Both record into the same stop marks beside
// burstHealth, so the first landed stop ends the episode.

// stopFanoutTimeout bounds one node-stop attempt. The client's own Stop
// carries the same bound; the outer one states it here so the goroutine can
// never outlive the drain that waits for it.
const stopFanoutTimeout = 30 * time.Second

// fanoutStopAsync sends Stop to the node running a stop-requested forward on
// a goroutine the drain waits for. The caller's context is already gone by
// the time the hook runs -- the handler answered -- so the attempt carries a
// detached timeout instead. It never holds s.mu or admitMu, and its outcome
// lands only while the stored forward still matches.
func (s *Server) fanoutStopAsync(ownerPath, name string, fwd store.Forward) {
	hook := s.stopFanoutHook
	s.fanout.Add(1)
	go func() {
		defer s.fanout.Done()
		err := s.stopForwardOnNode(ownerPath, name, fwd)
		if hook != nil {
			hook(err)
		}
	}()
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
	callCtx, cancel := context.WithTimeout(context.Background(), stopFanoutTimeout)
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
// -- takes no new stop traffic, so the stop waits for the observe retry
// instead.
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
