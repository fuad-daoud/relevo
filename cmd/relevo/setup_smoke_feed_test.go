package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fuad-daoud/relevo/internal/transcript"
)

// TestSmokeFeedPrintsEachToolCallOnce pins the feed's reading of a growing
// stream: tool calls only, each once across reads, paths relative to the
// smoke directory, a half-written line held until its newline lands.
func TestSmokeFeedPrintsEachToolCallOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "001-runner.jsonl")
	call := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Write","input":{"file_path":"/state/setup-smoke/repo/HELLO.md"}}]}}`
	prose := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}`
	f := &smokeFeed{kind: "claude", render: transcript.NewRenderer(), trim: "/state/setup-smoke/"}

	write := func(s string) {
		fh, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fh.WriteString(s)
		_ = fh.Close()
	}
	write(call[:40])
	if got := f.newLines(path); len(got) != 0 {
		t.Fatalf("half a line printed %v", got)
	}
	write(call[40:] + "\n" + prose + "\n")
	if got := f.newLines(path); !slices.Equal(got, []string{"● Write repo/HELLO.md"}) {
		t.Fatalf("first read = %q, want the one tool call", got)
	}
	if got := f.newLines(path); len(got) != 0 {
		t.Fatalf("a second read printed %v again", got)
	}
}
