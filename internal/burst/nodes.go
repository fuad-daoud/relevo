package burst

import (
	"time"
)

// Event kinds Merge reports: what changed for one node, or for the table as
// a whole. NodeID is "" on the table kinds.
const (
	EventAdded     = "added"
	EventDraining  = "draining"
	EventGone      = "gone"
	EventDenied    = "denied"
	EventPreparing = "preparing"
)

// NodeEvent is one change a merge made visible: a node appeared, started
// draining or vanished, or the table's denied or preparing block moved. The
// head logs these and drives its hooks from them, so they fire only on a
// change: the merge runs every interval, and a standing state re-reported
// each time is noise, not an event.
type NodeEvent struct {
	Kind   string
	NodeID string
}

// NodeRow is one node as the head knows it: the last response's offer, the
// head's two stop signals beside the moment the current Problem was set —
// the head requeues a node's rounds once a problem has outlasted
// node_lost_after — the time it last forwarded there, reported per node,
// and the lifecycle marks. LastSeen is the at of the last response that
// listed the node; Gone marks a row the responses stopped listing, kept
// until the head has requeued what it forwarded there. The zero time means
// never, and a nil ForwardedAt means no round has been sent yet.
type NodeRow struct {
	ID          string
	Name        string
	URL         string
	Fingerprint string
	Cap         int
	State       string // ready|draining, as last offered

	Verified     bool
	Problem      string // "" means the head saw no problem
	ProblemSince time.Time

	ForwardedAt *time.Time
	LastSeen    time.Time
	Gone        bool
}

// NodeTable is the head's node list between reports: rows in provider
// response order, plus one report cycle's marks. BuildReport counts the
// rows into a request, Merge folds a response in, and Due reads the marks
// to say when the next report is owed. The head persists the whole table as
// one JSON value, so a restart re-owns its nodes without a report: a row
// survives a restart with its offer, its verified answer and its problem.
//
// The rows are kept in the order the provider listed them because Place
// breaks a running-count tie by that order: the provider sorts its list by
// price per seat and the head reads no prices, so re-sorting the rows would
// quietly change placement. Gone rows sit behind the live ones in the order
// they went.
type NodeTable struct {
	Nodes []NodeRow

	// LastReport is when a report was last sent, good or failed, and
	// LastGood when a response was last merged; they differ exactly while
	// calls fail. Due measures from LastReport, so a failed call costs a
	// full interval instead of a fast retry, while LastGood still says how
	// stale the list is.
	LastReport time.Time
	LastGood   time.Time

	// Interval is the last good response's report_every, parsed and
	// clamped; zero before the first response, which reads as overdue.
	Interval time.Duration

	LastErr   string // why the last call failed; "" after a good response
	Preparing *Preparing
	Denied    *Denied
}

// Merge folds a validated response into the table as of the report time
// at, and returns what changed. A response that has not passed through
// ValidateResponse must not come here: unique ids, ready-or-draining state
// and an addressable URL are what every later read of a row takes on faith.
func (t *NodeTable) Merge(at time.Time, resp ReportResponse) []NodeEvent {
	at = at.UTC()
	rows, events := mergeRows(at, t.Nodes, resp.Nodes)
	if resp.Denied != nil && !sameDenial(t.Denied, resp.Denied) {
		events = append(events, NodeEvent{Kind: EventDenied})
	}
	if resp.Preparing != nil && !samePreparing(t.Preparing, resp.Preparing) {
		events = append(events, NodeEvent{Kind: EventPreparing})
	}
	t.Nodes = rows
	t.Denied = resp.Denied
	t.Preparing = resp.Preparing
	t.LastReport = at
	t.LastGood = at
	t.LastErr = ""
	t.Interval = ParseReportEvery(resp.ReportEvery)
	return events
}

// mergeRows rewrites the rows in response order: offered ids update in
// place, unknown ids join unverified, and rows the response left out are
// marked gone and kept behind the live ones.
func mergeRows(at time.Time, prev []NodeRow, offered []ReportNodeOut) ([]NodeRow, []NodeEvent) {
	index := make(map[string]int, len(prev))
	for i := range prev {
		index[prev[i].ID] = i
	}
	listed := make(map[string]bool, len(offered))
	rows := make([]NodeRow, 0, len(prev)+len(offered))
	var events []NodeEvent
	for _, n := range offered {
		listed[n.ID] = true
		row := NodeRow{
			ID: n.ID, Name: n.Name, URL: n.URL, Fingerprint: n.Fingerprint,
			Cap: n.Cap, State: n.State, LastSeen: at,
		}
		i, known := index[n.ID]
		if !known {
			// A new id joins unverified: the offer names an endpoint, it
			// does not prove the worker there answers with the pinned
			// fingerprint; only the head's own check can verify a node.
			events = append(events, NodeEvent{Kind: EventAdded, NodeID: n.ID})
		} else {
			p := &prev[i]
			row.Verified = p.Verified && p.URL == n.URL && p.Fingerprint == n.Fingerprint
			row.Problem, row.ProblemSince, row.ForwardedAt = p.Problem, p.ProblemSince, p.ForwardedAt
			if p.State == "ready" && n.State == "draining" {
				events = append(events, NodeEvent{Kind: EventDraining, NodeID: n.ID})
			}
		}
		rows = append(rows, row)
	}
	for i := range prev {
		p := &prev[i]
		if listed[p.ID] {
			continue
		}
		// Gone is the provider's own disappearance: the head stops
		// reporting the node and requeues its forwards, but keeps the row
		// — its offer and the head's problem and forward state are what
		// the requeue and a reappearance need.
		if !p.Gone {
			p.Gone = true
			events = append(events, NodeEvent{Kind: EventGone, NodeID: p.ID})
		}
		rows = append(rows, *p)
	}
	return rows, events
}

// sameDenial reads a denial by its reason: the reason names the episode
// (budget, cap, no_capacity, failing), while a moving retry window inside
// one episode is an update, not a new denial.
func sameDenial(a, b *Denied) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Reason == b.Reason
}

// samePreparing reads a preparing block by both fields: a new grant has a
// new since, and a changed slot count is a different ask, so any difference
// is another preparing episode.
func samePreparing(a, b *Preparing) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Slots == b.Slots && a.Since.Equal(b.Since)
}
