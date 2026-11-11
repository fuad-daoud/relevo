package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

func costInt64(v int64) *int64 { return &v }

func TestAppendCostSampleDedupesSameRound(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo := strings.Repeat("a", 64)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	s := costSample{Base: 1 << 30, Burst: 2 << 30, At: at}

	ok, err := appendCostSample(root, repo, "builder", 3, s)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if !ok {
		t.Fatal("first append = false, want true")
	}

	ok, err = appendCostSample(root, repo, "builder", 3, s)
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if ok {
		t.Fatal("second append = true, want false for the same round")
	}

	cf, err := loadCostFile(costFilePath(root, repo, "builder"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cf.LastRound != 3 {
		t.Fatalf("LastRound = %d, want 3", cf.LastRound)
	}
	if len(cf.Samples) != 1 {
		t.Fatalf("samples = %d, want 1", len(cf.Samples))
	}
	if cf.Samples[0] != s {
		t.Fatalf("sample = %+v, want %+v", cf.Samples[0], s)
	}
}

func TestAppendCostSampleTrimsTo20(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo := strings.Repeat("b", 64)
	for round := 1; round <= 21; round++ {
		s := costSample{Base: int64(round), Burst: int64(round * 100)}
		ok, err := appendCostSample(root, repo, "builder", round, s)
		if err != nil {
			t.Fatalf("append round %d: %v", round, err)
		}
		if !ok {
			t.Fatalf("append round %d = false, want true", round)
		}
	}

	cf, err := loadCostFile(costFilePath(root, repo, "builder"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cf.LastRound != 21 {
		t.Fatalf("LastRound = %d, want 21", cf.LastRound)
	}
	if len(cf.Samples) != 20 {
		t.Fatalf("samples = %d, want 20", len(cf.Samples))
	}
	for i, s := range cf.Samples {
		want := int64(i + 2)
		if s.Base != want || s.Burst != want*100 {
			t.Fatalf("samples[%d] = %+v, want base %d burst %d", i, s, want, want*100)
		}
	}
}

func TestCostFilePathSanitisesRole(t *testing.T) {
	t.Parallel()

	got := costFilePath("/root", strings.Repeat("c", 64), "Builder_X Y")
	want := filepath.Join("/root", "costs", strings.Repeat("c", 64)+"---uilder----.json")
	if got != want {
		t.Fatalf("costFilePath = %q, want %q", got, want)
	}
}

func TestLoadCostFileMissingAndCorrupt(t *testing.T) {
	t.Parallel()

	cf, err := loadCostFile(filepath.Join(t.TempDir(), "costs", "nope.json"))
	if err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if cf.LastRound != 0 || len(cf.Samples) != 0 {
		t.Fatalf("missing file = %+v, want empty", cf)
	}

	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCostFile(path); err == nil {
		t.Fatal("corrupt file: no error, want one")
	}
}

func TestSampleForEntry(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		entry     store.LogEntry
		want      costSample
		wantKnown bool
	}{
		{
			name:      "no rusage records nothing",
			entry:     store.LogEntry{Round: 1, TS: at},
			wantKnown: false,
		},
		{
			name: "burst and base come from the pointers",
			entry: store.LogEntry{Round: 1, TS: at, Rusage: &store.Rusage{
				BaseBytes: costInt64(1 << 30), BurstBytes: costInt64(2 << 30),
			}},
			want:      costSample{Base: 1 << 30, Burst: 2 << 30, At: at},
			wantKnown: true,
		},
		{
			name: "unknown base records zero",
			entry: store.LogEntry{Round: 1, TS: at, Rusage: &store.Rusage{
				BurstBytes: costInt64(2 << 30),
			}},
			want:      costSample{Base: 0, Burst: 2 << 30, At: at},
			wantKnown: true,
		},
		{
			name: "oom peak stands in for a missing burst",
			entry: store.LogEntry{Round: 1, TS: at, Rusage: &store.Rusage{
				PeakMemBytes: 3 << 30, EvOOMKill: costInt64(1),
			}},
			want:      costSample{Base: 0, Burst: 3 << 30, At: at},
			wantKnown: true,
		},
		{
			name: "a present burst wins over the oom peak",
			entry: store.LogEntry{Round: 1, TS: at, Rusage: &store.Rusage{
				BurstBytes: costInt64(1 << 30), PeakMemBytes: 3 << 30, EvOOMKill: costInt64(2),
			}},
			want:      costSample{Base: 0, Burst: 1 << 30, At: at},
			wantKnown: true,
		},
		{
			name: "a zero oom counter is not a kill",
			entry: store.LogEntry{Round: 1, TS: at, Rusage: &store.Rusage{
				PeakMemBytes: 3 << 30, EvOOMKill: costInt64(0),
			}},
			wantKnown: false,
		},
		{
			name: "a peak without a kill records nothing",
			entry: store.LogEntry{Round: 1, TS: at, Rusage: &store.Rusage{
				PeakMemBytes: 3 << 30,
			}},
			wantKnown: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sampleForEntry(tc.entry)
			if ok != tc.wantKnown {
				t.Fatalf("sampleForEntry() known = %v, want %v", ok, tc.wantKnown)
			}
			if ok && got != tc.want {
				t.Fatalf("sampleForEntry() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReportEntryForLastMatchWins(t *testing.T) {
	t.Parallel()

	entries := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "first"},
		{Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "other"},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "last"},
	}
	got, ok := reportEntryFor(entries, 1)
	if !ok {
		t.Fatal("reportEntryFor(1): no match, want the last round-1 report")
	}
	if got.Payload != "last" {
		t.Fatalf("reportEntryFor(1) payload = %q, want %q", got.Payload, "last")
	}
	if _, ok := reportEntryFor(entries, 3); ok {
		t.Fatal("reportEntryFor(3): match, want none")
	}
}
