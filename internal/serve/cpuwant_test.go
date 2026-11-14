package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestCPUWantMilliTable(t *testing.T) {
	t.Parallel()

	stallMorphic := int64(1620000000)
	stallPlanner := int64(72000000)
	negStall := int64(-1)

	tests := []struct {
		name    string
		rusage  *store.Rusage
		wall    time.Duration
		wantVal int64
		wantOK  bool
	}{
		{
			name: "starved morphic builder",
			rusage: &store.Rusage{
				CPUMS:       6840000,
				CPUSomeUsec: &stallMorphic,
			},
			wall:    time.Hour,
			wantVal: 2350,
			wantOK:  true,
		},
		{
			name: "planner-like",
			rusage: &store.Rusage{
				CPUMS:       360000,
				CPUSomeUsec: &stallPlanner,
			},
			wall:    time.Hour,
			wantVal: 120,
			wantOK:  true,
		},
		{
			name:    "nil rusage",
			rusage:  nil,
			wall:    time.Hour,
			wantVal: 0,
			wantOK:  false,
		},
		{
			name: "CPUMS is zero",
			rusage: &store.Rusage{
				CPUMS:       0,
				CPUSomeUsec: &stallMorphic,
			},
			wall:    time.Hour,
			wantVal: 0,
			wantOK:  false,
		},
		{
			name: "nil CPUSomeUsec",
			rusage: &store.Rusage{
				CPUMS:       6840000,
				CPUSomeUsec: nil,
			},
			wall:    time.Hour,
			wantVal: 0,
			wantOK:  false,
		},
		{
			name: "negative stall",
			rusage: &store.Rusage{
				CPUMS:       6840000,
				CPUSomeUsec: &negStall,
			},
			wall:    time.Hour,
			wantVal: 0,
			wantOK:  false,
		},
		{
			name: "wall zero",
			rusage: &store.Rusage{
				CPUMS:       6840000,
				CPUSomeUsec: &stallMorphic,
			},
			wall:    0,
			wantVal: 0,
			wantOK:  false,
		},
		{
			name: "wall negative",
			rusage: &store.Rusage{
				CPUMS:       6840000,
				CPUSomeUsec: &stallMorphic,
			},
			wall:    -time.Minute,
			wantVal: 0,
			wantOK:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotVal, gotOK := cpuWantMilli(tc.rusage, tc.wall)
			if gotOK != tc.wantOK {
				t.Fatalf("cpuWantMilli() ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotVal != tc.wantVal {
				t.Fatalf("cpuWantMilli() val = %d, want %d", gotVal, tc.wantVal)
			}
		})
	}
}

func TestCPUWantOf(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	stallRoundHigh := int64(1620000000)
	stallRoundLow := int64(72000000)
	stallGateHigh := int64(300000000)
	stallGateLow := int64(6000000)

	rusageRoundHigh := &store.Rusage{CPUMS: 6840000, CPUSomeUsec: &stallRoundHigh}
	rusageRoundLow := &store.Rusage{CPUMS: 360000, CPUSomeUsec: &stallRoundLow}
	gateHigh := &store.GateRecord{
		DurationMS: 600000,
		Rusage:     &store.Rusage{CPUMS: 1200000, CPUSomeUsec: &stallGateHigh},
	}
	gateLow := &store.GateRecord{
		DurationMS: 600000,
		Rusage:     &store.Rusage{CPUMS: 30000, CPUSomeUsec: &stallGateLow},
	}

	tests := []struct {
		name    string
		entry   store.LogEntry
		start   time.Time
		wantVal int64
		wantOK  bool
	}{
		{
			name: "round only",
			entry: store.LogEntry{
				TS:     end,
				Rusage: rusageRoundHigh,
			},
			start:   start,
			wantVal: 2350,
			wantOK:  true,
		},
		{
			name: "gate only (round has no rusage)",
			entry: store.LogEntry{
				TS:   end,
				Gate: gateHigh,
			},
			start:   start,
			wantVal: 2500,
			wantOK:  true,
		},
		{
			name: "both with the gate higher",
			entry: store.LogEntry{
				TS:     end,
				Rusage: rusageRoundLow,
				Gate:   gateHigh,
			},
			start:   start,
			wantVal: 2500,
			wantOK:  true,
		},
		{
			name: "both with the round higher",
			entry: store.LogEntry{
				TS:     end,
				Rusage: rusageRoundHigh,
				Gate:   gateLow,
			},
			start:   start,
			wantVal: 2350,
			wantOK:  true,
		},
		{
			name:    "neither",
			entry:   store.LogEntry{TS: end},
			start:   start,
			wantVal: 0,
			wantOK:  false,
		},
		{
			name: "zero start",
			entry: store.LogEntry{
				TS:     end,
				Rusage: rusageRoundHigh,
			},
			start:   time.Time{},
			wantVal: 0,
			wantOK:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotVal, gotOK := cpuWantOf(tc.entry, tc.start)
			if gotOK != tc.wantOK {
				t.Fatalf("cpuWantOf() ok = %v, want %v", gotOK, tc.wantOK)
			}
			if tc.wantOK {
				if gotVal == nil || *gotVal != tc.wantVal {
					t.Fatalf("cpuWantOf() val = %v, want %d", gotVal, tc.wantVal)
				}
			} else if gotVal != nil {
				t.Fatalf("cpuWantOf() val = %v, want nil", gotVal)
			}
		})
	}
}

