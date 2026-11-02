package burst

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const mergeURL = "https://1.2.3.4:7777"

func outNode(mutate func(*ReportNodeOut)) ReportNodeOut {
	n := ReportNodeOut{
		ID: "b1", Name: "b1 cx43@hel1", URL: mergeURL,
		Fingerprint: validFingerprint, Cap: 4, State: "ready",
	}
	if mutate != nil {
		mutate(&n)
	}
	return n
}

func mergeResp(every string, mutate func(*ReportResponse), nodes ...ReportNodeOut) ReportResponse {
	r := ReportResponse{Version: ReportVersion, Nodes: nodes, ReportEvery: every}
	if mutate != nil {
		mutate(&r)
	}
	return r
}

// freshRow is what Merge adds for an offered node of the outNode defaults:
// the offer, and nothing the head could know without verifying or watching.
func freshRow(id string, at time.Time) NodeRow {
	return NodeRow{
		ID: id, Name: id + " cx43@hel1", URL: mergeURL,
		Fingerprint: validFingerprint, Cap: 4, State: "ready",
		LastSeen: at,
	}
}

func TestMergeAddsNewNodesUnverifiedInResponseOrder(t *testing.T) {
	at := reportTime(8, 0)
	table := &NodeTable{}
	events := table.Merge(at, mergeResp("45s", nil,
		outNode(func(n *ReportNodeOut) { n.ID = "b2"; n.Name = "b2 cx43@hel1" }),
		outNode(nil),
	))
	want := []NodeRow{freshRow("b2", at), freshRow("b1", at)}
	if !reflect.DeepEqual(table.Nodes, want) {
		t.Errorf("rows = %+v, want %+v", table.Nodes, want)
	}
	wantEvents := []NodeEvent{{Kind: EventAdded, NodeID: "b2"}, {Kind: EventAdded, NodeID: "b1"}}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Errorf("events = %+v, want %+v", events, wantEvents)
	}
	if table.Interval != 45*time.Second {
		t.Errorf("interval = %v, want the response's 45s", table.Interval)
	}
	if !table.LastGood.Equal(at) || !table.LastReport.Equal(at) || table.LastErr != "" {
		t.Errorf("marks = %v/%v/%q, want both at %v and no error", table.LastGood, table.LastReport, table.LastErr, at)
	}
}

// TestMergeKeepsResponseOrderAndGoneRowsLast pins that the rows follow the
// provider's order — Place breaks running-count ties by it — with gone rows
// pushed behind the live ones.
func TestMergeKeepsResponseOrderAndGoneRowsLast(t *testing.T) {
	at := reportTime(8, 0)
	table := &NodeTable{Nodes: []NodeRow{
		buildRow(nil),
		buildRow(func(r *NodeRow) { r.ID = "b2" }),
		buildRow(func(r *NodeRow) { r.ID = "b3"; r.Gone = true; r.LastSeen = reportTime(7, 0) }),
	}}
	events := table.Merge(at, mergeResp("30s", nil,
		outNode(func(n *ReportNodeOut) { n.ID = "b2" }),
		outNode(nil),
	))
	if len(table.Nodes) != 3 {
		t.Fatalf("rows = %+v, want b2, b1 and the still-gone b3", table.Nodes)
	}
	if table.Nodes[0].ID != "b2" || table.Nodes[1].ID != "b1" || table.Nodes[2].ID != "b3" {
		t.Errorf("order = %s, %s, %s, want b2, b1, b3", table.Nodes[0].ID, table.Nodes[1].ID, table.Nodes[2].ID)
	}
	if !table.Nodes[2].Gone || !table.Nodes[2].LastSeen.Equal(reportTime(7, 0)) {
		t.Errorf("gone row = %+v, want it still gone and last seen when it was last listed", table.Nodes[2])
	}
	if events != nil {
		t.Errorf("events = %+v, want none for known ids and an already-gone row", events)
	}
}

