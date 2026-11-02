package burst

import "time"

// HeartbeatInterval is the idle report cadence: while nothing waits and no
// node is held, one report every 5 minutes keeps the provider's picture of
// the head current without a request loop over an empty pool. It equals
// MaxReportEvery, so an idle head never reports slower than the slowest
// cadence the contract allows a busy one to ask for.
const HeartbeatInterval = 5 * time.Minute

// Due says whether the table owes a report at time at. waiting is whether
// the head has rounds waiting and the reason is what the next report would
// carry: while anything waits or the provider still holds a node for the
// head, the head reports at the interval the last good response asked for,
// because every answer — new nodes, a denial, what is preparing — is built
// on that load; with nothing waiting and no node held, only the heartbeat
// applies.
//
// The clock runs from the last attempt, not the last good response:
// RecordFailure stamps a failed call, so a call that already ran and failed
// costs a full interval rather than a fast retry, while LastGood still
// marks the list's age. A table with no attempt yet has a zero mark and
// reads overdue, which is what makes the startup report go out.
func (t NodeTable) Due(at time.Time, waiting bool, reason string) bool {
	// A forward or a close changes what the next answer should be built
	// on — the head just took or repaid a seat — so it reports at once,
	// out of cycle. interval and startup only name the ordinary rhythm.
	switch reason {
	case ReportReasonForwarded, ReportReasonClosed:
		return true
	}
	hold := waiting
	for _, row := range t.Nodes {
		if !row.Gone {
			hold = true
			break
		}
	}
	interval := HeartbeatInterval
	if hold {
		interval = t.Interval
	}
	return at.Sub(t.LastReport) >= interval
}

// RecordFailure stamps a report call that brought back nothing usable. The
// attempt advances LastReport — the retry then waits a full interval — and
// records why, while rows, LastGood, the blocks and the interval are left
// as the last good response set them: the head keeps acting on a stale list
// rather than on no list.
func (t *NodeTable) RecordFailure(at time.Time, errText string) {
	t.LastReport = at.UTC()
	t.LastErr = errText
}
