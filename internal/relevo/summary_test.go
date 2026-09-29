package relevo

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// testReaderBinding is a reader binding whose fields writeReaderSummary reads:
// the shape, the role that names the artifact directory, and the harness kind
// that words the output label.
func testReaderBinding() store.Binding {
	return store.Binding{
		Name:    "reader-bind",
		Role:    "reviewer",
		Shape:   store.ShapeReader,
		Round:   1,
		Builder: store.Endpoint{Kind: "claude"},
	}
}

// TestWriteReaderSummaryRefusesADanglingSymlink pins that a symlink a runner
// plants at the output path is refused and its target is never created: the
// daemon must not follow a runner-planted link out of the state directory.
func TestWriteReaderSummaryRefusesADanglingSymlink(t *testing.T) {
	rt := newRuntime(t)
	b := testReaderBinding()
	writeReaderStream(t, rt, b.Name, b.Round, "final message")

	out := reportPathFor(rt, b)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "pwned")
	if err := os.Symlink(target, out); err != nil {
		t.Fatal(err)
	}

	_, err := writeReaderSummary(rt, b)
	if err == nil {
		t.Fatalf("writeReaderSummary = nil error, want the dangling symlink refused")
	}
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat(%q) = %v, want the symlink target never created", target, err)
	}
}

// TestWriteReaderSummaryRefusesASymlinkedArtifactDir pins that a symlinked
// NNN-<actor> directory is refused and MkdirAll never descends it, so nothing
// is written outside the state directory.
func TestWriteReaderSummaryRefusesASymlinkedArtifactDir(t *testing.T) {
	rt := newRuntime(t)
	b := testReaderBinding()
	writeReaderStream(t, rt, b.Name, b.Round, "final message")

	out := reportPathFor(rt, b)
	target := t.TempDir()
	if err := os.MkdirAll(rt.Store.Dir(b.Name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Dir(out)); err != nil {
		t.Fatal(err)
	}

	_, err := writeReaderSummary(rt, b)
	if err == nil {
		t.Fatalf("writeReaderSummary = nil error, want the symlinked artifact directory refused")
	}
	entries, rerr := os.ReadDir(target)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 0 {
		t.Errorf("symlinked artifact directory holds %d entries, want none: %v", len(entries), entries)
	}
}
