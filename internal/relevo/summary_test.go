package relevo

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestWriteReaderSummaryPrefersTheChainBlockMessage pins the artifact fix: a
// chain reviewer that recaps after its block leaves a final message with no
// findings, and the saved artifact must be the message that carried the block,
// stripped of the block the parse already read.
func TestWriteReaderSummaryPrefersTheChainBlockMessage(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	b := chainBinding(t, rt, "shop-rev")
	b.Builder.Kind = "claude"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	block := "# Review\n\nThe plan is honoured and the tests cover it.\n\n```relevo\nverdict: pass\n```\n"
	recap := "Done."
	line := func(text string) string {
		raw, err := json.Marshal(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": text}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw) + "\n"
	}
	stream := rt.Store.RunnerStreamPath(b.Name, b.Round)
	if err := os.MkdirAll(filepath.Dir(stream), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stream, []byte(line(block)+line(recap)), 0o644); err != nil {
		t.Fatal(err)
	}

	// The runner already saved the recap as its artifact.
	out := reportPathFor(rt, b)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte(recap+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := writeReaderSummary(rt, b); err != nil {
		t.Fatalf("writeReaderSummary: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "The plan is honoured") {
		t.Errorf("artifact = %q, want the block-carrying message", got)
	}
	if strings.Contains(string(got), "```relevo") {
		t.Errorf("artifact still carries the block:\n%s", got)
	}
}

// plainReaderBlock is a plain reader's findings message: it carries a relevo
// block, so the output must be taken from it even when a recap follows.
const plainReaderBlock = "# Findings\n\nThe code is sound.\n\n```relevo\nstatus: done\nhalted_at: \"\"\nchanged_paths: []\ncommands_run: []\nnot_done: []\n```\n"

// writeReaderMessages writes a claude stream whose assistant texts are texts,
// in order, so a test can put a recap after a block-carrying message.
func writeReaderMessages(t *testing.T, rt Runtime, name string, round int, texts ...string) {
	t.Helper()
	var stream strings.Builder
	for _, text := range texts {
		raw, err := json.Marshal(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": text}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		stream.Write(raw)
		stream.WriteByte('\n')
	}
	path := rt.Store.RunnerStreamPath(name, round)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(stream.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWriteReaderSummarySavesAPlainReadersBlockMessageOverARecap pins the plain
// reader half of the selection: a reader that is not a chain member and recaps
// after its block-carrying message still gets that message saved, with its
// block kept for the close to parse and strip.
func TestWriteReaderSummarySavesAPlainReadersBlockMessageOverARecap(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	b := testReaderBinding()
	writeReaderMessages(t, rt, b.Name, b.Round, plainReaderBlock, "Done.")

	if _, err := writeReaderSummary(rt, b); err != nil {
		t.Fatalf("writeReaderSummary: %v", err)
	}
	got, err := os.ReadFile(reportPathFor(rt, b))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "The code is sound") {
		t.Errorf("output = %q, want the block-carrying message", got)
	}
	if strings.Contains(string(got), "Done.") {
		t.Errorf("output = %q, want the recap not saved", got)
	}
	if !strings.Contains(string(got), "```relevo") {
		t.Errorf("output lost the relevo block the close must parse:\n%s", got)
	}
}

// TestWriteReaderSummarySavesTheFinalTextWithoutABlock pins the plain reader
// fallback: two block-free messages leave the last one saved, as before.
func TestWriteReaderSummarySavesTheFinalTextWithoutABlock(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	b := testReaderBinding()
	writeReaderMessages(t, rt, b.Name, b.Round, "First message.", "The final message.")

	if _, err := writeReaderSummary(rt, b); err != nil {
		t.Fatalf("writeReaderSummary: %v", err)
	}
	got, err := os.ReadFile(reportPathFor(rt, b))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "The final message.\n" {
		t.Errorf("output = %q, want the last block-free message", got)
	}
}

// TestWriteReaderSummaryLeavesAPlainReadersOwnFileAlone pins that a regular
// file the runner wrote at a plain reader's output path is left byte-identical.
func TestWriteReaderSummaryLeavesAPlainReadersOwnFileAlone(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	b := testReaderBinding()
	writeReaderMessages(t, rt, b.Name, b.Round, plainReaderBlock, "Done.")

	out := reportPathFor(rt, b)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	const own = "# The runner's own findings\n"
	if err := os.WriteFile(out, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := writeReaderSummary(rt, b); err != nil {
		t.Fatalf("writeReaderSummary: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != own {
		t.Errorf("output = %q, want the runner's own file unchanged %q", got, own)
	}
}

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
	if err := os.MkdirAll(rt.Store.OutDir(b.Name), 0o755); err != nil {
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
