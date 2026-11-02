package burst

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// reportTime names one wall-clock instant on the day the fixtures use; the
// fixtures never read a clock, so every time a test asserts is one it wrote.
func reportTime(hour, minute int) time.Time {
	return time.Date(2026, 11, 2, hour, minute, 0, 0, time.UTC)
}

func atPtr(t time.Time) *time.Time {
	return &t
}

// buildRow is one live, verified node of the table; a mutate turns it into
// the state the case is about.
func buildRow(mutate func(*NodeRow)) NodeRow {
	r := NodeRow{
		ID: "b1", Name: "b1 cx43@hel1", URL: "https://1.2.3.4:7777",
		Fingerprint: validFingerprint, Cap: 4, State: "ready",
		Verified: true, LastSeen: reportTime(7, 59),
	}
	if mutate != nil {
		mutate(&r)
	}
	return r
}

func buildCensus() Census {
	return Census{
		Head:    ReportHead{Name: "contabo", CPUs: 4, MemGB: 8},
		Local:   ReportLocal{Running: 1, Queued: 0, Cap: 3},
		Waiting: []ReportWaiting{waitingRound("w1")},
	}
}

func fwdRound(state, round, node string, minute int) Forward {
	return Forward{
		Round:  ReportRound{ID: round, Candidate: "cand-" + round, Started: reportTime(7, minute)},
		NodeID: node,
		State:  state,
	}
}

func TestBuildReportListsEveryLiveNodeIncludingIdleOnes(t *testing.T) {
	tests := []struct {
		name string
		rows []NodeRow
		fs   []Forward
		want []ReportNode
	}{
		{
			name: "a live node with no forwards is still reported at zero",
			rows: []NodeRow{buildRow(nil)},
			want: []ReportNode{{ID: "b1", Rounds: []ReportRound{}}},
		},
		{
			name: "a gone node is not reported",
			rows: []NodeRow{buildRow(nil), buildRow(func(r *NodeRow) { r.ID = "b2"; r.Gone = true })},
			want: []ReportNode{{ID: "b1", Rounds: []ReportRound{}}},
		},
		{
			name: "a forward on a gone node is not reported either",
			rows: []NodeRow{buildRow(nil), buildRow(func(r *NodeRow) { r.ID = "b2"; r.Gone = true })},
			fs:   []Forward{fwdRound(ForwardRunning, "r1", "b2", 30)},
			want: []ReportNode{{ID: "b1", Rounds: []ReportRound{}}},
		},
		{
			name: "the nodes keep the table order",
			rows: []NodeRow{buildRow(func(r *NodeRow) { r.ID = "b2" }), buildRow(nil)},
			want: []ReportNode{{ID: "b2", Rounds: []ReportRound{}}, {ID: "b1", Rounds: []ReportRound{}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, notes, err := BuildReport(buildCensus(), NodeTable{Nodes: tt.rows}, reportTime(8, 0), ReportReasonInterval, tt.fs)
			if err != nil {
				t.Fatalf("BuildReport: %v", err)
			}
			if len(notes) != 0 {
				t.Errorf("notes = %q, want none", notes)
			}
			if !reflect.DeepEqual(req.Nodes, tt.want) {
				t.Errorf("nodes = %+v, want %+v", req.Nodes, tt.want)
			}
		})
	}
}

// TestBuildReportCountsForwardsPerNode pins the three counts the provider
// acts on: running rounds (listed with their facts), queued rounds holding a
// seat, and closed-not-yet-acked rounds, per node.
func TestBuildReportCountsForwardsPerNode(t *testing.T) {
	fs := []Forward{
		fwdRound(ForwardRunning, "r1", "b1", 20),
		fwdRound(ForwardQueued, "r2", "b1", 0),
		fwdRound(ForwardUnacked, "r3", "b1", 10),
		fwdRound(ForwardRunning, "r4", "b2", 25),
	}
	rows := []NodeRow{buildRow(nil), buildRow(func(r *NodeRow) { r.ID = "b2" })}
	req, _, err := BuildReport(buildCensus(), NodeTable{Nodes: rows}, reportTime(8, 0), ReportReasonInterval, fs)
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}
	want := []ReportNode{
		{
			ID: "b1", Running: 1, Queued: 1, Unacked: 1,
			Rounds: []ReportRound{{ID: "r1", Candidate: "cand-r1", Started: reportTime(7, 20)}},
		},
		{
			ID: "b2", Running: 1,
			Rounds: []ReportRound{{ID: "r4", Candidate: "cand-r4", Started: reportTime(7, 25)}},
		},
	}
	if !reflect.DeepEqual(req.Nodes, want) {
		t.Errorf("nodes = %+v, want %+v", req.Nodes, want)
	}
}

func TestBuildReportCarriesHeadFactsWaitingAndNodeState(t *testing.T) {
	c := buildCensus()
	c.Local = ReportLocal{Running: 2, Queued: 1, Cap: 3}
	c.Waiting = []ReportWaiting{waitingRound("w1"), waitingRound("w2")}
	fwd := reportTime(7, 58)
	rows := []NodeRow{buildRow(func(r *NodeRow) {
		r.Problem = ReportProblemFingerprint
		r.ForwardedAt = atPtr(fwd)
	})}
	req, notes, err := BuildReport(c, NodeTable{Nodes: rows}, reportTime(8, 0), ReportReasonForwarded, nil)
	if err != nil || len(notes) != 0 {
		t.Fatalf("BuildReport = (%v, %q, %v), want no error and no notes", req, notes, err)
	}
	if req.Version != ReportVersion || req.Reason != ReportReasonForwarded {
		t.Errorf("version/reason = %d/%q, want %d/%q", req.Version, req.Reason, ReportVersion, ReportReasonForwarded)
	}
	if !req.At.Equal(reportTime(8, 0)) {
		t.Errorf("at = %v, want the given time", req.At)
	}
	if !reflect.DeepEqual(req.Head, c.Head) || !reflect.DeepEqual(req.Local, c.Local) {
		t.Errorf("head/local = %+v/%+v, want %+v/%+v", req.Head, req.Local, c.Head, c.Local)
	}
	if !reflect.DeepEqual(req.Waiting, c.Waiting) {
		t.Errorf("waiting = %+v, want %+v", req.Waiting, c.Waiting)
	}
	if len(req.Nodes) != 1 {
		t.Fatalf("nodes = %+v, want the one live row", req.Nodes)
	}
	n := req.Nodes[0]
	if n.Problem != ReportProblemFingerprint || n.ForwardedAt == nil || !n.ForwardedAt.Equal(fwd) {
		t.Errorf("node state carried as %+v, want the fingerprint problem and %v forwarded_at", n, fwd)
	}
}

