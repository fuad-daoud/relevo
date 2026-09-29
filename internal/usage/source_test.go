package usage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadHeadlessDispatch(t *testing.T) {
	dir := t.TempDir()
	r := New()
	cases := []struct {
		harness, fixture string
		samples          int
	}{
		{"claude", "testdata/claude-stream.jsonl", 1},
		{"agy", "testdata/agy-stream.jsonl", 1},
		{"opencode", "testdata/opencode-stream.jsonl", 2},
	}
	for _, c := range cases {
		path := filepath.Join(dir, c.harness+".jsonl")
		copyFixture(t, c.fixture, path)
		got, note := r.Read(context.Background(), Source{Harness: c.harness, Mode: ModeHeadless, Provider: "p", Model: "m", StreamPath: path})
		if note != "" || len(got) != c.samples {
			t.Errorf("%s: %d samples, note %q; want %d", c.harness, len(got), note, c.samples)
		}
	}
}

func TestReadHeadlessNotes(t *testing.T) {
	r := New()
	if _, note := r.Read(context.Background(), Source{Harness: "claude", Mode: ModeHeadless, StreamPath: "/nonexistent"}); note != "no stream" {
		t.Errorf("missing stream: note = %q", note)
	}
	_, empty := mustTemp(t, "relevo-exit:0\n")
	if _, note := r.Read(context.Background(), Source{Harness: "claude", Mode: ModeHeadless, StreamPath: empty}); note != "no usage events" {
		t.Errorf("no events: note = %q", note)
	}
	if _, note := r.Read(context.Background(), Source{Harness: "unknown-harness", Mode: ModeHeadless, StreamPath: empty}); note != "no reader for unknown-harness" {
		t.Errorf("unknown harness: note = %q", note)
	}
}

func TestStreamClosed(t *testing.T) {
	_, open := mustTemp(t, "{\"type\":\"assistant\"}\n")
	if streamClosed(open) {
		t.Error("no trailer: must be open")
	}
	_, closed := mustTemp(t, "{\"type\":\"result\"}\n\nrelevo-exit:0\n")
	if !streamClosed(closed) {
		t.Error("trailer as last line: must be closed")
	}
	_, mid := mustTemp(t, "relevo-exit:0\n{\"type\":\"assistant\"}\n")
	if streamClosed(mid) {
		t.Error("trailer not last: must be open")
	}
	if streamClosed("/nonexistent") {
		t.Error("missing file is not closed")
	}
}

func TestReadHeadlessWaitsForTrailer(t *testing.T) {
	// Everything but the result event and the trailer, appended 300 ms later.
	raw, err := os.ReadFile("testdata/claude-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	head := strings.Join(lines[:len(lines)-2], "\n") + "\n"
	tail := strings.Join(lines[len(lines)-2:], "\n") + "\n"
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		_, _ = f.WriteString(tail)
		_ = f.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, note := New().Read(ctx, Source{Harness: "claude", Mode: ModeHeadless, Provider: "anthropic", StreamPath: path})
	if note != "" || len(got) != 1 || !got[0].HasCost {
		t.Fatalf("want the measured result sample after the trailer lands; got %d samples, note %q, %+v", len(got), note, got)
	}
}

func TestReadHeadlessTimesOutOnOpenStream(t *testing.T) {
	raw, _ := os.ReadFile("testdata/claude-stream-killed.jsonl")
	// The killed fixture ends in a trailer; strip it to make an open stream.
	body := strings.TrimSuffix(strings.TrimRight(string(raw), "\n"), "relevo-exit:137")
	_, path := mustTemp(t, body)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	got, note := New().Read(ctx, Source{Harness: "claude", Mode: ModeHeadless, Provider: "anthropic", StreamPath: path})
	if note != "stream still open" {
		t.Errorf("note = %q, want \"stream still open\"", note)
	}
	if len(got) == 0 {
		t.Error("an open stream is still read: the fallback samples must come back with the note")
	}
}

// TestPeekOpenStreamReturnsPartial pins that Peek reads an open stream without
// polling for the trailer.
func TestPeekOpenStreamReturnsPartial(t *testing.T) {
	_, path := mustTemp(t,
		`{"type":"assistant","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":0,"output_tokens":5}}}`+"\n"+
			`{"type":"assistant","message":{"id":"msg_2","model":"claude-sonnet-5","usage":{"input_tokens":2,"cache_read_input_tokens":100,"output_tokens":20}}}`+"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	got, note := New().Peek(ctx, Source{Harness: "claude", Mode: ModeHeadless, Provider: "anthropic", StreamPath: path})
	if elapsed := time.Since(start); elapsed >= trailerPoll {
		t.Errorf("Peek took %v; it must not wait for the trailer", elapsed)
	}
	if note != "stream still open" {
		t.Errorf("note = %q, want \"stream still open\"", note)
	}
	if len(got) != 2 {
		t.Fatalf("%d samples, want the two assistant events: %+v", len(got), got)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(f, "{\"type\":\"result\",\"total_cost_usd\":0.5,\"usage\":{\"input_tokens\":12,\"cache_creation_input_tokens\":100,\"cache_read_input_tokens\":100,\"output_tokens\":25}}\n%s0\n", spawn.ExitTrailer)
	_ = f.Close()
	got, note = New().Read(context.Background(), Source{Harness: "claude", Mode: ModeHeadless, Provider: "anthropic", StreamPath: path})
	if note != "" || len(got) != 1 || !got[0].HasCost {
		t.Errorf("after the trailer: %d samples, note %q, %+v; want the measured result sample", len(got), note, got)
	}
}

func TestPeekMissingStream(t *testing.T) {
	got, note := New().Peek(context.Background(), Source{Harness: "claude", Mode: ModeHeadless, StreamPath: "/nonexistent"})
	if got != nil || note != "no stream" {
		t.Errorf("Peek on a missing stream = %d samples, note %q", len(got), note)
	}
}

// TestStreamClosedTrailer pins streamClosed against the trailer forms it must
// recognise, the new spelling included.
func TestStreamClosedTrailer(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]struct {
		body string
		want bool
	}{
		"new trailer":          {"hello\nrelevo-exit:3\n", true},
		"no trailer":           {"hello\n", false},
		"empty last line only": {"\n", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".jsonl")
			if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := streamClosed(p); got != c.want {
				t.Errorf("streamClosed() = %v, want %v for %q", got, c.want, c.body)
			}
		})
	}
}

