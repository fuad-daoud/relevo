package serve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

func costInt64(v int64) *int64 { return &v }

func TestAppendCostSampleDedupesSameRound(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo := strings.Repeat("a", 64)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	s := costSample{Base: 1 << 30, Burst: 2 << 30, At: at}

	ok, err := appendCostSample(root, repo, "builder", "b1", 3, s)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if !ok {
		t.Fatal("first append = false, want true")
	}

	ok, err = appendCostSample(root, repo, "builder", "b1", 3, s)
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

func TestAppendCostSampleKeepsOtherBindingsSameRound(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo := strings.Repeat("c", 64)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"x", "y", "z"} {
		ok, err := appendCostSample(root, repo, "builder", name, 1, costSample{Base: 1, Burst: 2, At: at})
		if err != nil {
			t.Fatalf("append %s: %v", name, err)
		}
		if !ok {
			t.Fatalf("append %s round 1 = false, want true: another binding's round 1 is a new round", name)
		}
	}
	cf, err := loadCostFile(costFilePath(root, repo, "builder"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cf.Samples) != 3 || cf.LastBinding != "z" {
		t.Fatalf("cost file = %d samples, last %q; want 3 and z", len(cf.Samples), cf.LastBinding)
	}
}

func TestAppendCostSampleConcurrentKeepsEverySample(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo := strings.Repeat("d", 64)
	const n = 12
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if _, err := appendCostSample(root, repo, "builder", fmt.Sprintf("b%d", i), 1, costSample{Base: 1, Burst: 2}); err != nil {
				t.Errorf("append b%d: %v", i, err)
			}
		})
	}
	wg.Wait()
	cf, err := loadCostFile(costFilePath(root, repo, "builder"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cf.Samples) != n {
		t.Fatalf("samples = %d, want %d: a concurrent append lost a sample", len(cf.Samples), n)
	}
}

func TestAppendCostSampleTrimsTo20(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo := strings.Repeat("b", 64)
	for round := 1; round <= 21; round++ {
		s := costSample{Base: int64(round), Burst: int64(round * 100)}
		ok, err := appendCostSample(root, repo, "builder", "b1", round, s)
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

// TestRecordClosedCostGuards pins the hook's guards: without a cost root or
// a store the call records nothing, and without a matching report entry a
// store-backed runtime still writes nothing.
func TestRecordClosedCostGuards(t *testing.T) {
	t.Parallel()

	t.Run("empty root records nothing", func(t *testing.T) {
		t.Parallel()

		env := setupTestEnv(t)
		repoID := strings.Repeat("e", 64)
		at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
		burst, base := int64(2<<30), int64(1<<30)

		rt := env.runtime(t)
		b := store.Binding{
			Name:  "api",
			Owner: string(env.id),
			CWD:   "/build/shop",
			Round: 2,
			Serve: &store.ServeFacts{BareRepo: "/fake/bare", RepoID: repoID},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("save: %v", err)
		}
		if err := rt.Store.AppendLog("api", store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
			TS: at, Rusage: &store.Rusage{BaseBytes: &base, BurstBytes: &burst},
		}); err != nil {
			t.Fatalf("append log: %v", err)
		}

		recordClosedCost(relevo.Runtime{Store: rt.Store}, b, 1)
		if _, err := os.Stat(costFilePath("", repoID, "builder")); !os.IsNotExist(err) {
			t.Fatalf("cost file exists with empty root: err=%v", err)
		}
	})

	t.Run("nil store returns without panicking", func(t *testing.T) {
		t.Parallel()

		costRoot := t.TempDir()
		repoID := strings.Repeat("f", 64)
		b := store.Binding{
			Name:  "api",
			CWD:   "/build/shop",
			Round: 2,
			Serve: &store.ServeFacts{BareRepo: "/fake/bare", RepoID: repoID},
		}
		recordClosedCost(relevo.Runtime{CostRoot: costRoot}, b, 1)
		if _, err := os.Stat(costFilePath(costRoot, repoID, "builder")); !os.IsNotExist(err) {
			t.Fatalf("cost file exists with nil store: err=%v", err)
		}
	})

	t.Run("store miss records nothing", func(t *testing.T) {
		t.Parallel()

		env := setupTestEnv(t)
		repoID := strings.Repeat("g", 64)

		rt := env.runtime(t)
		b := store.Binding{
			Name:  "api",
			Owner: string(env.id),
			CWD:   "/build/shop",
			Round: 2,
			Serve: &store.ServeFacts{BareRepo: "/fake/bare", RepoID: repoID},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("save: %v", err)
		}

		costRoot := t.TempDir()
		recordClosedCost(relevo.Runtime{Store: rt.Store, CostRoot: costRoot}, b, 1)
		if _, err := os.Stat(costFilePath(costRoot, repoID, "builder")); !os.IsNotExist(err) {
			t.Fatalf("cost file exists with no matching entry: err=%v", err)
		}
	})
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

func TestSeatCostFor(t *testing.T) {
	t.Parallel()

	set, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	reg := plannerAdmitRegistry(t, set)
	repo := strings.Repeat("f", 64)
	root := t.TempDir()
	for round := 1; round <= 10; round++ {
		s := costSample{Base: int64(round) << 20, Burst: int64(round) << 30}
		if _, err := appendCostSample(root, repo, "builder", "o/b", round, s); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	corrupt := strings.Repeat("9", 64)
	if err := os.WriteFile(costFilePath(root, corrupt, "builder"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	bind := func(role, repoID string) store.Binding {
		return store.Binding{Name: "x", Role: role, Serve: &store.ServeFacts{RepoID: repoID}}
	}
	cases := []struct {
		name        string
		root        string
		b           store.Binding
		base, burst int64
	}{
		{"history p90", root, bind("", repo), 9 << 20, 9 << 30},
		{"no history: builder defaults", root, bind("", strings.Repeat("0", 64)), seatBuilderBase, seatBuilderBurst},
		{"corrupt history: defaults, not zero", root, bind("", corrupt), seatBuilderBase, seatBuilderBurst},
		{"no root: defaults", "", bind("", repo), seatBuilderBase, seatBuilderBurst},
		{"no serve facts: defaults", root, store.Binding{Name: "x"}, seatBuilderBase, seatBuilderBurst},
		{"planner", root, bind("lite-planner", repo), seatPlannerBase, seatPlannerBurst},
		{"reviewer", root, bind("reviewer", repo), seatReviewerBase, seatReviewerBurst},
		{"unknown role: builder defaults", root, bind("nosuch", repo), seatBuilderBase, seatBuilderBurst},
	}
	for _, tc := range cases {
		base, burst := seatCostFor(tc.root, reg, tc.b)
		if base != tc.base || burst != tc.burst {
			t.Errorf("%s: got %d/%d, want %d/%d", tc.name, base, burst, tc.base, tc.burst)
		}
	}
}
