package view

import "time"

// pullGrace is how long a pending payload on a pull route may sit before the
// statusline treats the absence of a live wait as the human's problem.
//
// It exists for the window where the payload is written but the wait that will
// collect it has not registered, or registered and stopped being readable. It
// is deliberately much longer than the wait registration's own TTL: a wait that
// refreshes every poll must not escalate the row because one refresh was late.
const pullGrace = 2 * time.Minute

// pastPullGrace reports whether b's pending payload has waited longer than
// pullGrace. It reads Pending.TS, the pending entry's own TS, because that is
// when the payload was written; LastPayload.TS is the newest payload of either
// direction and would date the report from a later question.
//
// A zero Pending.TS -- a payload whose entry predates the field -- reads fresh
// and never escalates: no age is no evidence, and escalating on it would put
// every such row on NEEDS YOU at once.
func pastPullGrace(b BindingStatus, now time.Time) bool {
	if b.Pending == nil || b.Pending.TS.IsZero() {
		return false
	}
	return now.Sub(b.Pending.TS) > pullGrace
}

// pendingStalled reports whether b's pending payload is a fault the human has
// to fix. Which route that is depends on the route, and the pull arm has to
// answer a question the route alone cannot.
func pendingStalled(b BindingStatus, pending bool, now time.Time) bool {
	switch {
	case !pending:
		// Nothing waiting: a delivered report is REPORT IN, not a stall.
		return false
	case b.MasterMindRouteLive:
		// A live push route never stalls, however old the payload is. The
		// elapsed time says nothing about whether a human must act -- the
		// route's own liveness is the whole answer, and it stays that way
		// whatever the wait reports.
		return false
	case b.MasterMindRoute != "pull":
		// A route that is neither live nor a pull is a deliverer that cannot
		// deliver: nobody is coming, so it escalates at once, as it always did.
		return true
	default:
		// pull is a route, not a fault: the background `relevo wait` is how a
		// tools-mode mastermind collects its report, and the daemon cannot see
		// that wait from the route. So a pull payload reads REPORT IN through
		// the grace, and escalates only once it has waited past the grace with
		// no wait registered to collect it.
		return !b.WaitLive && pastPullGrace(b, now)
	}
}
