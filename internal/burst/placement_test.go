package burst

import (
	"reflect"
	"testing"
	"time"
)

// waitingRound builds one round whose candidate and harness are named for its
// id; a want list that rebuilds the round with it pins that Place carries the
// whole round through.
func waitingRound(id string) ReportWaiting {
	return ReportWaiting{
		ID:        id,
		Since:     time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC),
		Candidate: "cand-" + id,
		Harness:   "harn-" + id,
	}
}

func readyNode(id string, cap, running, queued int) PlaceNode {
	return PlaceNode{ID: id, Verified: true, State: "ready", Cap: cap, Running: running, Queued: queued}
}

func TestPlacePacksWaitingRoundsOntoEligibleNodes(t *testing.T) {
	tests := []struct {
		name      string
		localFree int
		waiting   []ReportWaiting
		nodes     []PlaceNode
		want      []Placement
	}{
		{
			name:      "the first localFree rounds stay local",
			localFree: 2,
			waiting:   []ReportWaiting{waitingRound("r1"), waitingRound("r2"), waitingRound("r3")},
			nodes:     []PlaceNode{readyNode("b1", 5, 0, 0)},
			want:      []Placement{{Round: waitingRound("r3"), NodeID: "b1"}},
		},
		{
			name:      "a batch shorter than the local seats places nothing",
			localFree: 4,
			waiting:   []ReportWaiting{waitingRound("r1"), waitingRound("r2")},
			nodes:     []PlaceNode{readyNode("b1", 5, 0, 0)},
			want:      nil,
		},
		{
			name:      "a negative localFree skips no round",
			localFree: -1,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes:     []PlaceNode{readyNode("b1", 1, 0, 0)},
			want:      []Placement{{Round: waitingRound("r1"), NodeID: "b1"}},
		},
		{
			name:      "the first waiting round takes the last seat",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1"), waitingRound("r2")},
			nodes:     []PlaceNode{readyNode("b1", 1, 0, 0)},
			want:      []Placement{{Round: waitingRound("r1"), NodeID: "b1"}},
		},
		{
			name:      "a draining node never takes a round",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes: []PlaceNode{
				{ID: "b1", Verified: true, State: "draining", Cap: 3, Running: 2},
				readyNode("b2", 3, 0, 0),
			},
			want: []Placement{{Round: waitingRound("r1"), NodeID: "b2"}},
		},
		{
			name:      "an unverified node never takes a round",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes: []PlaceNode{
				{ID: "b1", State: "ready", Cap: 3, Running: 2},
				readyNode("b2", 3, 0, 0),
			},
			want: []Placement{{Round: waitingRound("r1"), NodeID: "b2"}},
		},
		{
			name:      "a node with a problem never takes a round",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes: []PlaceNode{
				{ID: "b1", Verified: true, Problem: ReportProblemFingerprint, State: "ready", Cap: 3, Running: 2},
				readyNode("b2", 3, 0, 0),
			},
			want: []Placement{{Round: waitingRound("r1"), NodeID: "b2"}},
		},
		{
			name:      "the fullest ready node takes the round",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes:     []PlaceNode{readyNode("b1", 3, 0, 0), readyNode("b2", 3, 2, 0)},
			want:      []Placement{{Round: waitingRound("r1"), NodeID: "b2"}},
		},
		{
			name:      "a tie goes to the node listed first",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes:     []PlaceNode{readyNode("b2", 3, 1, 0), readyNode("b1", 3, 1, 0)},
			want:      []Placement{{Round: waitingRound("r1"), NodeID: "b2"}},
		},
		{
			name:      "a full node is skipped",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes:     []PlaceNode{readyNode("b1", 3, 3, 0), readyNode("b2", 3, 0, 0)},
			want:      []Placement{{Round: waitingRound("r1"), NodeID: "b2"}},
		},
		{
			name:      "queued rounds hold seats",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes:     []PlaceNode{readyNode("b1", 3, 1, 2), readyNode("b2", 3, 0, 0)},
			want:      []Placement{{Round: waitingRound("r1"), NodeID: "b2"}},
		},
		{
			name:      "placements never exceed the free seats",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1"), waitingRound("r2"), waitingRound("r3")},
			nodes:     []PlaceNode{readyNode("b1", 2, 0, 0)},
			want: []Placement{
				{Round: waitingRound("r1"), NodeID: "b1"},
				{Round: waitingRound("r2"), NodeID: "b1"},
			},
		},
		{
			name:      "fullest is recomputed as the batch fills",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1"), waitingRound("r2")},
			nodes:     []PlaceNode{readyNode("b1", 3, 2, 0), readyNode("b2", 3, 0, 0)},
			want: []Placement{
				{Round: waitingRound("r1"), NodeID: "b1"},
				{Round: waitingRound("r2"), NodeID: "b2"},
			},
		},
		{
			name:      "the round keeps its candidate and harness",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes:     []PlaceNode{readyNode("b1", 1, 0, 0)},
			want:      []Placement{{Round: waitingRound("r1"), NodeID: "b1"}},
		},
		{
			name:      "no node at all places nothing",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1")},
			want:      nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Place(tt.localFree, tt.waiting, tt.nodes)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Place(%d) = %+v, want %+v", tt.localFree, got, tt.want)
			}
		})
	}
}