func TestMergeClearsVerifiedWhenTheAddressChanges(t *testing.T) {
	otherFingerprint := "sha256:" + strings.Repeat("ab", 32)
	tests := []struct {
		name         string
		mutate       func(*ReportNodeOut)
		wantVerified bool
	}{
		{name: "a url change", mutate: func(n *ReportNodeOut) { n.URL = "https://9.9.9.9:7777" }, wantVerified: false},
		{name: "a fingerprint change", mutate: func(n *ReportNodeOut) { n.Fingerprint = otherFingerprint }, wantVerified: false},
		{name: "the same offer", mutate: nil, wantVerified: true},
		{name: "a name change", mutate: func(n *ReportNodeOut) { n.Name = "b1 cx44@hel1" }, wantVerified: true},
		{name: "a cap change", mutate: func(n *ReportNodeOut) { n.Cap = 8 }, wantVerified: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			at := reportTime(8, 0)
			table := &NodeTable{Nodes: []NodeRow{buildRow(nil)}}
			table.Merge(at, mergeResp("30s", nil, outNode(tt.mutate)))
			row := table.Nodes[0]
			if row.Verified != tt.wantVerified {
				t.Errorf("verified = %v, want %v", row.Verified, tt.wantVerified)
			}
			want := outNode(tt.mutate)
			if row.Name != want.Name || row.URL != want.URL || row.Fingerprint != want.Fingerprint || row.Cap != want.Cap {
				t.Errorf("offer = %+v, want the response's %+v", row, want)
			}
		})
	}
}

// TestMergeCarriesHeadStateForward pins that only the address clears
// Verified: a problem the head saw, since when, and the last forward survive
// an offer change, because the node's identity is what was pinned, not its
// price or size.
func TestMergeCarriesHeadStateForward(t *testing.T) {
	at := reportTime(8, 0)
	fwd := reportTime(7, 40)
	prev := buildRow(func(r *NodeRow) {
		r.Problem = ReportProblemFingerprint
		r.ProblemSince = reportTime(7, 30)
		r.ForwardedAt = atPtr(fwd)
	})
	table := &NodeTable{Nodes: []NodeRow{prev}}
	table.Merge(at, mergeResp("30s", nil, outNode(func(n *ReportNodeOut) { n.URL = "https://9.9.9.9:7777" })))
	row := table.Nodes[0]
	if row.Verified {
		t.Errorf("verified = true, want false after the url changed")
	}
	if row.Problem != ReportProblemFingerprint || !row.ProblemSince.Equal(reportTime(7, 30)) {
		t.Errorf("problem = %q since %v, want it carried with its time", row.Problem, row.ProblemSince)
	}
	if row.ForwardedAt == nil || !row.ForwardedAt.Equal(fwd) {
		t.Errorf("forwarded_at = %v, want it carried", row.ForwardedAt)
	}
	if !row.LastSeen.Equal(at) {
		t.Errorf("last_seen = %v, want the merge time %v", row.LastSeen, at)
	}
}

func TestMergeDrainingTransitionEvents(t *testing.T) {
	at := reportTime(8, 0)
	drain := outNode(func(n *ReportNodeOut) { n.State = "draining" })
	tests := []struct {
		name string
		node ReportNodeOut
		want []NodeEvent
	}{
		{name: "ready to draining fires", node: drain, want: []NodeEvent{{Kind: EventDraining, NodeID: "b1"}}},
		{name: "still draining fires nothing", node: drain, want: nil},
		{name: "back to ready fires nothing", node: outNode(nil), want: nil},
	}
	table := &NodeTable{Nodes: []NodeRow{buildRow(nil)}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := table.Merge(at, mergeResp("30s", nil, tt.node))
			if !reflect.DeepEqual(events, tt.want) {
				t.Errorf("events = %+v, want %+v", events, tt.want)
			}
			if table.Nodes[0].State != tt.node.State {
				t.Errorf("state = %q, want %q", table.Nodes[0].State, tt.node.State)
			}
		})
	}
}

