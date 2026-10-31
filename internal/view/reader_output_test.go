package view

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestReaderOutputEmptyReadsBlankAsEmpty pins the close's empty rule: zero
// bytes and whitespace-only content both read as empty, content does not, and
// a missing path reports its read error instead of guessing.
func TestReaderOutputEmptyReadsBlankAsEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(empty, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	blank := filepath.Join(dir, "blank.md")
	if err := os.WriteFile(blank, []byte("  \n\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, "full.md")
	if err := os.WriteFile(full, []byte("# findings\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{"zero bytes", empty, true},
		{"whitespace only", blank, true},
		{"content", full, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReaderOutputEmpty(tc.path)
			if err != nil {
				t.Fatalf("ReaderOutputEmpty: %v", err)
			}
			if got != tc.want {
				t.Errorf("ReaderOutputEmpty = %v, want %v", got, tc.want)
			}
		})
	}

	if _, err := ReaderOutputEmpty(filepath.Join(dir, "missing.md")); err == nil {
		t.Errorf("ReaderOutputEmpty on a missing path = nil error, want the read error")
	}
}

// TestReaderOutputPresentSortsPaths pins the presence rule: a regular file
// with content is present and not empty, an empty one is present and empty,
// and a missing path or a directory is neither.
func TestReaderOutputPresentSortsPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	full := filepath.Join(dir, "full.md")
	if err := os.WriteFile(full, []byte("# findings\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blank := filepath.Join(dir, "blank.md")
	if err := os.WriteFile(blank, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	if present, empty := ReaderOutputPresent(full); !present || empty {
		t.Errorf("ReaderOutputPresent(full) = (%v, %v), want (true, false)", present, empty)
	}
	if present, empty := ReaderOutputPresent(blank); !present || !empty {
		t.Errorf("ReaderOutputPresent(blank) = (%v, %v), want (true, true)", present, empty)
	}
	if present, empty := ReaderOutputPresent(filepath.Join(dir, "missing.md")); present || empty {
		t.Errorf("ReaderOutputPresent(missing) = (%v, %v), want (false, false)", present, empty)
	}
	if present, empty := ReaderOutputPresent(dir); present || empty {
		t.Errorf("ReaderOutputPresent(dir) = (%v, %v), want (false, false)", present, empty)
	}
}

// TestReaderFallbackHeaderMarksOnlyTheMarkedRound pins the marking rule: the
// header line answers only for a round whose log carries the fallback token,
// and the newest marking of the round wins.
func TestReaderFallbackHeaderMarksOnlyTheMarkedRound(t *testing.T) {
	t.Parallel()

	if !strings.HasPrefix(ReaderFallbackHeaderLine, "note: ") {
		t.Errorf("the header line must read as a note, got %q", ReaderFallbackHeaderLine)
	}

	entries := []store.LogEntry{
		{Round: 1, Note: "noreport"},
		{Round: 2, Note: "unmarked/" + ReaderFallbackNote},
		{Round: 2, Note: "escape"},
	}
	if got := ReaderFallbackHeader(entries, 2); got != ReaderFallbackHeaderLine {
		t.Errorf("ReaderFallbackHeader(round 2) = %q, want %q", got, ReaderFallbackHeaderLine)
	}
	if got := ReaderFallbackHeader(entries, 1); got != "" {
		t.Errorf("ReaderFallbackHeader(round 1) = %q, want no header", got)
	}
}

// TestWithFallbackHeaderHeadsOnlySalvagedOutput pins the display rule: a
// salvaged output is headed with the fallback line, a runner-written one is
// returned unchanged.
func TestWithFallbackHeaderHeadsOnlySalvagedOutput(t *testing.T) {
	t.Parallel()

	entries := []store.LogEntry{{Round: 1, Note: ReaderFallbackNote}}
	if got := WithFallbackHeader(entries, 1, "saved"); got != ReaderFallbackHeaderLine+"\nsaved" {
		t.Errorf("WithFallbackHeader = %q, want the header above the text", got)
	}
	if got := WithFallbackHeader(nil, 1, "own"); got != "own" {
		t.Errorf("WithFallbackHeader = %q, want the text unchanged", got)
	}
}

// readerOutputStream builds a claude stream whose assistant texts are texts,
// in order.
func readerOutputStream(t *testing.T, texts ...string) []byte {
	t.Helper()
	var b strings.Builder
	for _, text := range texts {
		raw, err := json.Marshal(map[string]any{
			"type":    "assistant",
			"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// TestReaderOutputTextPrefersTheBlockMessage pins the selection rule: a plain
// reader keeps the block-carrying message over a recap, a chain reader gets it
// stripped of its block, and a block-free stream yields its last text.
func TestReaderOutputTextPrefersTheBlockMessage(t *testing.T) {
	t.Parallel()

	block := "# Findings\n\nThe code is sound.\n\n```relevo\nstatus: done\n```\n"
	stream := readerOutputStream(t, block, "Done.")

	if got := ReaderOutputText("claude", stream, false); !strings.Contains(got, "The code is sound") || !strings.Contains(got, "```relevo") {
		t.Errorf("ReaderOutputText(plain) = %q, want the block-carrying message kept whole", got)
	}
	if got := ReaderOutputText("claude", stream, true); !strings.Contains(got, "The code is sound") || strings.Contains(got, "```relevo") {
		t.Errorf("ReaderOutputText(chain) = %q, want the message with its block stripped", got)
	}

	plain := readerOutputStream(t, "First message.", "The final message.")
	if got := ReaderOutputText("claude", plain, false); got != "The final message." {
		t.Errorf("ReaderOutputText(block-free) = %q, want the last text", got)
	}
	if got := ReaderOutputText("claude", nil, false); got != "" {
		t.Errorf("ReaderOutputText(empty) = %q, want nothing", got)
	}
}

// TestLastStreamKindFollowsTheLastSegment pins the kind rule: the last
// segment's kind wins, and the endpoint's own kind covers a round with none.
func TestLastStreamKindFollowsTheLastSegment(t *testing.T) {
	t.Parallel()

	bare := store.Binding{Builder: store.Endpoint{Kind: "claude"}}
	if got := LastStreamKind(bare); got != "claude" {
		t.Errorf("LastStreamKind(bare) = %q, want the endpoint kind", got)
	}
	switched := store.Binding{Builder: store.Endpoint{
		Kind:           "claude",
		StreamSegments: []store.StreamSegment{{Kind: "claude"}, {Kind: "opencode"}},
	}}
	if got := LastStreamKind(switched); got != "opencode" {
		t.Errorf("LastStreamKind(switched) = %q, want the last segment kind", got)
	}
}
