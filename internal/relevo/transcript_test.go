package relevo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/store"
)

// jsonlLine is line idx (0-based) of a transcript testdata file, newline
// included, so a stream can be built from real harness lines.
func jsonlLine(t *testing.T, name string, idx int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "transcript", "testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if idx < 0 || idx >= len(lines) {
		t.Fatalf("%s has %d lines, want line %d", name, len(lines), idx)
	}
	return lines[idx] + "\n"
}

// transcriptStore is a saved binding "webshop" with round 1, so a round_file
// row can be written against it.
func transcriptStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.New(t.TempDir())
	if err := s.Save(store.Binding{
		Name:    "webshop",
		CWD:     "/work/webshop",
		Round:   1,
		Builder: store.Endpoint{Kind: "claude"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return s
}

func transcriptRead(s *store.Store) func(string) ([]byte, bool, error) {
	return func(path string) ([]byte, bool, error) {
		return readBytesMissing(s.ReadFile, path)
	}
}

// N1: renderStream must be byte-for-byte what the drain appends for the same
// stream, and stable as a prefix when the stream grows.
func TestRenderStreamMatchesTheDrain(t *testing.T) {
	t.Parallel()

	agy := jsonlLine(t, "agy.jsonl", 5) + jsonlLine(t, "agy.jsonl", 6)
	claude := jsonlLine(t, "claude.jsonl", 5) + jsonlLine(t, "claude.jsonl", 6)
	// A trailing partial line: the stream ends mid-event.
	stream := agy + claude + `{"type":"assistant","message":{"content":[{"type":"text"`
	segs := []store.StreamSegment{{Start: 0, Kind: "agy"}, {Start: int64(len(agy)), Kind: "claude"}}

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	seedLegacyLog(t, rt, "webshop", 1)
	streamWrite(t, rt, stream)

	b.Builder.Kind = "claude"
	b.Builder.StreamRound = 1
	b.Builder.StreamOffset = 0
	b.Builder.StreamStart = int64(len(agy))
	b.Builder.StreamSegments = segs
	drainStream(rt, b)

	log, err := os.ReadFile(rt.Store.BuilderLogPath("webshop", 1))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	want := renderStream([]byte(stream), segs, "claude")
	if !bytes.Equal(log, want) {
		t.Fatalf("drained log = %q, want renderStream = %q", log, want)
	}
	if !bytes.HasSuffix(want, []byte("\n")) || bytes.HasSuffix(want, []byte("\n\n")) {
		t.Fatalf("renderStream = %q, want complete lines only", want)
	}

	// Prefix stability: complete lines appended after the partial one extend
	// the old rendering, never rewrite it.
	streamWrite(t, rt, jsonlLine(t, "claude.jsonl", 9))
	full, err := os.ReadFile(rt.Store.RunnerStreamPath("webshop", 1))
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	after := renderStream(full, segs, "claude")
	if !bytes.HasPrefix(after, want) {
		t.Errorf("renderStream after growth = %q, want %q as a prefix", after, want)
	}
	if bytes.Equal(after, want) {
		t.Errorf("renderStream did not grow after a complete line was appended")
	}
}

// N2: streamTail is the last n rendered lines, read backwards in doubling
// windows, across a segment boundary, and through the read fallback.
func TestStreamTail(t *testing.T) {
	t.Parallel()

	agy := jsonlLine(t, "agy.jsonl", 5) + jsonlLine(t, "agy.jsonl", 6)
	multi := `{"type":"assistant","message":{"content":[{"type":"text","text":"alpha\nbeta\ngamma"}]}}` + "\n"
	var body strings.Builder
	for body.Len() < 300*1024 {
		body.WriteString(multi)
	}
	stream := agy + body.String()
	segs := []store.StreamSegment{{Start: 0, Kind: "agy"}, {Start: int64(len(agy)), Kind: "claude"}}

	dir := t.TempDir()
	path := filepath.Join(dir, "001-builder.jsonl")
	if err := os.WriteFile(path, []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	rendered := renderStream([]byte(stream), segs, "claude")
	all := strings.Split(strings.TrimRight(string(rendered), "\n"), "\n")
	if len(all) < 60 {
		t.Fatalf("rendered only %d lines, want a large stream", len(all))
	}

	for _, n := range []int{1, 3, 40} {
		want := strings.Join(all[len(all)-n:], "\n")
		if got := streamTail(path, os.ReadFile, segs, "claude", n, 0); got != want {
			t.Errorf("streamTail(n=%d) = %q, want %q", n, got, want)
		}
		// The same bytes through the read fallback, as a sealed round.
		read := func(string) ([]byte, error) { return []byte(stream), nil }
		if got := streamTail(filepath.Join(dir, "absent.jsonl"), read, segs, "claude", n, 0); got != want {
			t.Errorf("streamTail read fallback (n=%d) = %q, want %q", n, got, want)
		}
	}

	// A segment boundary inside the window: an agy prefix large enough that
	// the first 64 KiB window starts inside it and still crosses into claude.
	agyOne := jsonlLine(t, "agy.jsonl", 5) + jsonlLine(t, "agy.jsonl", 6)
	var agyBig strings.Builder
	for agyBig.Len() < 40*1024 {
		agyBig.WriteString(agyOne)
	}
	var claudeBig strings.Builder
	for claudeBig.Len() < 40*1024 {
		claudeBig.WriteString(multi)
	}
	crossStream := agyBig.String() + claudeBig.String()
	crossSegs := []store.StreamSegment{{Start: 0, Kind: "agy"}, {Start: int64(agyBig.Len()), Kind: "claude"}}
	crossPath := filepath.Join(dir, "002-builder.jsonl")
	if err := os.WriteFile(crossPath, []byte(crossStream), 0o644); err != nil {
		t.Fatal(err)
	}
	crossRendered := renderStream([]byte(crossStream), crossSegs, "claude")
	crossAll := strings.Split(strings.TrimRight(string(crossRendered), "\n"), "\n")
	agyLines := strings.Count(string(renderStream([]byte(agyBig.String()), crossSegs, "claude")), "\n")
	n := (len(crossAll) - agyLines) + 25 // the last 25 agy lines plus every claude line
	if n <= 0 || n > len(crossAll) {
		t.Fatalf("boundary n = %d, out of range for %d rendered lines", n, len(crossAll))
	}
	want := strings.Join(crossAll[len(crossAll)-n:], "\n")
	if got := streamTail(crossPath, os.ReadFile, crossSegs, "claude", n, 0); got != want {
		t.Errorf("streamTail across a segment boundary = %q, want %q", got, want)
	}

	// N8: a stream whose last 64 KiB renders to fewer than 40 lines. Each
	// stream line is about 8 KiB of assistant text, so it renders to exactly
	// one line; a single fixed 64 KiB window (2a's M3) would return about
	// eight of the forty wanted.
	bigLine := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + strings.Repeat("x", 8*1024) + `"}]}}` + "\n"
	var few strings.Builder
	for few.Len() < 400*1024 {
		few.WriteString(bigLine)
	}
	fewPath := filepath.Join(dir, "003-builder.jsonl")
	if err := os.WriteFile(fewPath, []byte(few.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	fewAll := strings.Split(strings.TrimRight(string(renderStream([]byte(few.String()), nil, "claude")), "\n"), "\n")
	if len(fewAll) < 40 {
		t.Fatalf("rendered only %d lines, want at least 40", len(fewAll))
	}
	wantFew := strings.Join(fewAll[len(fewAll)-40:], "\n")
	if got := streamTail(fewPath, os.ReadFile, nil, "claude", 40, 0); got != wantFew {
		t.Errorf("streamTail(few, 40) = %d bytes, want the last 40 rendered lines (%d bytes); a fixed 64 KiB window is not enough", len(got), len(wantFew))
	}

	if got := streamTail(filepath.Join(dir, "missing.jsonl"), os.ReadFile, segs, "claude", 5, 0); got != "" {
		t.Errorf("streamTail(missing) = %q, want empty", got)
	}
	if got := streamTail(path, os.ReadFile, segs, "claude", 0, 0); got != "" {
		t.Errorf("streamTail(n=0) = %q, want empty", got)
	}
}

// N3: builderTail reads the log when the endpoint writes one, the stream when
// it points elsewhere (2b), and "" when neither exists.
func TestBuilderTailLogOrStream(t *testing.T) {
	t.Parallel()

	s := transcriptStore(t)
	rt := Runtime{Store: s}
	logPath := s.BuilderLogPath("webshop", 1)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("log one\nlog two\nlog three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.BuilderStreamPath("webshop", 1), []byte("stream says something else\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := s.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	b.Builder.LogPath = logPath
	if got := builderTail(rt, b, 2); got != "log two\nlog three" {
		t.Errorf("builderTail with a log = %q, want the log's tail", got)
	}

	b.Builder.LogPath = s.BuilderStreamPath("webshop", 1)
	if got := builderTail(rt, b, 5); got != "stream says something else" {
		t.Errorf("builderTail with the stream as LogPath = %q, want the rendered stream", got)
	}

	b.Builder.LogPath = ""
	if got := builderTail(rt, b, 5); got != "stream says something else" {
		t.Errorf("builderTail with no log = %q, want the rendered stream", got)
	}

	ghost := store.Binding{Name: "ghost", Round: 1}
	if got := builderTail(rt, ghost, 5); got != "" {
		t.Errorf("builderTail with nothing = %q, want empty", got)
	}
}

// N4: RoundTranscript prefers the log, falls back to the rendered stream with
// the round's stored segments, then the live endpoint's, then the fallback
// kind, and reports a miss.
func TestRoundTranscript(t *testing.T) {
	t.Parallel()

	agy := jsonlLine(t, "agy.jsonl", 5)
	claude := jsonlLine(t, "claude.jsonl", 5) + jsonlLine(t, "claude.jsonl", 6)
	stream := agy + claude
	segs := []store.StreamSegment{{Start: 0, Kind: "agy"}, {Start: int64(len(agy)), Kind: "claude"}}

	t.Run("log present wins", func(t *testing.T) {
		s := transcriptStore(t)
		logPath := s.BuilderLogPath("webshop", 1)
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(logPath, []byte("the log\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.RunnerStreamPath("webshop", 1), []byte(stream), 0o644); err != nil {
			t.Fatal(err)
		}
		text, source, found, err := RoundTranscript(s, "webshop", 1, store.Endpoint{}, transcriptRead(s))
		if err != nil || !found {
			t.Fatalf("found = %v, err = %v, want found", found, err)
		}
		if string(text) != "the log\n" || source != "001-builder.log" {
			t.Errorf("text = %q, source = %q, want the log and 001-builder.log", text, source)
		}
	})

	t.Run("stream plus a segments row", func(t *testing.T) {
		s := transcriptStore(t)
		if err := os.WriteFile(s.RunnerStreamPath("webshop", 1), []byte(stream), 0o644); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(segs)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.WithLock(func(tx *store.Tx) error {
			return tx.PutRoundFile("webshop", 1, s.BuilderSegmentsPath("webshop", 1), body)
		}); err != nil {
			t.Fatalf("PutRoundFile: %v", err)
		}
		text, source, found, err := RoundTranscript(s, "webshop", 1, store.Endpoint{}, transcriptRead(s))
		if err != nil || !found {
			t.Fatalf("found = %v, err = %v, want found", found, err)
		}
		if want := renderStream([]byte(stream), segs, ""); !bytes.Equal(text, want) {
			t.Errorf("text = %q, want the row-rendered stream %q", text, want)
		}
		if source != "001-runner.jsonl (rendered)" {
			t.Errorf("source = %q, want 001-runner.jsonl (rendered)", source)
		}
	})

	t.Run("live endpoint segments when there is no row", func(t *testing.T) {
		s := transcriptStore(t)
		if err := os.WriteFile(s.RunnerStreamPath("webshop", 1), []byte(stream), 0o644); err != nil {
			t.Fatal(err)
		}
		live := store.Endpoint{StreamRound: 1, Kind: "claude", StreamSegments: segs}
		text, _, found, err := RoundTranscript(s, "webshop", 1, live, transcriptRead(s))
		if err != nil || !found {
			t.Fatalf("found = %v, err = %v, want found", found, err)
		}
		if want := renderStream([]byte(stream), segs, "claude"); !bytes.Equal(text, want) {
			t.Errorf("text = %q, want the live-segment rendering %q", text, want)
		}
	})

	t.Run("no row and no live segments uses the fallback kind", func(t *testing.T) {
		s := transcriptStore(t)
		if err := os.WriteFile(s.RunnerStreamPath("webshop", 1), []byte(stream), 0o644); err != nil {
			t.Fatal(err)
		}
		live := store.Endpoint{Kind: "agy"}
		text, _, found, err := RoundTranscript(s, "webshop", 1, live, transcriptRead(s))
		if err != nil || !found {
			t.Fatalf("found = %v, err = %v, want found", found, err)
		}
		if want := renderStream([]byte(stream), nil, "agy"); !bytes.Equal(text, want) {
			t.Errorf("text = %q, want the fallback-kind rendering %q", text, want)
		}
	})

	t.Run("nothing is not found", func(t *testing.T) {
		s := transcriptStore(t)
		_, _, found, err := RoundTranscript(s, "webshop", 1, store.Endpoint{}, transcriptRead(s))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if found {
			t.Error("found = true, want false")
		}
	})

	t.Run("archived read adapter", func(t *testing.T) {
		s := transcriptStore(t)
		files := map[string][]byte{
			"001-builder.jsonl": []byte(stream),
			"001-builder-segments.json": func() []byte {
				b, _ := json.Marshal(segs)
				return b
			}(),
		}
		read := func(path string) ([]byte, bool, error) {
			data, ok := files[filepath.Base(path)]
			return data, ok, nil
		}
		text, source, found, err := RoundTranscript(s, "webshop", 1, store.Endpoint{Kind: "claude"}, read)
		if err != nil || !found {
			t.Fatalf("found = %v, err = %v, want found", found, err)
		}
		if want := renderStream([]byte(stream), segs, "claude"); !bytes.Equal(text, want) {
			t.Errorf("text = %q, want the sealed rendering %q", text, want)
		}
		if source != "001-builder.jsonl (rendered)" {
			t.Errorf("source = %q, want 001-builder.jsonl (rendered)", source)
		}
	})

	t.Run("the pre-rename stream name", func(t *testing.T) {
		s := transcriptStore(t)
		if err := os.WriteFile(s.BuilderStreamPath("webshop", 1), []byte(stream), 0o644); err != nil {
			t.Fatal(err)
		}
		text, source, found, err := RoundTranscript(s, "webshop", 1, store.Endpoint{Kind: "claude"}, transcriptRead(s))
		if err != nil || !found {
			t.Fatalf("found = %v, err = %v, want found", found, err)
		}
		if want := renderStream([]byte(stream), nil, "claude"); !bytes.Equal(text, want) {
			t.Errorf("text = %q, want the pre-rename rendering %q", text, want)
		}
		if source != "001-builder.jsonl (rendered)" {
			t.Errorf("source = %q, want 001-builder.jsonl (rendered)", source)
		}
	})
}

// N5: startProcess records the round's segments as a round_file row (never on
// disk), overwrites it as the round switches harnesses, and succeeds with
// only a warning when the binding has no record.
func TestStartProcessRecordsSegmentsRow(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	if err := rt.Store.Save(store.Binding{
		Name:    "webshop",
		Round:   1,
		CWD:     t.TempDir(),
		State:   store.StateActive,
		Builder: store.Endpoint{Mode: store.ModeHeadless, Kind: "agy"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	spawn := func(b store.Binding, kind string) store.Binding {
		t.Helper()
		var got store.Binding
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			var err error
			got, err = startProcess(context.Background(), rt, tx, b, []string{"echo", "hi"}, candidate.Candidate{Harness: kind})
			return err
		})
		if err != nil {
			t.Fatalf("startProcess(%s): %v", kind, err)
		}
		return got
	}
	readSegs := func() []store.StreamSegment {
		t.Helper()
		data, err := rt.Store.ReadFile(rt.Store.BuilderSegmentsPath("webshop", 1))
		if err != nil {
			t.Fatalf("ReadFile(segments): %v", err)
		}
		var out []store.StreamSegment
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("decode segments: %v", err)
		}
		return out
	}

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	got := spawn(b, "agy")
	if want := []store.StreamSegment{{Start: 0, Kind: "agy"}}; !reflect.DeepEqual(readSegs(), want) {
		t.Errorf("row = %+v, want %+v", readSegs(), want)
	}
	if !reflect.DeepEqual(readSegs(), got.Builder.StreamSegments) {
		t.Errorf("row = %+v, want the endpoint's segments %+v", readSegs(), got.Builder.StreamSegments)
	}
	if _, err := os.Stat(rt.Store.BuilderSegmentsPath("webshop", 1)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("segments row leaked to disk: %v", err)
	}

	// A mid-round switch: the row is overwritten with both segments.
	streamPath := rt.Store.BuilderStreamPath("webshop", 1)
	if err := os.MkdirAll(filepath.Dir(streamPath), 0o755); err != nil {
		t.Fatal(err)
	}
	first := []byte(`{"event":"init"}` + "\n")
	if err := os.WriteFile(streamPath, first, 0o644); err != nil {
		t.Fatal(err)
	}
	got.Builder.Kind = "claude"
	got = spawn(got, "claude")
	want := []store.StreamSegment{{Start: 0, Kind: "agy"}, {Start: int64(len(first)), Kind: "claude"}}
	if !reflect.DeepEqual(readSegs(), want) {
		t.Errorf("after a switch row = %+v, want %+v", readSegs(), want)
	}

	// An unsaved binding: the write fails with ErrNotFound, startProcess does
	// not.
	ghost := store.Binding{Name: "ghost", Round: 1, CWD: t.TempDir(), Builder: store.Endpoint{Mode: store.ModeHeadless, Kind: "agy"}}
	if _, err := func() (store.Binding, error) {
		var out store.Binding
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			var err error
			out, err = startProcess(context.Background(), rt, tx, ghost, []string{"echo", "hi"}, candidate.Candidate{Harness: "agy"})
			return err
		})
		return out, err
	}(); err != nil {
		t.Errorf("startProcess without a saved record = %v, want success", err)
	}
}

// N6: once the endpoint has moved to a later round, the old round's
// transcript uses the stored row's segments, not the live ones.
func TestRoundTranscriptOfASealedSwitchedRound(t *testing.T) {
	t.Parallel()

	s := transcriptStore(t)
	agy := jsonlLine(t, "agy.jsonl", 5)
	claude := jsonlLine(t, "claude.jsonl", 5)
	stream := agy + claude
	if err := os.WriteFile(s.RunnerStreamPath("webshop", 2), []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	rowSegs := []store.StreamSegment{{Start: 0, Kind: "agy"}, {Start: int64(len(agy)), Kind: "claude"}}
	body, err := json.Marshal(rowSegs)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile("webshop", 2, s.BuilderSegmentsPath("webshop", 2), body)
	}); err != nil {
		t.Fatalf("PutRoundFile: %v", err)
	}

	live := store.Endpoint{
		StreamRound:    3,
		Kind:           "opencode",
		StreamSegments: []store.StreamSegment{{Start: 0, Kind: "opencode"}},
	}
	text, _, found, err := RoundTranscript(s, "webshop", 2, live, transcriptRead(s))
	if err != nil || !found {
		t.Fatalf("found = %v, err = %v, want found", found, err)
	}
	if want := renderStream([]byte(stream), rowSegs, "opencode"); !bytes.Equal(text, want) {
		t.Errorf("text = %q, want the row-segment rendering %q", text, want)
	}
	if liveRender := renderStream([]byte(stream), live.StreamSegments, "opencode"); bytes.Equal(text, liveRender) {
		t.Errorf("text = %q, want the row's segments, not the live endpoint's", text)
	}
}