func TestStreamFromSkipsEarlierSegment(t *testing.T) {
	seg1 := "{\"type\":\"step_finish\",\"timestamp\":1789589782193,\"part\":{\"type\":\"step-finish\",\"tokens\":{\"input\":1000,\"output\":50}}}\n"
	seg2 := "{\"type\":\"step_finish\",\"timestamp\":1789589783193,\"part\":{\"type\":\"step-finish\",\"tokens\":{\"input\":2000,\"output\":100}}}\n"
	trailer := "relevo-exit:0\n"
	full := []byte(seg1 + seg2 + trailer)
	offset2 := int64(len(seg1))

	path := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(path, full, 0o644); err != nil {
		t.Fatal(err)
	}
	r := New()

	samples0, note0 := r.Read(context.Background(), Source{
		Harness: "opencode", Mode: ModeHeadless, StreamPath: path, StreamFrom: 0,
	})
	if note0 != "" || len(samples0) != 2 {
		t.Fatalf("StreamFrom=0: got %d samples, note %q", len(samples0), note0)
	}
	var total0 int64
	for _, s := range samples0 {
		total0 += s.Tokens.Total()
	}
	if total0 != 3150 {
		t.Errorf("StreamFrom=0: total tokens = %d, want 3150", total0)
	}

	samples1, note1 := r.Read(context.Background(), Source{
		Harness: "opencode", Mode: ModeHeadless, StreamPath: path, StreamFrom: offset2,
	})
	if note1 != "" || len(samples1) != 1 {
		t.Fatalf("StreamFrom=offset2: got %d samples, note %q", len(samples1), note1)
	}
	if samples1[0].Tokens.Total() != 2100 {
		t.Errorf("StreamFrom=offset2: total tokens = %d, want 2100", samples1[0].Tokens.Total())
	}

	readFile := func(string) ([]byte, error) {
		return full, nil
	}
	sealed0, noteS0 := r.Read(context.Background(), Source{
		Harness: "opencode", Mode: ModeHeadless, StreamPath: "/not/on/disk", ReadFile: readFile, StreamFrom: 0,
	})
	if noteS0 != "" || len(sealed0) != 2 {
		t.Fatalf("sealed StreamFrom=0: got %d samples, note %q", len(sealed0), noteS0)
	}
	var sealedTot0 int64
	for _, s := range sealed0 {
		sealedTot0 += s.Tokens.Total()
	}
	if sealedTot0 != 3150 {
		t.Errorf("sealed StreamFrom=0: total tokens = %d, want 3150", sealedTot0)
	}

	sealed1, noteS1 := r.Read(context.Background(), Source{
		Harness: "opencode", Mode: ModeHeadless, StreamPath: "/not/on/disk", ReadFile: readFile, StreamFrom: offset2,
	})
	if noteS1 != "" || len(sealed1) != 1 {
		t.Fatalf("sealed StreamFrom=offset2: got %d samples, note %q", len(sealed1), noteS1)
	}
	if sealed1[0].Tokens.Total() != 2100 {
		t.Errorf("sealed StreamFrom=offset2: total tokens = %d, want 2100", sealed1[0].Tokens.Total())
	}
}
