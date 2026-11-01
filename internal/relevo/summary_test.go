package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// chainReviewerRound starts a chain, closes its builder and returns the
// reviewer binding with a claude stream whose assistant texts are texts, and
// the path of its output file with the given content (none when file is nil).
func chainReviewerRound(t *testing.T, file *string, texts ...string) (Runtime, store.Binding, string) {
	t.Helper()
	rt, _ := chainRuntime(t)
	startedChain(t, rt, ChainOptions{})
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	b := chainBinding(t, rt, "shop-rev")
	b.Builder.Kind = "claude"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
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
	path := rt.Store.RunnerStreamPath(b.Name, b.Round)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(stream.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out := reportPathFor(rt, b)
	if file != nil {
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, []byte(*file), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return rt, b, out
}

// TestWriteReaderSummaryPrefersTheChainBlockMessageOverARecap pins the recap
// protection: a chain reviewer that recaps after its block and left an empty
// output file gets the block-carrying message saved, stripped of its block.
func TestWriteReaderSummaryPrefersTheChainBlockMessageOverARecap(t *testing.T) {
	t.Parallel()

	block := "# Review\n\nThe plan is honoured and the tests cover it.\n\n```relevo\nverdict: pass\n```\n"
	empty := "\n"
	rt, b, out := chainReviewerRound(t, &empty, block, "Done.")

	_, fellBack, err := writeReaderSummary(rt, b)
	if err != nil {
		t.Fatalf("writeReaderSummary: %v", err)
	}
	if !fellBack {
		t.Errorf("fellBack = false, want true for an empty file")
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

// TestWriteReaderSummaryKeepsAChainReviewersFindingsFile pins that findings the
// reviewer wrote to its file survive a pointer-plus-block final message, and
// that the verdict is read from that message.
func TestWriteReaderSummaryKeepsAChainReviewersFindingsFile(t *testing.T) {
	t.Parallel()

	findings := "# Findings\n\nTwo real problems in the diff.\n"
	rt, b, out := chainReviewerRound(t, &findings, "Findings are in the output file.\n\n```relevo\nverdict: changes\n```\n")

	_, fellBack, err := writeReaderSummary(rt, b)
	if err != nil {
		t.Fatalf("writeReaderSummary: %v", err)
	}
	if fellBack {
		t.Errorf("fellBack = true for a kept file, want false")
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != findings {
		t.Errorf("output = %q, want the findings kept byte-identical %q", got, findings)
	}
	var verdict string
	for _, body := range chainOutcomeBodies(rt, b, b.Round, got) {
		if v, ok := reporttail.BlockValue(body, "verdict"); ok {
			verdict = v
			break
		}
	}
	if verdict != "changes" {
		t.Errorf("verdict = %q, want changes from the final message", verdict)
	}
}

// TestWriteReaderSummaryStripsTheBlockFromAKeptChainFile pins that a block
// inside a kept findings file is removed when the final message carries its own.
func TestWriteReaderSummaryStripsTheBlockFromAKeptChainFile(t *testing.T) {
	t.Parallel()

	file := "# Findings\n\nOne problem.\n\n```relevo\nverdict: changes\n```\n"
	rt, b, out := chainReviewerRound(t, &file, "See the file.\n\n```relevo\nverdict: changes\n```\n")

	if _, _, err := writeReaderSummary(rt, b); err != nil {
		t.Fatalf("writeReaderSummary: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# Findings\n\nOne problem.\n" {
		t.Errorf("output = %q, want the findings without the block", got)
	}
}

// TestWriteReaderSummaryKeepsAChainFileBlockWhenTheMessageHasNone pins that a
// file carrying the only block is left byte-identical, so its verdict survives.
func TestWriteReaderSummaryKeepsAChainFileBlockWhenTheMessageHasNone(t *testing.T) {
	t.Parallel()

	file := "# Findings\n\nOne problem.\n\n```relevo\nverdict: changes\n```\n"
	rt, b, out := chainReviewerRound(t, &file, "Review written to the file.")

	if _, _, err := writeReaderSummary(rt, b); err != nil {
		t.Fatalf("writeReaderSummary: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != file {
		t.Errorf("output = %q, want the file byte-identical %q", got, file)
	}
	if v, ok := reporttail.BlockValue(got, "verdict"); !ok || v != "changes" {
		t.Errorf("verdict = %q, %v, want changes from the file", v, ok)
	}
}

// plainReaderBlock is a plain reader's findings message: it carries a relevo
// block, so the output must be taken from it even when a recap follows.
const plainReaderBlock = "# Findings\n\nThe code is sound.\n\n```relevo\nstatus: done\nhalted_at: \"\"\nchanged_paths: []\ncommands_run: []\nnot_done: []\n```\n"

// writeReaderMessages writes a claude stream whose assistant texts are texts,
// in order, so a test can put a recap after a block-carrying message. The last
// result event a one-shot claude run always streams carries the last text, so
// everything that reads the stream reads the message it always read.
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
	end, err := json.Marshal(map[string]any{"type": "result", "result": texts[len(texts)-1]})
	if err != nil {
		t.Fatal(err)
	}
	stream.Write(end)
	stream.WriteByte('\n')
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

	if _, _, err := writeReaderSummary(rt, b); err != nil {
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

	if _, _, err := writeReaderSummary(rt, b); err != nil {
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
// file the runner wrote at a plain reader's output path is left byte-identical
// and reported as no fallback. The file's content differs from the stream's
// last block-carrying message on purpose: a close that re-saved the stream over
// a file the runner had already written passes every assertion here that leaves
// the two texts the same.
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
	if strings.Contains(own, "The code is sound") {
		t.Fatalf("the runner's own text must differ from the stream text, got %q", own)
	}
	if err := os.WriteFile(out, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}

	_, fellBack, err := writeReaderSummary(rt, b)
	if err != nil {
		t.Fatalf("writeReaderSummary: %v", err)
	}
	if fellBack {
		t.Errorf("fellBack = true for a file the runner wrote, want false")
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != own {
		t.Errorf("output = %q, want the runner's own file unchanged %q", got, own)
	}
}

// TestWriteReaderSummaryFallsBackFromAnEmptyOutputFile pins that an output file
// with nothing in it is not an artifact: the close saves the stream's
// block-carrying message over it and says the fallback fired. A file that is
// zero bytes and one that is only whitespace are the same case, and a close
// that treated either as delivered leaves the round with a blank artifact and
// no record that it happened.
func TestWriteReaderSummaryFallsBackFromAnEmptyOutputFile(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		content string
	}{
		{"zero bytes", ""},
		{"whitespace only", "   \n\t\n  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rt := newRuntime(t)
			b := testReaderBinding()
			writeReaderMessages(t, rt, b.Name, b.Round, plainReaderBlock, "Done.")

			out := reportPathFor(rt, b)
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(out, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}

			path, fellBack, err := writeReaderSummary(rt, b)
			if err != nil {
				t.Fatalf("writeReaderSummary: %v", err)
			}
			if path != out {
				t.Errorf("path = %q, want %q", path, out)
			}
			if !fellBack {
				t.Fatalf("fellBack = false for an empty output file, want true")
			}
			got, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), "The code is sound") {
				t.Errorf("output = %q, want the stream's block-carrying message saved over it", got)
			}
			if present, empty := view.ReaderOutputPresent(out); !present || empty {
				t.Errorf("readerOutputPresent = (%v, %v), want a present non-empty file after the fallback", present, empty)
			}
			// The stream message carried the block, so the close leaves it whole
			// and parses the tail later; the salvage is the message, not a stub.
			if !strings.Contains(string(got), "```relevo") {
				t.Errorf("output = %q, want the block-carrying message kept whole", got)
			}
		})
	}
}

// TestWriteReaderSummaryRefusalIsNotAFallback pins that a symlink a runner
// planted at the output path is still a refusal: the close reports an error and
// no fallback, so the round closes without an output rather than quietly saving
// the stream over a path it must not follow.
func TestWriteReaderSummaryRefusalIsNotAFallback(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	b := testReaderBinding()
	writeReaderStream(t, rt, b.Name, b.Round, "final message")

	out := reportPathFor(rt, b)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "pwned"), out); err != nil {
		t.Fatal(err)
	}

	_, fellBack, err := writeReaderSummary(rt, b)
	if err == nil {
		t.Fatalf("writeReaderSummary = nil error, want the planted symlink refused")
	}
	if fellBack {
		t.Errorf("fellBack = true for a refused output path, want false: a refusal is not a fallback")
	}
}

// TestReaderFallbackIsMarkedForTheMasterMind pins that the fallback is visible
// where the MasterMind reads it: the close's own note token, the header
// `show --output` puts above the salvaged text, and no header at all on a round
// that closed on the runner's own file.
func TestReaderFallbackIsMarkedForTheMasterMind(t *testing.T) {
	t.Parallel()

	// The header line a reader of the output is shown.
	if !strings.HasPrefix(view.ReaderFallbackHeaderLine, "note: ") {
		t.Errorf("the header line must read as a note, got %q", view.ReaderFallbackHeaderLine)
	}

	entries := []store.LogEntry{
		{Round: 1, Note: "noreport"},
		{Round: 2, Note: "unmarked/" + view.ReaderFallbackNote},
		{Round: 2, Note: "escape"},
	}
	if got := view.ReaderFallbackHeader(entries, 2); got != view.ReaderFallbackHeaderLine {
		t.Errorf("readerFallbackHeader(round 2) = %q, want %q", got, view.ReaderFallbackHeaderLine)
	}
	if got := view.ReaderFallbackHeader(entries, 1); got != "" {
		t.Errorf("readerFallbackHeader(round 1) = %q, want no header: that round did not fall back", got)
	}

	// The same round through Show: a saved fallback is marked above the text, a
	// file-first close is not marked at all.
	st := store.New(t.TempDir())
	rt := Runtime{Store: st}
	b := testReaderBinding()
	b.CWD = "/repo"
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	out := st.OutputPath(b.Name, b.Round, "reviewer", "findings")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("# salvaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	appendNote := func(note string) {
		t.Helper()
		if err := st.AppendLog(b.Name, store.LogEntry{
			Round: b.Round, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true,
		}); err != nil {
			t.Fatalf("AppendLog(prompt): %v", err)
		}
		if err := st.AppendLog(b.Name, store.LogEntry{
			Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Note: note, Confirmed: true,
		}); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	appendNote(view.ReaderFallbackNote)
	res, err := Show(ctx, rt, ShowOptions{Name: b.Name, Round: b.Round, Section: ShowOutput})
	if err != nil {
		t.Fatalf("Show(--output): %v", err)
	}
	if res.Missing {
		t.Fatalf("Show(--output) = missing, want the saved output")
	}
	if !strings.HasPrefix(res.Text, view.ReaderFallbackHeaderLine) {
		t.Errorf("--output = %q, want it headed with %q", res.Text, view.ReaderFallbackHeaderLine)
	}
	if !strings.Contains(res.Text, "# salvaged") {
		t.Errorf("--output = %q, want the salvaged text under the header", res.Text)
	}

	// A round whose close kept the runner's own file carries no header, so the
	// marker cannot be mistaken for a property of every reader output.
	st2 := store.New(t.TempDir())
	rt2 := Runtime{Store: st2}
	b2 := testReaderBinding()
	b2.CWD = "/repo"
	if err := st2.Save(b2); err != nil {
		t.Fatal(err)
	}
	out2 := st2.OutputPath(b2.Name, b2.Round, "reviewer", "findings")
	if err := os.MkdirAll(filepath.Dir(out2), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out2, []byte("# the runner's own\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, e := range []store.LogEntry{
		{Round: b2.Round, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{Round: b2.Round, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true},
	} {
		if err := st2.AppendLog(b2.Name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	res2, err := Show(ctx, rt2, ShowOptions{Name: b2.Name, Round: b2.Round, Section: ShowOutput})
	if err != nil {
		t.Fatalf("Show(--output): %v", err)
	}
	if res2.Text != "# the runner's own\n" {
		t.Errorf("--output = %q, want the runner's own file with no header", res2.Text)
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

	_, _, err := writeReaderSummary(rt, b)
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

	_, _, err := writeReaderSummary(rt, b)
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

// TestWriteReaderOutputRefusesEscapingArtifactDir pins that creating a reader's
// output through the out/ root refuses a NNN-<actor> directory swapped for a
// symlink out of out/, so nothing is written outside the state directory.
func TestWriteReaderOutputRefusesEscapingArtifactDir(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	if err := os.MkdirAll(st.OutDir("reader-bind"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(st.OutDir("reader-bind"), "001-reviewer")); err != nil {
		t.Fatal(err)
	}
	root, err := st.OutRoot("reader-bind")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	if err := writeReaderOutput(root, filepath.Join("001-reviewer", "out.md"), "hello"); err == nil {
		t.Fatal("writeReaderOutput followed an escaping artifact dir")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the outside directory holds %v, want nothing", entries)
	}
}

// TestReplaceReaderOutputRefusesEscapingArtifactDir pins the same for the
// truncate-write path: the file behind the escaping symlink is refused and left
// byte-identical.
func TestReplaceReaderOutputRefusesEscapingArtifactDir(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	if err := os.MkdirAll(st.OutDir("reader-bind"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	outside := filepath.Join(target, "out.md")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(st.OutDir("reader-bind"), "001-reviewer")); err != nil {
		t.Fatal(err)
	}
	root, err := st.OutRoot("reader-bind")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	if err := replaceReaderOutput(root, filepath.Join("001-reviewer", "out.md"), []byte("pwned\n")); err == nil {
		t.Fatal("replaceReaderOutput followed an escaping artifact dir")
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "outside\n" {
		t.Errorf("outside file = %q, %v; want it untouched", b, err)
	}
}

// TestReaderArtifactIsTheBlockCarryingMessageNotATrailingSummary pins the
// artifact selection for a reader that writes a complete deliverable ending in
// a relevo block followed by a trailing summary or recap: the saved artifact
// must be the block-carrying message, not the trailing summary.
func TestReaderArtifactIsTheBlockCarryingMessageNotATrailingSummary(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	b := testReaderBinding()
	const planMessage = "# Plan\n\n1. Do the work.\n2. Verify it.\n\n```relevo\nstatus: done\nhalted_at: \"\"\nchanged_paths: []\ncommands_run: []\nnot_done: []\n```\n"
	const trailingSummary = "The plan is above, outlining the two implementation steps."
	writeReaderMessages(t, rt, b.Name, b.Round, planMessage, trailingSummary)

	if _, _, err := writeReaderSummary(rt, b); err != nil {
		t.Fatalf("writeReaderSummary: %v", err)
	}
	got, err := os.ReadFile(reportPathFor(rt, b))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "# Plan") {
		t.Errorf("artifact = %q, want the plan message", got)
	}
	if strings.Contains(string(got), trailingSummary) {
		t.Errorf("artifact = %q, want trailing summary excluded", got)
	}
}
