package serve

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The observe pass ends a forward two ways: a closed round's ack clears it
// once the node confirms receipt, and a round waiting on a human parks the
// head binding on the node's reason. Both apply only while the stored
// forward still matches, the way the adopt and send paths apply theirs.

// ackObserveClosed acks the closed round without the lock. An ack failure
// keeps Phase closed so the next cycle retries and the node keeps counting
// unacked; it is also the node's ack episode, counted and flagged once when
// a reachable node refuses three in a row. A failure never marks the node
// unreachable and never requeues: the round is home, only its receipt is
// missing. A 404 is not a refusal: the node has no binding left to ack --
// this head released it and the unbind fan-out reached the node -- so the
// forward clears with no failure counted and no ack-stuck episode. True
// means the ack landed and the forward cleared.
func (s *Server) ackObserveClosed(ctx context.Context, cl *client.Client, w observeWork) bool {
	if _, err := cl.Ack(ctx, w.fwd.Node, w.fwd.NodeBinding, w.fwd.Round); err != nil {
		if !burstHTTPStatus(err, http.StatusNotFound) {
			slog.Warn("burst: observe ack failed, keeping closed", "binding", w.name, "node", w.fwd.Node, "err", err)
			s.noteAckStuck(ctx, w)
			return false
		}
		slog.Info("burst: observe ack found no node binding, clearing the forward", "binding", w.name, "node", w.fwd.Node)
	} else {
		s.burstHealth.clearAckFails(w.fwd.Node)
	}
	st := s.ownerStore(w.ownerPath)
	cur, err := st.Load(w.name)
	if err != nil {
		return false
	}
	if !observeClosedAckMatches(cur, w.fwd) {
		return true
	}
	cur.Burst.Forward = nil
	if len(cur.Burst.Shipped) == 0 && len(cur.Burst.NodeBindings) == 0 {
		cur.Burst = nil
	}
	if err := st.Save(cur); err != nil {
		slog.Warn("burst: observe ack clear failed", "binding", w.name, "err", err)
		return false
	}
	return true
}

// observeNeedsYou parks a head binding the node waits on a human for. The
// head takes the node's halt reason and report, clears the forward, and
// leaves the round open: the local tick owns what happens next.
func (s *Server) observeNeedsYou(ctx context.Context, cl *client.Client, row burst.NodeRow, w observeWork, view remote.BindingView, now time.Time) {
	if view.Round != w.fwd.Round {
		slog.Warn("burst: observe answered another waiting round", "binding", w.name, "node", w.fwd.Node, "want", w.fwd.Round, "got", view.Round)
		return
	}
	var report []byte
	if rc, err := cl.RoundFile(ctx, w.fwd.Node, w.fwd.NodeBinding, view.Round, "report"); err == nil {
		body, rerr := io.ReadAll(rc)
		_ = rc.Close()
		if rerr == nil {
			report = body
		}
	} else if !isObserveNotFound(err) {
		if burstForwardUnreachable(err) {
			s.noteNodeUnreachable(now, row)
		}
		return
	}
	st := s.ownerStore(w.ownerPath)
	cur, err := st.Load(w.name)
	if err != nil {
		return
	}
	if !observeMatches(cur, w.fwd) {
		return
	}
	rt := s.runtimeAt(w.ownerPath)
	if report != nil {
		path := rt.Store.ReportPath(cur.Name, w.fwd.Round)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			_ = os.WriteFile(path, report, 0o644)
		}
	}
	cur.State = store.StateNeedsYou
	cur.Halt = view.Halt
	if cur.Halt == "" {
		cur.Halt = "the node waits on its human"
	}
	cur.Burst.Forward = nil
	if len(cur.Burst.Shipped) == 0 && len(cur.Burst.NodeBindings) == 0 {
		cur.Burst = nil
	}
	if err := st.Save(cur); err != nil {
		slog.Warn("burst: observe needs_you save failed", "binding", w.name, "err", err)
		return
	}
	s.fanoutClearedStop(w.name, w.fwd)
}
