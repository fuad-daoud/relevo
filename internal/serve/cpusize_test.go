package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

func cpuCostPtr(v int64) *int64 { return &v }

// wantSamplesWithCPU builds n samples all carrying the same CPUWant, the
// history one round class closes round after round.
func wantSamplesWithCPU(n int, want int64) []costSample {
	samples := make([]costSample, n)
	for i := range samples {
		samples[i] = costSample{Base: 1 << 30, Burst: 2 << 30, CPUWant: cpuCostPtr(want), At: time.Now()}
	}
	return samples
}

// TestLookupCPUSize pins the learned size: the ceil of the p90 of the
// samples' CPUWant, clamped to [1, 4], from enough history; too few samples
// with CPU data answer not ok, and a nil CPUWant is skipped, never read as
// zero, so a mix counts only the samples that carry a reading.
func TestLookupCPUSize(t *testing.T) {
	t.Parallel()

	// p90 of [1100..1900] (rank 5 of 5) is 1900 -> ceil 2; a sample over
	// 2000 becomes the p90 -> ceil 3. p90 of 0 milli pins the lower clamp,
	// p90 of 7000 milli the upper one.
	cases := []struct {
		name    string
		samples []costSample
		want    int
		ok      bool
	}{
		{"no samples", nil, 0, false},
		{"four samples is too few", wantSamplesWithCPU(4, 1900), 0, false},
		{"1100..1900 -> 2", growingSamples(), 2, true},
		{"one over 2000 pushes p90 to 3", overPinnedSamples(), 3, true},
		{"planner 120 milli -> 1", wantSamplesWithCPU(5, 120), 1, true},
		{"0 milli clamps to 1", wantSamplesWithCPU(5, 0), 1, true},
		{"7000 milli clamps to 4", wantSamplesWithCPU(5, 7000), 4, true},
		{"ten without CPU data plus four with -> only the four count",
			append(mixedSamples(10, 0), wantSamplesWithCPU(4, 200)...), 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := lookupCPUSize(c.samples)
			if ok != c.ok || got != c.want {
				t.Errorf("lookupCPUSize = (%d, %v); want (%d, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

// growingSamples is five samples 1100..1900 milli.
func growingSamples() []costSample {
	samples := make([]costSample, 5)
	for i := range samples {
		samples[i] = costSample{CPUWant: cpuCostPtr(int64(1100 + i*200)), At: time.Now()}
	}
	return samples
}

// overPinnedSamples is five ordinary samples plus one over 2000, so the p90
// ranks at the max and the size grows a step.
func overPinnedSamples() []costSample {
	return append(growingSamples(), costSample{CPUWant: cpuCostPtr(2600), At: time.Now()})
}

// mixedSamples builds n samples without CPU data, the shape every round
// closed before CPU recording had.
func mixedSamples(n, burst int64) []costSample {
	samples := make([]costSample, n)
	for i := range samples {
		samples[i] = costSample{Base: 3 << 30, Burst: burst, At: time.Now()}
	}
	return samples
}

// TestLookupCPUSizeNilLeavesMemoryUnseen pins that a nil CPUWant only leaves
// the CPU lookup out: lookupSeat over the same mixed history still answers
// from all 14 samples, memory being unaffected.
func TestLookupCPUSizeNilLeavesMemoryUnseen(t *testing.T) {
	t.Parallel()

	samples := append(mixedSamples(10, 5<<30), wantSamplesWithCPU(4, 200)...)
	base, burst := lookupSeat(samples, false, false)
	if base != 3<<30 || burst != 5<<30 {
		t.Fatalf("lookupSeat = (%d, %d), want (%d, %d) from all 14 samples", base, burst, 3<<30, 5<<30)
	}
}

// TestLookupCPUSizeStarvedClass pins the starved builder: a round pinned at
// two cores that used 1.9 and stalled 45% of its wall wanted 2350 milli, so
// its class grows above the 2-pin.
func TestLookupCPUSizeStarvedClass(t *testing.T) {
	t.Parallel()

	// The way a pinned-at-2 morphic builder reports: CPUMS 1900 (1.9 cores
	// over a 1s wall) and cpu_some 450000 usec (45% of it).
	r := store.Rusage{CPUMS: 1900, CPUSomeUsec: cpuCostPtr(450000)}
	want, ok := cpuWantMilli(&r, time.Second)
	if !ok || want != 2350 {
		t.Fatalf("cpuWantMilli = (%d, %v), want (2350, true)", want, ok)
	}
	samples := wantSamplesWithCPU(5, want)
	size, ok := lookupCPUSize(samples)
	if !ok || size != 3 {
		t.Fatalf("lookupCPUSize = (%d, %v), want (3, true), above the 2-pin", size, ok)
	}
}

// TestRoundCPUSizeFor pins the user's path: the seam reads the binding's
// repo-role cost history through the same file appendCostSample wrote, and a
// binding with no serve facts, no root, a corrupt file, or another repo or
// role -- which has no history of its own -- answers not ok.
func TestRoundCPUSizeFor(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repoID := strings.Repeat("c", 64)
	role := "builder"
	b := store.Binding{Name: "m", Owner: "o", Serve: &store.ServeFacts{RepoID: repoID}}

	// No history at all yet.
	if _, ok := roundCPUSizeFor(root)(b); ok {
		t.Fatal("empty history answered ok, want not ok")
	}

	want := int64(2350)
	for i := 1; i <= 5; i++ {
		if _, err := appendCostSample(root, repoID, role, "o/m", i,
			costSample{Base: 1 << 30, Burst: 2 << 30, CPUWant: cpuCostPtr(want), At: time.Now()}); err != nil {
			t.Fatalf("appendCostSample: %v", err)
		}
	}
	size, ok := roundCPUSizeFor(root)(b)
	if !ok || size != 3 {
		t.Fatalf("roundCPUSizeFor = (%d, %v), want (3, true)", size, ok)
	}

	// A different role reads another file: none.
	if _, ok := roundCPUSizeFor(root)(store.Binding{
		Name: "m", Owner: "o", Role: "planner", Serve: &store.ServeFacts{RepoID: repoID},
	}); ok {
		t.Error("another role answered ok, want not ok (its file is empty)")
	}
	// A different repo reads another file: none.
	if _, ok := roundCPUSizeFor(root)(store.Binding{
		Name: "m", Owner: "o", Serve: &store.ServeFacts{RepoID: strings.Repeat("d", 64)},
	}); ok {
		t.Error("another repo answered ok, want not ok (its file is empty)")
	}
	// No serve facts, no history.
	if _, ok := roundCPUSizeFor(root)(store.Binding{Name: "m", Owner: "o"}); ok {
		t.Error("a binding without serve facts answered ok, want not ok")
	}
	// No root, no history.
	if _, ok := roundCPUSizeFor("")(b); ok {
		t.Error("an empty root answered ok, want not ok")
	}
	// A corrupt file answers not ok, never a guess.
	path := costFilePath(root, strings.Repeat("d", 64), role)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := roundCPUSizeFor(root)(store.Binding{
		Name: "m", Owner: "o", Serve: &store.ServeFacts{RepoID: strings.Repeat("d", 64)},
	}); ok {
		t.Error("a corrupt cost file answered ok, want not ok")
	}
}