func TestBuildReportRefusesUnusableInputs(t *testing.T) {
	tests := []struct {
		name     string
		census   Census
		rows     []NodeRow
		fs       []Forward
		contains string
	}{
		{
			name:     "a head without a name",
			census:   Census{Head: ReportHead{Name: "", CPUs: 4}, Local: ReportLocal{}, Waiting: []ReportWaiting{waitingRound("w1")}},
			contains: "head.name: required",
		},
		{
			name:     "a negative local count",
			census:   Census{Head: ReportHead{Name: "contabo"}, Local: ReportLocal{Running: -1}, Waiting: []ReportWaiting{waitingRound("w1")}},
			contains: "local.running: must be >= 0, got -1",
		},
		{
			name:     "a forward in an unknown state",
			census:   buildCensus(),
			rows:     []NodeRow{buildRow(nil)},
			fs:       []Forward{fwdRound("sending", "r1", "b1", 30)},
			contains: `forward r1: unknown state "sending"`,
		},
		{
			name:     "a forward on a node the table never held",
			census:   buildCensus(),
			rows:     []NodeRow{buildRow(nil)},
			fs:       []Forward{fwdRound(ForwardRunning, "r1", "zz", 30)},
			contains: `forward r1: unknown node "zz"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := BuildReport(tt.census, NodeTable{Nodes: tt.rows}, reportTime(8, 0), ReportReasonInterval, tt.fs)
			if !refusesWith(t, err, tt.contains) {
				t.Fatalf("err = %v, want a refusal naming %q", err, tt.contains)
			}
		})
	}
}

// TestBuildReportProducesAValidatedDocument pins the contract's normalisation
// on the built request: times arrive in UTC, an unrecognised reason becomes
// "" with a note, and empty lists are never a refusal — so what BuildReport
// hands back is what ValidateRequest accepts, and the provider decodes.
func TestBuildReportProducesAValidatedDocument(t *testing.T) {
	cest := time.FixedZone("CEST", 2*3600)

	t.Run("times arrive in UTC", func(t *testing.T) {
		c := buildCensus()
		c.Waiting = []ReportWaiting{{ID: "w1", Since: time.Date(2026, 11, 2, 9, 59, 0, 0, cest)}}
		rows := []NodeRow{buildRow(func(r *NodeRow) {
			r.ForwardedAt = atPtr(time.Date(2026, 11, 2, 10, 0, 0, 0, cest))
		})}
		req, notes, err := BuildReport(c, NodeTable{Nodes: rows}, time.Date(2026, 11, 2, 10, 0, 0, 0, cest), ReportReasonInterval, nil)
		if err != nil || len(notes) != 0 {
			t.Fatalf("BuildReport = (%v, %q, %v), want no error and no notes", req, notes, err)
		}
		if want := reportTime(8, 0); !req.At.Equal(want) || req.At.Location() != time.UTC {
			t.Errorf("at = %v, want %v in UTC", req.At, want)
		}
		if want := reportTime(7, 59); !req.Waiting[0].Since.Equal(want) {
			t.Errorf("waiting since = %v, want %v", req.Waiting[0].Since, want)
		}
		if want := reportTime(8, 0); !req.Nodes[0].ForwardedAt.Equal(want) {
			t.Errorf("forwarded_at = %v, want %v", req.Nodes[0].ForwardedAt, want)
		}
	})

	t.Run("an unrecognised reason is kept empty and noted", func(t *testing.T) {
		req, notes, err := BuildReport(buildCensus(), NodeTable{}, reportTime(8, 0), "heartbeat", nil)
		if err != nil {
			t.Fatalf("BuildReport: %v", err)
		}
		if req.Reason != "" {
			t.Errorf("reason = %q, want \"\"", req.Reason)
		}
		if want := []string{"reason"}; !reflect.DeepEqual(notes, want) {
			t.Errorf("notes = %q, want %q", notes, want)
		}
	})

	t.Run("an idle head reports empty lists", func(t *testing.T) {
		c := buildCensus()
		c.Waiting = nil
		req, notes, err := BuildReport(c, NodeTable{}, reportTime(8, 0), ReportReasonInterval, nil)
		if err != nil || len(notes) != 0 {
			t.Fatalf("BuildReport = (%+v, %q, %v), want an accepted idle report", req, notes, err)
		}
		if len(req.Waiting) != 0 || len(req.Nodes) != 0 {
			t.Errorf("waiting/nodes = %d/%d, want both empty", len(req.Waiting), len(req.Nodes))
		}
		body, merr := json.Marshal(req)
		if merr != nil {
			t.Fatalf("marshal: %v", merr)
		}
		if strings.Contains(string(body), `"waiting":null`) || strings.Contains(string(body), `"nodes":null`) {
			t.Errorf("request %s, want empty lists rather than nulls", body)
		}
	})
}