func TestCostBackwardAndFold(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repoID := strings.Repeat("c", 64)
	role := "builder"
	path := costFilePath(root, repoID, role)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	rawJSON := `{
		"last_binding": "o/test",
		"last_round": 5,
		"samples": [
			{"base": 100, "burst": 1000, "binding": "o/test", "round": 1, "at": "2026-11-01T10:00:00Z"},
			{"base": 200, "burst": 2000, "binding": "o/test", "round": 2, "at": "2026-11-01T11:00:00Z"},
			{"base": 300, "burst": 3000, "binding": "o/test", "round": 3, "at": "2026-11-01T12:00:00Z"},
			{"base": 400, "burst": 4000, "binding": "o/test", "round": 4, "at": "2026-11-01T13:00:00Z"},
			{"base": 500, "burst": 5000, "binding": "o/test", "round": 5, "at": "2026-11-01T14:00:00Z"}
		]
	}`
	if err := os.WriteFile(path, []byte(rawJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cf, err := loadCostFile(path)
	if err != nil {
		t.Fatalf("loadCostFile: %v", err)
	}
	if len(cf.Samples) != 5 {
		t.Fatalf("len(cf.Samples) = %d, want 5", len(cf.Samples))
	}
	for i, s := range cf.Samples {
		if s.CPUWant != nil {
			t.Fatalf("sample %d CPUWant = %v, want nil", i, s.CPUWant)
		}
	}

	base, burst := lookupSeat(cf.Samples, false, false)
	if base != 500 || burst != 5000 {
		t.Fatalf("lookupSeat = (%d, %d), want (500, 5000)", base, burst)
	}
	b := store.Binding{
		Name:  "test",
		Owner: "o",
		Shape: store.ShapeWriter,
		Serve: &store.ServeFacts{RepoID: repoID},
	}
	burstCeil, ok := roundBurstFor(root)(b)
	if !ok || burstCeil != 5000+5000/2 {
		t.Fatalf("roundBurstFor = (%d, %v), want (7500, true)", burstCeil, ok)
	}

	wantCPU := int64(2350)
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	sampleWithCPU := costSample{
		Base:    1 << 30,
		Burst:   2 << 30,
		At:      at,
		CPUWant: &wantCPU,
	}
	if _, err := appendCostSample(root, repoID, role, "o/fold", 6, sampleWithCPU); err != nil {
		t.Fatalf("appendCostSample: %v", err)
	}

	wrote, err := foldCostSample(root, repoID, role, "o/fold", 6, &store.Rusage{
		BurstBytes: costInt64(3 << 30),
	})
	if err != nil {
		t.Fatalf("foldCostSample: %v", err)
	}
	if !wrote {
		t.Fatal("foldCostSample wrote = false, want true")
	}

	cfFolded, err := loadCostFile(path)
	if err != nil {
		t.Fatalf("loadCostFile after fold: %v", err)
	}
	var foldedSample costSample
	found := false
	for _, s := range cfFolded.Samples {
		if s.Binding == "o/fold" && s.Round == 6 {
			foldedSample = s
			found = true
			break
		}
	}
	if !found {
		t.Fatal("folded sample not found")
	}
	if foldedSample.Burst != 3<<30 {
		t.Fatalf("folded Burst = %d, want %d", foldedSample.Burst, 3<<30)
	}
	if foldedSample.CPUWant == nil || *foldedSample.CPUWant != wantCPU {
		t.Fatalf("folded CPUWant = %v, want %d", foldedSample.CPUWant, wantCPU)
	}
}