// TestPlaceReplaysTheDaysimOracle walks the placement scenarios of the
// provider's placement oracle, the stand-in place() in the burst daysim
// harness (contabo/burst/internal/daysim/harness_test.go): a free head seat,
// the fullest ready node, a draining node skipped, and a full node skipped.
// The oracle breaks a running-count tie by price per seat; relevo reads no
// prices, so the provider's response order carries that choice: the tie case
// below pins the substitution, not a different pick.
func TestPlaceReplaysTheDaysimOracle(t *testing.T) {
	tests := []struct {
		name      string
		localFree int
		waiting   []ReportWaiting
		nodes     []PlaceNode
		want      []Placement
	}{
		{
			name:      "a free head seat keeps the round local",
			localFree: 1,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes:     []PlaceNode{readyNode("b1", 3, 1, 0)},
			want:      nil,
		},
		{
			name:      "one head seat then nodes: each round takes one seat",
			localFree: 1,
			waiting:   []ReportWaiting{waitingRound("r1"), waitingRound("r2")},
			nodes:     []PlaceNode{readyNode("b1", 3, 2, 0)},
			want:      []Placement{{Round: waitingRound("r2"), NodeID: "b1"}},
		},
		{
			name:      "no head seat then the fullest ready node",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes:     []PlaceNode{readyNode("b1", 3, 0, 0), readyNode("b2", 3, 2, 0)},
			want:      []Placement{{Round: waitingRound("r1"), NodeID: "b2"}},
		},
		{
			name:      "a draining node is skipped",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes: []PlaceNode{
				{ID: "b2", Verified: true, State: "draining", Cap: 3, Running: 2},
				readyNode("b1", 3, 0, 0),
			},
			want: []Placement{{Round: waitingRound("r1"), NodeID: "b1"}},
		},
		{
			name:      "a node with no free seat is skipped",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes:     []PlaceNode{readyNode("b2", 3, 3, 0), readyNode("b1", 3, 0, 0)},
			want:      []Placement{{Round: waitingRound("r1"), NodeID: "b1"}},
		},
		{
			name:      "equal running keeps the response order",
			localFree: 0,
			waiting:   []ReportWaiting{waitingRound("r1")},
			nodes:     []PlaceNode{readyNode("b1", 3, 1, 0), readyNode("b2", 3, 1, 0)},
			want:      []Placement{{Round: waitingRound("r1"), NodeID: "b1"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Place(tt.localFree, tt.waiting, tt.nodes)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Place(%d) = %+v, want %+v", tt.localFree, got, tt.want)
			}
		})
	}
}