func TestMergeMarksGoneNodesOnce(t *testing.T) {
	at := reportTime(8, 0)
	table := &NodeTable{Nodes: []NodeRow{buildRow(nil), buildRow(func(r *NodeRow) { r.ID = "b2" })}}
	events := table.Merge(at, mergeResp("30s", nil, outNode(nil)))
	want := []NodeEvent{{Kind: EventGone, NodeID: "b2"}}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %+v, want %+v", events, want)
	}
	row := table.Nodes[1]
	if !row.Gone || row.ID != "b2" {
		t.Errorf("row = %+v, want b2 marked gone and kept for the requeue that owns its forwards", row)
	}
	events = table.Merge(reportTime(8, 1), mergeResp("30s", nil, outNode(nil)))
	if events != nil {
		t.Errorf("events = %+v, want no repeat gone for an already-gone row", events)
	}
}

// TestMergeRevivesAGoneNodeWithoutAnEvent pins the only way a gone row
// leaves Gone: the provider lists the id again. Listing it is the whole
// correction, so the head learns from the row, not from an event.
func TestMergeRevivesAGoneNodeWithoutAnEvent(t *testing.T) {
	at := reportTime(8, 0)
	table := &NodeTable{Nodes: []NodeRow{
		buildRow(nil),
		buildRow(func(r *NodeRow) { r.ID = "b2"; r.Gone = true }),
	}}
	events := table.Merge(at, mergeResp("30s", nil,
		outNode(func(n *ReportNodeOut) { n.ID = "b2" }),
		outNode(nil),
	))
	if events != nil {
		t.Errorf("events = %+v, want none: a reappearing id is neither new nor newly stopping", events)
	}
	if table.Nodes[0].Gone || table.Nodes[0].Verified != true {
		t.Errorf("revived row = %+v, want Gone cleared and its state kept while the address is unchanged", table.Nodes[0])
	}
}

func TestMergeDeniedEvents(t *testing.T) {
	at := reportTime(8, 0)
	table := &NodeTable{}
	steps := []struct {
		name   string
		denied *Denied
		want   []NodeEvent
	}{
		{name: "first denial fires", denied: &Denied{Reason: "budget", RetryAfter: "1h"}, want: []NodeEvent{{Kind: EventDenied}}},
		{name: "the same reason fires nothing", denied: &Denied{Reason: "budget", RetryAfter: "2h"}},
		{name: "a new reason fires", denied: &Denied{Reason: "cap", RetryAfter: "1h"}, want: []NodeEvent{{Kind: EventDenied}}},
		{name: "the denial lifting fires nothing", denied: nil},
		{name: "a denial after a clear fires again", denied: &Denied{Reason: "budget", RetryAfter: "1h"}, want: []NodeEvent{{Kind: EventDenied}}},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			events := table.Merge(at, mergeResp("30s", func(r *ReportResponse) { r.Denied = st.denied }))
			if !reflect.DeepEqual(events, st.want) {
				t.Errorf("events = %+v, want %+v", events, st.want)
			}
			if !reflect.DeepEqual(table.Denied, st.denied) {
				t.Errorf("denied = %+v, want the response's %+v", table.Denied, st.denied)
			}
		})
	}
}

