package burst

import (
	"encoding/json"
	"time"
)

// Census is what the head knows about itself when it reports: the facts that
// identify it, its own load, and the waiting rounds in FIFO order. A
// forwarded round is in none of the local counts — it holds no local process
// and has left the local queue — and reaches the report through the
// forwards, so a forwarded round is counted exactly once, on its node.
type Census struct {
	Head    ReportHead
	Local   ReportLocal
	Waiting []ReportWaiting
}

// Forward is one round the head sent to a node and has not brought home:
// sent and not yet confirmed, running or queued on the node, or closed there
// with the result not yet acked. State says which; the head's loop decides
// that from the forward's phase and the node's own view, and clears the
// record when the round is closed and acked or requeued.
type Forward struct {
	Round  ReportRound
	NodeID string
	State  string
}

// Forward states a counted round can be in: the three per-node counts of the
// report, and no more.
const (
	ForwardRunning = "running"
	ForwardQueued  = "queued"
	ForwardUnacked = "unacked"
)

// BuildReport assembles the report the head sends from its census and its
// node table. Every live node appears, even one counting nothing: the list
// is the head's statement of the nodes it still holds, and dropping a live
// one would tell the provider to idle-delete a node the head means to use.
// Per-node running, queued and unacked are counted from the head's forward
// records; only running rounds are listed in Rounds, the rounds the node is
// burning a seat on. The finished document goes through ValidateRequest, so
// what comes back is what the provider's decoder accepts, and a census or
// forward that could not form such a document is refused here rather than
// over the wire.
func BuildReport(c Census, t NodeTable, at time.Time, reason string, fs []Forward) (ReportRequest, []string, error) {
	index := make(map[string]int, len(t.Nodes))
	gone := make(map[string]bool)
	nodes := make([]ReportNode, 0, len(t.Nodes))
	for _, row := range t.Nodes {
		if row.Gone {
			gone[row.ID] = true
			continue
		}
		index[row.ID] = len(nodes)
		nodes = append(nodes, ReportNode{
			ID: row.ID, ForwardedAt: row.ForwardedAt, Problem: row.Problem,
		})
	}
	for _, f := range fs {
		switch f.State {
		case ForwardRunning, ForwardQueued, ForwardUnacked:
		default:
			return ReportRequest{}, nil, badf("forward %s: unknown state %q", f.Round.ID, f.State)
		}
		i, ok := index[f.NodeID]
		if !ok {
			// A forward on a gone node waits for the requeue that will take
			// it back; counting it would put a node the last good response
			// declared gone back into the report.
			if gone[f.NodeID] {
				continue
			}
			return ReportRequest{}, nil, badf("forward %s: unknown node %q", f.Round.ID, f.NodeID)
		}
		switch f.State {
		case ForwardRunning:
			nodes[i].Running++
			nodes[i].Rounds = append(nodes[i].Rounds, f.Round)
		case ForwardQueued:
			nodes[i].Queued++
		case ForwardUnacked:
			nodes[i].Unacked++
		}
	}
	waiting := c.Waiting
	if waiting == nil {
		// A JSON null waiting is a missing required field to the provider;
		// an empty list says the head has nothing waiting.
		waiting = []ReportWaiting{}
	}
	// json.Marshal fails only on values no encoding exists for, and a
	// request is strings, counts and times; the blank keeps that failure
	// from hiding: the empty document that follows is then refused below.
	data, _ := json.Marshal(ReportRequest{
		Version: ReportVersion, At: at, Reason: reason,
		Head: c.Head, Local: c.Local, Waiting: waiting, Nodes: nodes,
	})
	req, notes, err := ValidateRequest(data)
	if err != nil {
		return ReportRequest{}, nil, err
	}
	return req, notes, nil
}
