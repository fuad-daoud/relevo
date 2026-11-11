package serve

import (
	"slices"
	"testing"
	"time"
)

func seatSamples(bases, bursts []int64) []costSample {
	samples := make([]costSample, len(bases))
	for i := range bases {
		burst := int64(0)
		if i < len(bursts) {
			burst = bursts[i]
		}
		samples[i] = costSample{Base: bases[i], Burst: burst, At: time.Now()}
	}
	return samples
}

func TestLookupSeatP90(t *testing.T) {
	outlierBases := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 1000}
	outlierBursts := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120, 130, 140, 150, 160, 170, 180, 190, 5000}
	zeroBaseSamples := seatSamples(
		[]int64{0, 0, 10, 20, 30, 40},
		[]int64{5, 15, 25, 35, 45, 55},
	)
	ignoredZeroSamples := seatSamples(
		[]int64{0, 0, 10, 20, 30, 40, 50, 60, 70, 80},
		[]int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
	)
	tests := []struct {
		name      string
		samples   []costSample
		wantBase  int64
		wantBurst int64
	}{
		{
			name:      "no samples take builder defaults",
			samples:   nil,
			wantBase:  1 << 30,
			wantBurst: 5 << 30,
		},
		{
			name:      "four samples still take builder defaults",
			samples:   seatSamples([]int64{10, 20, 30, 40}, []int64{5, 15, 25, 35}),
			wantBase:  1 << 30,
			wantBurst: 5 << 30,
		},
		{
			name:      "five samples switch to p90",
			samples:   seatSamples([]int64{10, 20, 30, 40, 50}, []int64{5, 15, 25, 35, 45}),
			wantBase:  50,
			wantBurst: 45,
		},
		{
			name:      "twenty samples ignore the outlier",
			samples:   seatSamples(outlierBases, outlierBursts),
			wantBase:  18,
			wantBurst: 180,
		},
		{
			name:      "zero bases fall back to the default while burst keeps p90",
			samples:   zeroBaseSamples,
			wantBase:  1 << 30,
			wantBurst: 55,
		},
		{
			name:      "zero bases are ignored once known bases suffice",
			samples:   ignoredZeroSamples,
			wantBase:  80,
			wantBurst: 9,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, burst := lookupSeat(tc.samples, false, false)
			if base != tc.wantBase || burst != tc.wantBurst {
				t.Fatalf("lookupSeat() = (%d, %d), want (%d, %d)", base, burst, tc.wantBase, tc.wantBurst)
			}
		})
	}
}

func TestLookupSeatDefaults(t *testing.T) {
	tests := []struct {
		name    string
		planner bool
		reader  bool
		base    int64
		burst   int64
	}{
		{name: "planner", planner: true, reader: false, base: 512 << 20, burst: 1 << 30},
		{name: "reviewer-shaped reader", planner: false, reader: true, base: 512 << 20, burst: 2 << 30},
		{name: "builder", planner: false, reader: false, base: 1 << 30, burst: 5 << 30},
		{name: "unknown role takes builder defaults", planner: false, reader: false, base: 1 << 30, burst: 5 << 30},
		{name: "planner wins over reader", planner: true, reader: true, base: 512 << 20, burst: 1 << 30},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, burst := lookupSeat(nil, tc.planner, tc.reader)
			if base != tc.base || burst != tc.burst {
				t.Fatalf("lookupSeat(nil, %v, %v) = (%d, %d), want (%d, %d)",
					tc.planner, tc.reader, base, burst, tc.base, tc.burst)
			}
		})
	}
}

func TestSeatBudget(t *testing.T) {
	const (
		gb10 = int64(10 << 30)
		gb9  = int64(9 << 30)
		gb16 = int64(16 << 30)
		gb14 = int64(16<<30 - 2<<30)
	)
	tests := []struct {
		name                          string
		sliceMax, nonRound, mem, over int64
		want                          int64
	}{
		{name: "override wins over a set slice", sliceMax: gb10, nonRound: gb10, mem: gb16, over: 100, want: 100},
		{name: "zero override falls through to the slice", sliceMax: gb10, nonRound: 1 << 30, mem: gb16, want: gb9},
		{name: "slice minus non-round bytes", sliceMax: gb10, nonRound: 1 << 30, mem: gb16, want: gb9},
		{name: "machine memory minus reserve", mem: gb16, want: gb14},
		{name: "negative slice result clamps to zero", sliceMax: 1 << 30, nonRound: 2 << 30, mem: gb16, want: 0},
		{name: "machine memory below reserve clamps to zero", mem: 1 << 30, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := seatBudget(tc.sliceMax, tc.nonRound, tc.mem, tc.over); got != tc.want {
				t.Fatalf("seatBudget(%d, %d, %d, %d) = %d, want %d",
					tc.sliceMax, tc.nonRound, tc.mem, tc.over, got, tc.want)
			}
		})
	}
}

func TestEstimateK(t *testing.T) {
	counts := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 100}
	tests := []struct {
		name    string
		counts  []int
		ceiling int
		want    int
	}{
		{name: "no history starts at two", counts: nil, ceiling: 8, want: 2},
		{name: "no history is clamped by the ceiling", counts: nil, ceiling: 1, want: 1},
		{name: "p95 ignores the outlier", counts: counts, ceiling: 100, want: 19},
		{name: "above ceiling clamps to ceiling", counts: counts, ceiling: 3, want: 3},
		{name: "all-zero counts floor to one", counts: []int{0, 0, 0, 0, 0, 0}, ceiling: 8, want: 1},
		{name: "ceiling below one reads as one", counts: counts, ceiling: 0, want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := estimateK(tc.counts, tc.ceiling); got != tc.want {
				t.Fatalf("estimateK(%v, %d) = %d, want %d", tc.counts, tc.ceiling, got, tc.want)
			}
		})
	}
}

func TestNearestRankEdges(t *testing.T) {
	if got := nearestRank(nil, 90); got != 0 {
		t.Fatalf("nearestRank(nil, 90) = %d, want 0", got)
	}
	values := []int64{30, 10, 20}
	if got := nearestRank(values, 0); got != 10 {
		t.Fatalf("nearestRank(values, 0) = %d, want 10", got)
	}
	if got := nearestRank(values, 101); got != 30 {
		t.Fatalf("nearestRank(values, 101) = %d, want 30", got)
	}
	if slices.Contains(values, 0) || values[0] != 30 {
		t.Fatalf("nearestRank modified its input: %v", values)
	}
}