func TestMergePreparingEvents(t *testing.T) {
	at := reportTime(8, 0)
	table := &NodeTable{}
	steps := []struct {
		name      string
		preparing *Preparing
		want      []NodeEvent
	}{
		{name: "an ask fires", preparing: &Preparing{Slots: 2, Since: reportTime(7, 50)}, want: []NodeEvent{{Kind: EventPreparing}}},
		{name: "the same ask fires nothing", preparing: &Preparing{Slots: 2, Since: reportTime(7, 50)}},
		{name: "a newer grant fires", preparing: &Preparing{Slots: 2, Since: reportTime(7, 55)}, want: []NodeEvent{{Kind: EventPreparing}}},
		{name: "preparing done fires nothing", preparing: nil},
		{name: "another ask fires", preparing: &Preparing{Slots: 5, Since: reportTime(7, 58)}, want: []NodeEvent{{Kind: EventPreparing}}},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			events := table.Merge(at, mergeResp("30s", func(r *ReportResponse) { r.Preparing = st.preparing }))
			if !reflect.DeepEqual(events, st.want) {
				t.Errorf("events = %+v, want %+v", events, st.want)
			}
			if !reflect.DeepEqual(table.Preparing, st.preparing) {
				t.Errorf("preparing = %+v, want the response's %+v", table.Preparing, st.preparing)
			}
		})
	}
}

// TestMergeFiresEventsInKindOrder pins the slice a caller reads: node events
// in response order, then the rows that vanished, then the table blocks —
// added, draining, gone, denied, preparing.
func TestMergeFiresEventsInKindOrder(t *testing.T) {
	at := reportTime(8, 0)
	table := &NodeTable{Nodes: []NodeRow{
		buildRow(nil),
		buildRow(func(r *NodeRow) { r.ID = "b5" }),
		buildRow(func(r *NodeRow) { r.ID = "b3"; r.Gone = true }),
	}}
	events := table.Merge(at, mergeResp("30s", func(r *ReportResponse) {
		r.Denied = &Denied{Reason: "budget", RetryAfter: "1h"}
		r.Preparing = &Preparing{Slots: 2, Since: reportTime(7, 50)}
	},
		outNode(func(n *ReportNodeOut) { n.ID = "b4" }),
		outNode(func(n *ReportNodeOut) { n.State = "draining" }),
	))
	want := []NodeEvent{
		{Kind: EventAdded, NodeID: "b4"},
		{Kind: EventDraining, NodeID: "b1"},
		{Kind: EventGone, NodeID: "b5"},
		{Kind: EventDenied},
		{Kind: EventPreparing},
	}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %+v, want %+v", events, want)
	}
	if len(table.Nodes) != 4 {
		t.Errorf("rows = %+v, want b4, b1, the gone b5 and the still-gone b3", table.Nodes)
	}
}

// TestMergeRecordsTheCadenceBlocksAndClearsTheError pins the marks a good
// response leaves behind: both timestamps at the report time, the interval
// parsed from the response, the blocks kept whole, and the last error gone.
func TestMergeRecordsTheCadenceBlocksAndClearsTheError(t *testing.T) {
	at := reportTime(8, 0)
	old := &Preparing{Slots: 1, Since: reportTime(7, 0)}
	table := &NodeTable{
		Nodes:      []NodeRow{buildRow(nil)},
		LastReport: reportTime(7, 30),
		LastGood:   reportTime(7, 0),
		LastErr:    "burst report: exit 1",
		Interval:   10 * time.Second,
		Preparing:  old,
	}
	denied := &Denied{Reason: "no_capacity", RetryAfter: "30m"}
	preparing := &Preparing{Slots: 3, Since: reportTime(7, 59)}
	table.Merge(at, mergeResp("", func(r *ReportResponse) {
		r.Denied = denied
		r.Preparing = preparing
	}, outNode(nil)))
	if table.Interval != DefaultReportEvery {
		t.Errorf("interval = %v, want the 30s default for a missing report_every", table.Interval)
	}
	if !table.LastReport.Equal(at) || !table.LastGood.Equal(at) {
		t.Errorf("marks = %v/%v, want both at %v", table.LastReport, table.LastGood, at)
	}
	if table.LastErr != "" {
		t.Errorf("last_err = %q, want cleared by a good response", table.LastErr)
	}
	if !reflect.DeepEqual(table.Denied, denied) || !reflect.DeepEqual(table.Preparing, preparing) {
		t.Errorf("blocks = %+v/%+v, want the response's", table.Denied, table.Preparing)
	}
}
