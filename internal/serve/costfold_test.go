package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestFoldCostSampleUpdatesInPlace pins that a settled check folds into its
// own round's sample: the burst rises to the check's peak while the base
// stays the round's, other samples are untouched, and the file holds no
// second sample for the round. A lower check peak, or a check with no known
// burst, folds nothing and writes nothing.
func TestFoldCostSampleUpdatesInPlace(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo := strings.Repeat("a", 64)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if _, err := appendCostSample(root, repo, "builder", "o/m", 2, costSample{Base: 1 << 30, Burst: 2 << 30, At: at}); err != nil {
		t.Fatalf("append m: %v", err)
	}
	other := costSample{Base: 1 << 29, Burst: 1 << 30, At: at}
	if _, err := appendCostSample(root, repo, "builder", "o/other", 1, other); err != nil {
		t.Fatalf("append other: %v", err)
	}
	other.Binding, other.Round = "o/other", 1

	// The check's own base is larger than the round's: the fold still leaves
	// the round's base alone and only raises the burst.
	wrote, err := foldCostSample(root, repo, "builder", "o/m", 2, &store.Rusage{
		BaseBytes: costInt64(4 << 30), BurstBytes: costInt64(3 << 30),
	})
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if !wrote {
		t.Fatal("fold = false, want true for a higher check peak")
	}
	cf, err := loadCostFile(costFilePath(root, repo, "builder"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cf.Samples) != 2 {
		t.Fatalf("samples = %d, want 2: the fold updates in place, never appends", len(cf.Samples))
	}
	var m costSample
	for _, s := range cf.Samples {
		if s.Binding == "o/m" {
			m = s
		} else if s != other {
			t.Fatalf("other sample = %+v, want %+v untouched", s, other)
		}
	}
	if m.Base != 1<<30 || m.Burst != 3<<30 {
		t.Fatalf("folded sample = %+v, want {base 1GiB burst 3GiB}", m)
	}

	before, err := os.ReadFile(costFilePath(root, repo, "builder"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for name, r := range map[string]*store.Rusage{
		"a lower check peak": {BurstBytes: costInt64(1 << 30)},
		"a burstless check":  {BaseBytes: costInt64(9 << 30)},
	} {
		wrote, err := foldCostSample(root, repo, "builder", "o/m", 2, r)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if wrote {
			t.Fatalf("%s: fold = true, want false", name)
		}
	}
	after, err := os.ReadFile(costFilePath(root, repo, "builder"))
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("a no-op fold rewrote the file, want it untouched")
	}
}

// TestFoldCostSampleLeavesMissingAlone pins that a fold with no sample to
// land on writes nothing: an unknown key, a key the trim dropped, and a
// sample written before the key fields existed.
func TestFoldCostSampleLeavesMissingAlone(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo := strings.Repeat("b", 64)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if _, err := appendCostSample(root, repo, "builder", "o/m", 1, costSample{Base: 1 << 30, Burst: 2 << 30, At: at}); err != nil {
		t.Fatalf("append: %v", err)
	}
	path := costFilePath(root, repo, "builder")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	wrote, err := foldCostSample(root, repo, "builder", "o/ghost", 1, &store.Rusage{BurstBytes: costInt64(9 << 30)})
	if err != nil {
		t.Fatalf("fold unknown: %v", err)
	}
	if wrote {
		t.Fatal("fold unknown key = true, want false")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("folding an unknown key rewrote the file, want it untouched")
	}

	trimmed := strings.Repeat("c", 64)
	for round := 1; round <= 21; round++ {
		if _, err := appendCostSample(root, trimmed, "builder", "o/m", round, costSample{Base: 1, Burst: 1}); err != nil {
			t.Fatalf("append %d: %v", round, err)
		}
	}
	wrote, err = foldCostSample(root, trimmed, "builder", "o/m", 1, &store.Rusage{BurstBytes: costInt64(9 << 30)})
	if err != nil {
		t.Fatalf("fold trimmed: %v", err)
	}
	if wrote {
		t.Fatal("fold trimmed key = true, want false")
	}
	cf, err := loadCostFile(costFilePath(root, trimmed, "builder"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cf.Samples) != 20 {
		t.Fatalf("samples = %d, want the trimmed 20", len(cf.Samples))
	}

	legacy := strings.Repeat("d", 64)
	legacyPath := costFilePath(root, legacy, "builder")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"last_binding":"o/m","last_round":1,"samples":[{"base":1,"burst":2}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err = os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy: %v", err)
	}
	wrote, err = foldCostSample(root, legacy, "builder", "o/m", 1, &store.Rusage{BurstBytes: costInt64(9 << 30)})
	if err != nil {
		t.Fatalf("fold legacy: %v", err)
	}
	if wrote {
		t.Fatal("fold keyless sample = true, want false")
	}
	if after, _ := os.ReadFile(legacyPath); string(after) != string(before) {
		t.Fatal("folding a keyless sample rewrote the file, want it untouched")
	}
}

// TestFoldCostSampleFindsSampleAfterLastBindingMovedOn pins that the fold
// keys on the sample's own binding and round, not on LastBinding: a check
// settling after another binding closed still lands on its round.
func TestFoldCostSampleFindsSampleAfterLastBindingMovedOn(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo := strings.Repeat("e", 64)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if _, err := appendCostSample(root, repo, "builder", "o/a", 1, costSample{Base: 1 << 30, Burst: 2 << 30, At: at}); err != nil {
		t.Fatalf("append a: %v", err)
	}
	if _, err := appendCostSample(root, repo, "builder", "o/b", 1, costSample{Base: 1 << 30, Burst: 1 << 30, At: at}); err != nil {
		t.Fatalf("append b: %v", err)
	}
	wrote, err := foldCostSample(root, repo, "builder", "o/a", 1, &store.Rusage{BurstBytes: costInt64(3 << 30)})
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if !wrote {
		t.Fatal("fold = false, want true after LastBinding moved on")
	}
	cf, err := loadCostFile(costFilePath(root, repo, "builder"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, s := range cf.Samples {
		if s.Binding == "o/a" && s.Burst != 3<<30 {
			t.Fatalf("a sample burst = %d, want the folded 3GiB", s.Burst)
		}
		if s.Binding == "o/b" && s.Burst != 1<<30 {
			t.Fatalf("b sample burst = %d, want the untouched 1GiB", s.Burst)
		}
	}
}

// TestFoldCheckCostGuards pins the fold hook's guards: a binding with no
// serve facts records nothing, and a corrupt file warns rather than failing
// the settle.
func TestFoldCheckCostGuards(t *testing.T) {
	t.Parallel()

	t.Run("no serve facts records nothing", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		foldCheckCost(root, store.Binding{Name: "local"}, 1, &store.Rusage{BurstBytes: costInt64(9 << 30)})
		if entries, err := os.ReadDir(filepath.Join(root, "costs")); err == nil {
			t.Fatalf("costs dir holds %d entries, want none", len(entries))
		} else if !os.IsNotExist(err) {
			t.Fatalf("read costs dir: %v", err)
		}
	})

	t.Run("happy path raises the burst", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		repo := strings.Repeat("f", 64)
		at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
		b := store.Binding{
			Name: "api", Owner: "o", CWD: "/build/shop",
			Serve: &store.ServeFacts{RepoID: repo},
		}
		if _, err := appendCostSample(root, repo, "builder", "o/api", 1, costSample{Base: 1 << 30, Burst: 2 << 30, At: at}); err != nil {
			t.Fatalf("append: %v", err)
		}
		foldCheckCost(root, b, 1, &store.Rusage{BurstBytes: costInt64(3 << 30)})
		cf, err := loadCostFile(costFilePath(root, repo, "builder"))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if len(cf.Samples) != 1 || cf.Samples[0].Burst != 3<<30 || cf.Samples[0].Base != 1<<30 {
			t.Fatalf("samples = %+v, want one {base 1GiB burst 3GiB}", cf.Samples)
		}
	})

	t.Run("corrupt file warns without failing", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		repo := strings.Repeat("0", 64)
		path := costFilePath(root, repo, "builder")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		b := store.Binding{
			Name: "api", Owner: "o", CWD: "/build/shop",
			Serve: &store.ServeFacts{RepoID: repo},
		}
		foldCheckCost(root, b, 1, &store.Rusage{BurstBytes: costInt64(3 << 30)})
	})
}
