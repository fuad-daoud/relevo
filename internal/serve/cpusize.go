package serve

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

const (
	cpuSizeMin = 1
	// cpuSizeMax bounds what history can learn: a round's pin is a share of
	// a small shared pool, and wanted undercounts when stalled, so growth is
	// stepwise and bounded.
	cpuSizeMax = 4
)

// lookupCPUSize answers the CPU set size a repo-role's history learned: the
// ceil of the p90 of the samples' CPUWant, clamped to [cpuSizeMin,
// cpuSizeMax]. p90 leaves the rare oversized round out of the everyday size,
// the same rank statistic the seat uses. Fewer than seatMinSamples samples
// with CPU data answer ok false -- with a short history the rank sits on the
// max, as for the seat, so the configured size is the steadier answer. A nil
// CPUWant carries no reading and is skipped, never counted as zero.
func lookupCPUSize(samples []costSample) (size int, ok bool) {
	values := make([]int64, 0, len(samples))
	for _, s := range samples {
		if s.CPUWant != nil {
			values = append(values, *s.CPUWant)
		}
	}
	if len(values) < seatMinSamples {
		return 0, false
	}
	milli := nearestRank(values, 90)
	size = min(max(int((milli+999)/1000), cpuSizeMin), cpuSizeMax)
	return size, true
}

// roundCPUSizeFor answers the RoundCPUSize seam for one live snapshot: the
// CPU set size a served round should get from its repo-role cost history,
// ok false when the history is too short to learn from. Same shape as
// roundBurstFor.
func roundCPUSizeFor(root string) func(store.Binding) (int, bool) {
	return func(b store.Binding) (int, bool) {
		return lookupCPUSize(costSamplesFor(root, b))
	}
}
