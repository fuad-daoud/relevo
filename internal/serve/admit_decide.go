package serve

import "time"

// seatCost is one round's memory footprint for the admission decision: the
// base every running round holds plus the burst the K rule reserves against
// the largest single round.
type seatCost struct {
	Base  int64
	Burst int64
}

// Wait reasons a queued round reports on its queue view, so a client need not
// guess from counts which gate holds it.
const (
	waitMemory   = "memory"
	waitCeiling  = "ceiling"
	waitPressure = "pressure"
)

// fairnessWait bounds how long a memory-held round waits before it blocks
// later rounds of its own class. A blocked class still skips rather than
// force-starts: the wait names the hold, it never overrides it.
const fairnessWait = 10 * time.Minute

// decide answers whether one queued candidate may start. The ceiling holds
// first and the pressure brake second, both without consulting memory. Past
// them, the memory rule holds the base of every running round plus the
// candidate against the budget with K times the largest burst on top: one
// burst at a time is the most that can land together. An oversize candidate
// alone in its class starts anyway, since nothing it waits on would free a
// seat. Fairness is not decided here; the pass tracks it.
func decide(running []seatCost, cand seatCost, budget int64, k int, classCount, ceiling int, pressureOK bool) (start bool, wait string, waitBytes int64) {
	if classCount >= ceiling {
		return false, waitCeiling, 0
	}
	if !pressureOK {
		return false, waitPressure, 0
	}
	if k < 1 {
		k = 1
	}
	var bases int64
	peak := cand.Burst
	for _, r := range running {
		bases += r.Base
		if r.Burst > peak {
			peak = r.Burst
		}
	}
	needed := bases + cand.Base + int64(k)*peak
	if alone := cand.Base + int64(k)*cand.Burst; alone > budget && classCount == 0 {
		return true, "", 0
	}
	if needed <= budget {
		return true, "", 0
	}
	return false, waitMemory, needed - budget
}

// queueWait is one queued round's held reason: what holds it and, for a
// memory hold, the shortfall in bytes. A round the pass would start carries
// neither, so no stale wait survives a start.
type queueWait struct {
	Wait      string
	WaitBytes int64
}

// waitedTooLong reports whether a queued round has waited past the fairness
// horizon. A zero stamp never blocks, so a round queued before stamping
// existed cannot starve its class.
func waitedTooLong(queuedAt, now time.Time) bool {
	return !queuedAt.IsZero() && now.Sub(queuedAt) >= fairnessWait
}

// runningCostsOf lifts one pass's running rounds to the slice decide reads.
func runningCostsOf(rounds []runningRound) []seatCost {
	costs := make([]seatCost, 0, len(rounds))
	for _, r := range rounds {
		costs = append(costs, seatCost{Base: r.Base, Burst: r.Burst})
	}
	return costs
}

// queueWaits simulates the pass over a census in FIFO order, aligned with
// c.Queued: the same blocked rules as the admission pass, assuming a
// decide-start would start. A fairness-skipped round stamps a memory wait
// with no bytes, since the decision never ran to measure one.
func queueWaits(c census, budget int64, k int, pressureOK bool, now time.Time) []queueWait {
	running := runningCostsOf(c.RunningRounds)
	builders, planners := c.Running, c.PlannersRunning
	var blockedBuilder, blockedPlanner bool
	out := make([]queueWait, len(c.Queued))
	for i, q := range c.Queued {
		classCount, ceiling := builders, c.BuilderCap
		blocked := blockedBuilder
		if q.Planner {
			classCount, ceiling = planners, c.PlannerCap
			blocked = blockedPlanner
		}
		if blocked {
			out[i] = queueWait{Wait: waitMemory}
			continue
		}
		start, wait, waitBytes := decide(running, seatCost{Base: q.Base, Burst: q.Burst}, budget, k, classCount, ceiling, pressureOK)
		if !start {
			out[i] = queueWait{Wait: wait, WaitBytes: waitBytes}
			if wait == waitMemory && waitedTooLong(q.QueuedAt, now) {
				if q.Planner {
					blockedPlanner = true
				} else {
					blockedBuilder = true
				}
			}
			continue
		}
		if q.Planner {
			planners++
		} else {
			builders++
		}
		running = append(running, seatCost{Base: q.Base, Burst: q.Burst})
	}
	return out
}
