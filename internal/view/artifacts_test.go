package view

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// liveArtifacts is a store holding a live builder binding whose round-1
// artifact directory has the given files, each written with its body.
func liveArtifacts(t *testing.T, files map[string]string) (*store.Store, store.Binding) {
	t.Helper()
	st := store.New(t.TempDir())
	b := store.Binding{Name: "api"}
	b.CWD = st.Dir(b.Name)
	if err := st.Save(b); err != nil {
		t.Fatalf("save: %v", err)
	}
	dir := st.ArtifactDir(b.Name, 1, "builder")
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return st, b
}

func TestRoundArtifactsListsTheActorsRoundSortedOutputFirst(t *testing.T) {
	t.Parallel()

	st, b := liveArtifacts(t, map[string]string{
		"summary.md":      "s",
		"site/index.html": "page",
		"findings.md":     "f",
	})
	got, err := RoundArtifacts(st, b.Name, 1, "builder", "findings.md")
	if err != nil {
		t.Fatalf("RoundArtifacts: %v", err)
	}
	var rels []string
	for _, f := range got {
		rels = append(rels, f.Rel)
	}
	if want := []string{"findings.md", "summary.md", "site/index.html"}; !slices.Equal(rels, want) {
		t.Errorf("rels = %q, want %q", rels, want)
	}
	if got[2].Size != int64(len("page")) {
		t.Errorf("site/index.html size = %d, want %d", got[2].Size, len("page"))
	}

	other, err := RoundArtifacts(st, b.Name, 2, "builder", "")
	if err != nil || len(other) != 0 {
		t.Errorf("round 2 = %v, %v; want no files", other, err)
	}
}

func TestArtifactRelsOfAnUnknownBindingIsNotFound(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	_, err := ArtifactRels(st, "nobody", 1, "builder")
	if !errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), `binding "nobody" not found (live or archived)`) {
		t.Errorf("err = %v, want the live-or-archived not-found refusal", err)
	}
}

func TestArtifactCapComparesTheRoundsTotalSize(t *testing.T) {
	t.Parallel()

	st, b := liveArtifacts(t, map[string]string{"a.bin": "12345", "b/c.bin": "678"})
	total, err := ArtifactDirSize(st, b, 1)
	if err != nil || total != 8 {
		t.Fatalf("ArtifactDirSize = %d, %v; want 8", total, err)
	}
	for _, tc := range []struct {
		max  int64
		want bool
	}{{7, true}, {8, false}, {100, false}} {
		over, got := ArtifactCapExceeded(st, b, 1, tc.max)
		if over != tc.want || got != 8 {
			t.Errorf("ArtifactCapExceeded(max %d) = %v, %d; want %v, 8", tc.max, over, got, tc.want)
		}
	}

	over, got := ArtifactCapExceeded(st, store.Binding{Name: "gone"}, 1, 0)
	if over || got != 0 {
		t.Errorf("unreadable size = %v, %d; want under the cap with 0", over, got)
	}
}

func TestSortArtifactsRanksOutputThenSummaryThenName(t *testing.T) {
	t.Parallel()

	files := []ArtifactFile{{Rel: "z.md"}, {Rel: "summary.md"}, {Rel: "a.md"}, {Rel: "out.md"}}
	SortArtifacts(files, "out.md")
	var rels []string
	for _, f := range files {
		rels = append(rels, f.Rel)
	}
	if want := []string{"out.md", "summary.md", "a.md", "z.md"}; !slices.Equal(rels, want) {
		t.Errorf("with output = %q, want %q", rels, want)
	}

	SortArtifacts(files, "")
	if files[0].Rel != "summary.md" {
		t.Errorf("without output, first = %q, want summary.md", files[0].Rel)
	}
}

func TestArtifactSizeAndCapWording(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		n    int64
		want string
	}{{999, "999"}, {1000, "1k"}, {1500, "1.5k"}, {999_999, "1000k"}, {1_000_000, "1M"}, {2_500_000, "2.5M"}} {
		if got := ArtifactSizeText(tc.n); got != tc.want {
			t.Errorf("ArtifactSizeText(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
	if got := FormatMB(2621440); got != "2.5" {
		t.Errorf("FormatMB(2.5 MiB) = %q, want 2.5", got)
	}
	if got := FormatMB(2097152); got != "2" {
		t.Errorf("FormatMB(2 MiB) = %q, want 2", got)
	}
	want := "artifacts over the cap: 2.5 > 2 MB; raise policy.artifact_max_mb to seal them"
	if got := ArtifactCapReason(2621440, 2<<20); got != want {
		t.Errorf("ArtifactCapReason = %q, want %q", got, want)
	}
	mtime := time.Date(2026, 10, 31, 9, 5, 0, 0, time.UTC)
	if got := ArtifactLine(ArtifactFile{Rel: "summary.md", Size: 1500, MTime: mtime}); got != "  1.5k  09:05  summary.md" {
		t.Errorf("ArtifactLine = %q", got)
	}
}
