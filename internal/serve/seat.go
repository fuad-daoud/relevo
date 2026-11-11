package serve

import (
	"slices"
	"time"
)

// costSample is one closed round's memory footprint for the seat model. Plan 2
// appends these to the cost history file, so the JSON shape is the file's
// contract. A Base of 0 means the round produced no base reading, and lookups
// ignore it.
type costSample struct {
	Base  int64     `json:"base"`
	Burst int64     `json:"burst"`
	At    time.Time `json:"at"`
}

const (
	// seatMinSamples is the history depth below which the rank statistic sits
	// on the max, so the class default is the steadier answer.
	seatMinSamples = 5

	seatBuilderBase   = 1 << 30
	seatBuilderBurst  = 5 << 30
	seatReviewerBase  = 512 << 20
	seatReviewerBurst = 2 << 30
	seatPlannerBase   = 512 << 20
	seatPlannerBurst  = 1 << 30

	// seatBudgetReserve holds back machine memory outside any round's slice,
	// so the budget never promises the whole machine.
	seatBudgetReserve = 2 << 30
)

// nearestRank returns the pct-th percentile of values by the nearest-rank
// rule: rank ceil(pct/100*n), at least 1, counting from the sorted copy. One
// helper serves both the p90 seat lookup and the p95 K estimate.
func nearestRank(values []int64, pct int) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	rank := (pct*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// seatDefaults answers the class footprint the caller classified: planner
// rounds take planner defaults, consult-shaped readers take reviewer
// defaults, and anything else — builders and unknown roles — takes builder
// defaults.
func seatDefaults(planner, reader bool) (base, burst int64) {
	switch {
	case planner:
		return seatPlannerBase, seatPlannerBurst
	case reader:
		return seatReviewerBase, seatReviewerBurst
	default:
		return seatBuilderBase, seatBuilderBurst
	}
}

// lookupSeat answers a round's seat from cost history. The caller passes the
// classification as primitives, so this imports neither the role registry nor
// the round classifier. Base and burst are decided independently: the base
// p90 runs only over samples with a known base, falling back to the class
// default when those are too few, while the burst p90 runs over all samples.
func lookupSeat(samples []costSample, planner, reader bool) (base, burst int64) {
	baseDefault, burstDefault := seatDefaults(planner, reader)
	if len(samples) < seatMinSamples {
		return baseDefault, burstDefault
	}
	bursts := make([]int64, len(samples))
	known := make([]int64, 0, len(samples))
	for i, s := range samples {
		bursts[i] = s.Burst
		if s.Base > 0 {
			known = append(known, s.Base)
		}
	}
	if len(known) < seatMinSamples {
		return baseDefault, nearestRank(bursts, 90)
	}
	return nearestRank(known, 90), nearestRank(bursts, 90)
}

// seatBudget answers the memory available to rounds. An explicit override
// wins; otherwise the slice minus its non-round bytes, else machine memory
// minus the reserve. A slice smaller than its non-round bytes is no budget,
// not a negative one.
func seatBudget(sliceMax, nonRoundBytes, memTotal, override int64) int64 {
	var budget int64
	switch {
	case override > 0:
		budget = override
	case sliceMax > 0:
		budget = sliceMax - nonRoundBytes
	default:
		budget = memTotal - seatBudgetReserve
	}
	if budget < 0 {
		return 0
	}
	return budget
}

// estimateK answers the parallelism from past round counts: the p95 of the
// counts clamped to [1, ceiling], starting at two seats with no history. A
// ceiling below 1 reads as 1.
func estimateK(counts []int, ceiling int) int {
	if ceiling < 1 {
		ceiling = 1
	}
	if len(counts) == 0 {
		return min(2, ceiling)
	}
	values := make([]int64, len(counts))
	for i, c := range counts {
		values[i] = int64(c)
	}
	return min(max(int(nearestRank(values, 95)), 1), ceiling)
}
