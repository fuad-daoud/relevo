package burst

// PlaceNode is one node as placement sees it: the head's two stop signals
// (verified and problem) beside the response's state and cap and the load
// the head last reported. Queued rounds hold their seats before they start,
// so a node has room only while cap - running - queued is positive.
type PlaceNode struct {
	ID       string
	Verified bool   // the node answered WhoAmI with the pinned fingerprint
	Problem  string // "" means the head saw no problem
	State    string // "ready" is the only state that takes a round
	Cap      int
	Running  int
	Queued   int
}

// Placement pairs a waiting round with the node it goes to. Round is carried
// through unchanged: placement never re-picks its candidate.
type Placement struct {
	Round  ReportWaiting
	NodeID string
}

// Place packs waiting rounds onto nodes in their given FIFO order. The first
// localFree rounds are the head's own seats and emit no placement; a localFree
// of zero or less places the whole batch through nodes. Each later round goes
// to the eligible node (verified, problem-free, ready, with a free seat) with
// the most running rounds, and each placement consumes one seat, so fullest
// is recomputed as the batch fills. A round with no eligible node is not
// emitted; it stays waiting. Ties go to the node listed first: callers must
// pass nodes in provider response order, because the provider sorts that list
// by price per seat and Place holds no prices to compare.
func Place(localFree int, waiting []ReportWaiting, nodes []PlaceNode) []Placement {
	eligible := make([]bool, len(nodes))
	run := make([]int, len(nodes))
	free := make([]int, len(nodes))
	for i, n := range nodes {
		eligible[i] = n.Verified && n.Problem == "" && n.State == "ready"
		run[i] = n.Running
		free[i] = n.Cap - n.Running - n.Queued
	}
	var out []Placement
	for i, w := range waiting {
		if i < localFree {
			continue
		}
		best := -1
		for j := range nodes {
			if !eligible[j] || free[j] <= 0 {
				continue
			}
			if best < 0 || run[j] > run[best] {
				best = j
			}
		}
		if best < 0 {
			continue
		}
		out = append(out, Placement{Round: w, NodeID: nodes[best].ID})
		run[best]++
		free[best]--
	}
	return out
}
