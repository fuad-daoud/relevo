package usage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func stepFinish(input, out int64) string {
	return fmt.Sprintf(`{"type":"step_finish","part":{"cost":0.01,"tokens":{"input":%d,"output":%d,"cache":{"read":0,"write":0}}}}`, input, out)
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendBytes(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
}

func cachedEntry(t *testing.T, r Reader, path, harness string) *streamCache {
	t.Helper()
	rd, ok := r.(reader)
	if !ok {
		t.Fatal("production reader expected")
	}
	return rd.cache[path+"\x00"+harness+"\x000"]
}

// TestParseCachedFeedsOnlyAppendedBytes pins that a second Peek after the stream
// grows parses the appended bytes only and returns the whole fold.
func TestParseCachedFeedsOnlyAppendedBytes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLines(t, path, stepFinish(1, 1), stepFinish(1, 1), stepFinish(1, 1))
	r := New()
	src := Source{Harness: "opencode", Mode: ModeHeadless, Provider: "p", Model: "m", StreamPath: path}
	if got, _ := r.Peek(ctx, src); len(got) != 3 {
		t.Fatalf("first Peek: %d samples, want 3", len(got))
	}
	e := cachedEntry(t, r, path, "opencode")
	before := e.offset
	if before == 0 {
		t.Fatal("first Peek must record an offset")
	}

	appended := stepFinish(1, 1) + "\n" + stepFinish(1, 1) + "\n"
	appendBytes(t, path, appended)
	got, _ := r.Peek(ctx, src)
	if len(got) != 5 {
		t.Fatalf("second Peek: %d samples, want 5", len(got))
	}
	if adv := e.offset - before; adv != int64(len(appended)) {
		t.Errorf("offset advanced %d bytes, want exactly the %d appended", adv, len(appended))
	}
}

// TestParseCachedPartialLineWaits pins that an incomplete trailing line is held in
// the cache's tail, not fed half-parsed.
func TestParseCachedPartialLineWaits(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLines(t, path, stepFinish(1, 1), stepFinish(1, 1), stepFinish(1, 1))
	r := New()
	src := Source{Harness: "opencode", Mode: ModeHeadless, Provider: "p", Model: "m", StreamPath: path}
	if got, _ := r.Peek(ctx, src); len(got) != 3 {
		t.Fatalf("first Peek: %d samples, want 3", len(got))
	}
	half := `{"type":"step_finish","part":{"cost":0.01,"tok`
	rest := `ens":{"input":1,"output":1,"cache":{"read":0,"write":0}}}}`
	appendBytes(t, path, half)
	got, _ := r.Peek(ctx, src)
	e := cachedEntry(t, r, path, "opencode")
	if len(got) != 3 {
		t.Errorf("partial line: %d samples, want the 3 complete ones", len(got))
	}
	if len(e.tail) == 0 {
		t.Error("the incomplete line must be kept as the cache's tail")
	}
	appendBytes(t, path, rest+"\n")
	got, _ = r.Peek(ctx, src)
	if len(got) != 4 {
		t.Errorf("after the line completes: %d samples, want 4", len(got))
	}
}

func TestParseCachedTruncatedFileResets(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLines(t, path, stepFinish(1, 1), stepFinish(1, 1), stepFinish(1, 1), stepFinish(1, 1), stepFinish(1, 1))
	r := New()
	src := Source{Harness: "opencode", Mode: ModeHeadless, Provider: "p", Model: "m", StreamPath: path}
	if got, _ := r.Peek(ctx, src); len(got) != 5 {
		t.Fatalf("first Peek: %d samples, want 5", len(got))
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	writeLines(t, path, stepFinish(1, 1))
	got, _ := r.Peek(ctx, src)
	if len(got) != 1 {
		t.Errorf("after truncate-and-rewrite: %d samples, want 1", len(got))
	}
}

// TestParseCachedUnchangedFileIsNoRead pins the stat short-circuit: with no write
// between two Peeks the second opens nothing.
func TestParseCachedUnchangedFileIsNoRead(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLines(t, path, stepFinish(1, 1), stepFinish(1, 1))
	r := New()
	src := Source{Harness: "opencode", Mode: ModeHeadless, Provider: "p", Model: "m", StreamPath: path}
	if got, _ := r.Peek(ctx, src); len(got) != 2 {
		t.Fatalf("first Peek: %d samples, want 2", len(got))
	}
	e := cachedEntry(t, r, path, "opencode")
	offset, size := e.offset, e.size
	if os.Getuid() == 0 {
		if got, _ := r.Peek(ctx, src); len(got) != 2 {
			t.Errorf("second Peek: %d samples, want 2", len(got))
		}
		return
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	got, _ := r.Peek(ctx, src)
	if len(got) != 2 {
		t.Errorf("second Peek must be served from the cache: %d samples", len(got))
	}
	if e.offset != offset || e.size != size {
		t.Errorf("cache entry moved without a write: offset %d->%d size %d->%d", offset, e.offset, size, e.size)
	}
}

// TestClaudeCarryDedupesAcrossFeeds pins that the carry's dedupe survives the
// resumable form: the same message.id fed in two Peek passes is one sample.
func TestClaudeCarryDedupesAcrossFeeds(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	line := `{"type":"assistant","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"input_tokens":10,"cache_read_input_tokens":0,"output_tokens":5}}}`
	writeLines(t, path, line)
	r := New()
	src := Source{Harness: "claude", Mode: ModeHeadless, Provider: "anthropic", StreamPath: path}
	if got, _ := r.Peek(ctx, src); len(got) != 1 {
		t.Fatalf("first Peek: %d samples, want 1", len(got))
	}
	appendBytes(t, path, line+"\n")
	got, _ := r.Peek(ctx, src)
	if len(got) != 1 {
		t.Errorf("duplicate message.id across feeds: %d samples, want 1", len(got))
	}
}
