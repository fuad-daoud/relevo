package serve

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// cpuWantMilli calculates the milli-cores of CPU demand from cgroup usage and
// pressure. A pinned round can never consume more CPU time than its pin
// allows, so CPU consumed alone would hold a starved class at its pin. The
// stall term adds the runnable time spent waiting for a CPU: cpu_ms is total
// consumed CPU time in milliseconds (usage_usec/1000), and cpu_some_usec is
// cpu.pressure some total, the microseconds at least one task waited.
func cpuWantMilli(r *store.Rusage, wall time.Duration) (int64, bool) {
	if r == nil || r.CPUMS <= 0 || r.CPUSomeUsec == nil || *r.CPUSomeUsec < 0 || wall <= 0 || wall.Microseconds() <= 0 {
		return 0, false
	}
	usec := r.CPUMS*1000 + *r.CPUSomeUsec
	return usec * 1000 / wall.Microseconds(), true
}

// cpuWantOf derives the peak CPU demand of a closed round and its gate. The
// gate joins because it runs on the round's assigned CPUs sequentially with
// the round, so the peak demand is the max rather than a sum.
func cpuWantOf(e store.LogEntry, start time.Time) (*int64, bool) {
	var roundVal int64
	var roundOK bool
	if !start.IsZero() {
		roundVal, roundOK = cpuWantMilli(e.Rusage, e.TS.Sub(start))
	}
	var gateVal int64
	var gateOK bool
	if e.Gate != nil && e.Gate.Rusage != nil {
		gateVal, gateOK = cpuWantMilli(e.Gate.Rusage, time.Duration(e.Gate.DurationMS)*time.Millisecond)
	}
	if !roundOK && !gateOK {
		return nil, false
	}
	val := roundVal
	if gateOK && (!roundOK || gateVal > val) {
		val = gateVal
	}
	return &val, true
}
