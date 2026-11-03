package serve

import (
	"context"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/hooks"
)

// burstHealth is the server's memory-only picture of node health: when the
// verify pass last dialled each checked node, and how many acks in a row
// each node has refused. Nothing is persisted: a restart re-dials once and
// can re-fire one stuck-ack hook, which is cheaper than storing marks no
// reader needs across a process.
type burstHealth struct {
	mu        sync.Mutex
	lastCheck map[string]time.Time
	ackFails  map[string]int
}

func newBurstHealth() *burstHealth {
	return &burstHealth{
		lastCheck: map[string]time.Time{},
		ackFails:  map[string]int{},
	}
}

// recheckDue says whether a node with a standing problem may be dialled
// again at now, after having waited the backoff since its last check. A
// node never dialled is always due; nil health dials every node every pass.
func (h *burstHealth) recheckDue(id string, now time.Time, backoff time.Duration) bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	last, ok := h.lastCheck[id]
	return !ok || !now.Before(last.Add(backoff))
}

// noteCheck stamps that this verify pass dialled the node.
func (h *burstHealth) noteCheck(id string, at time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastCheck[id] = at
}

// burstAckStuckAfter is how many consecutive refused acks a node earns
// before the head flags the episode: the first failures read as a node
// still settling, three in a row says the ack will not land on its own.
const burstAckStuckAfter = 3

// noteAckFailure counts one refused ack and returns the streak.
func (h *burstHealth) noteAckFailure(id string) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ackFails[id]++
	return h.ackFails[id]
}

// clearAckFails ends a node's episode: an ack that landed proves the path
// works again.
func (h *burstHealth) clearAckFails(id string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.ackFails, id)
}

// forgetAckEpisodes drops the count of every node not holding a closed
// forward any more: a forward that left by requeue or parking ends its
// episode even though no ack failure or success just ran.
func (h *burstHealth) forgetAckEpisodes(holdingClosed map[string]bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id := range h.ackFails {
		if !holdingClosed[id] {
			delete(h.ackFails, id)
		}
	}
}

// noteAckStuck counts one refused ack toward its node's episode and fires
// burst_ack_stuck on the third in a row: once per episode, the way the
// security hook fires once per mismatch, while the failure itself is logged
// where it happened.
func (s *Server) noteAckStuck(ctx context.Context, w observeWork) {
	if s.burstHealth.noteAckFailure(w.fwd.Node) != burstAckStuckAfter {
		return
	}
	if s.cfg.Hooks != nil {
		s.cfg.Hooks.Dispatch(ctx, hooks.Event{
			Type:      hooks.EventBurstAckStuck,
			BindingID: w.fwd.Node,
			State:     w.name,
			Round:     w.fwd.Round,
			Timestamp: s.cfg.Now().UTC(),
		})
	}
}
