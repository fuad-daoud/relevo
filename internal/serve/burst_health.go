package serve

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/store"
)

// burstHealth is the server's memory-only picture of node health: when the
// verify pass last dialled each checked node, how many acks in a row each
// node has refused, and which waiting rounds a node refused lately. Nothing is persisted: a restart re-dials once and
// can re-fire one stuck-ack hook, which is cheaper than storing marks no
// reader needs across a process.
type burstHealth struct {
	mu        sync.Mutex
	lastCheck map[string]time.Time
	ackFails  map[string]int
	refused   map[string]time.Time
	stopLast  map[string]time.Time
	stopDone  map[string]bool
}

func newBurstHealth() *burstHealth {
	return &burstHealth{
		lastCheck: map[string]time.Time{},
		ackFails:  map[string]int{},
		refused:   map[string]time.Time{},
		stopLast:  map[string]time.Time{},
		stopDone:  map[string]bool{},
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

// burstRefusalHold is how long a round a node refused stays out of
// placement. A refusal names the round, not the moment: the same node
// answers the same way on the next tick, so without the hold the round
// would bounce between claim and requeue every tick. It still runs locally
// in the meantime.
const burstRefusalHold = 5 * time.Minute

// burstRefusalKey names one waiting round: its owner-scoped id and the queue
// stamp it keeps across a requeue, so a later round of the same binding is
// a new key.
func burstRefusalKey(id string, queuedAt time.Time) string {
	return id + "@" + queuedAt.UTC().Format(time.RFC3339Nano)
}

// noteRefused records that a node refused the round at now, and forgets
// refusals older than the hold so the map stays as small as the queue.
func (h *burstHealth) noteRefused(key string, now time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, at := range h.refused {
		if now.Sub(at) >= burstRefusalHold {
			delete(h.refused, k)
		}
	}
	h.refused[key] = now
}

// refusalHeld says whether the round is still inside its hold at now.
func (h *burstHealth) refusalHeld(key string, now time.Time) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	at, ok := h.refused[key]
	return ok && now.Sub(at) < burstRefusalHold
}

// stopRetryEvery bounds the node-stop retry: a stop the node never answered
// waits this long before the next attempt, so a down node is not dialled on
// every tick while its round stays open.
const stopRetryEvery = 30 * time.Second

// stopMarkKey names one stop episode: the node, node binding and round the
// stop was sent for, so a later round's stop is a new episode.
func stopMarkKey(fwd store.Forward) string {
	return fmt.Sprintf("%s\x00%s\x00%d", fwd.Node, fwd.NodeBinding, fwd.Round)
}

// stopDue says whether another Stop may go out for key at now: never after
// one landed (an answer, or the node's nothing-to-stop), and at most one
// attempt per stopRetryEvery otherwise. A nil health dials every pass, the
// way recheckDue does.
func (h *burstHealth) stopDue(key string, now time.Time) bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopDone[key] {
		return false
	}
	last, ok := h.stopLast[key]
	return !ok || !now.Before(last.Add(stopRetryEvery))
}

// noteStopSent stamps an attempt that left without an answer, so the next
// one waits out stopRetryEvery.
func (h *burstHealth) noteStopSent(key string, now time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopLast[key] = now
}

// noteStopDone ends the episode: the stop landed, so no retry goes out.
func (h *burstHealth) noteStopDone(key string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopDone[key] = true
}

// forgetStopMarks drops every stop mark outside open, so a server that lives
// for months does not keep one per round ever stopped. A round's forward
// never comes back once its stop closed it, so a dropped mark is never asked
// for again.
func (h *burstHealth) forgetStopMarks(open map[string]bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for key := range h.stopLast {
		if !open[key] {
			delete(h.stopLast, key)
		}
	}
	for key := range h.stopDone {
		if !open[key] {
			delete(h.stopDone, key)
		}
	}
}
