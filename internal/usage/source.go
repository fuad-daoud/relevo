package usage

import (
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

type Mode string

const (
	ModeHeadless Mode = "headless"
)

// Source is everything a reader needs to find one round's record.
type Source struct {
	Harness    string // "claude" | "agy" | "opencode" | "codex"
	Mode       Mode
	Provider   string // the candidate's; "" for an adopted builder
	Model      string // the candidate's; "" for an adopted builder
	Plan       bool   // the candidate's subscription flag
	StreamPath string // headless: the round's stream, NNN-runner.jsonl, or NNN-builder.jsonl for a round from before the rename
	StreamFrom int64  // parse only bytes at or after this offset; 0 means whole stream
	// ReadFile, when set, reads a sealed round's stream from the store's database.
	ReadFile   func(string) ([]byte, error)
	Worktree   string // pane: the binding's worktree; "" for a --cwd binding
	Start, End time.Time
}

// Reader turns a Source into samples; it never errors.
type Reader interface {
	Read(ctx context.Context, src Source) (samples []Sample, note string)
	// Peek is Read without waiting for the record to close.
	Peek(ctx context.Context, src Source) (samples []Sample, note string)
}

// reader's mu and cache are pointers, so every copy shares them.
type reader struct {
	mu    *sync.Mutex
	cache map[string]*streamCache
}

func New() Reader {
	return reader{mu: &sync.Mutex{}, cache: map[string]*streamCache{}}
}

func (r reader) Read(ctx context.Context, src Source) ([]Sample, string) {
	switch src.Mode {
	case ModeHeadless:
		return r.readStream(ctx, src, true)
	}
	return nil, "no reader for mode " + string(src.Mode)
}

func (r reader) Peek(ctx context.Context, src Source) ([]Sample, string) {
	switch src.Mode {
	case ModeHeadless:
		return r.readStream(ctx, src, false)
	}
	return nil, "no reader for mode " + string(src.Mode)
}

// legacyExitTrailer is legacy.ExitTrailer: the same line a stream written before
// the rename ends in, taken from legacy so the old name lives in one place.
const legacyExitTrailer = legacy.ExitTrailer

// LegacyExitTrailerForTest exposes legacyExitTrailer so internal/proc can pin it.
func LegacyExitTrailerForTest() string { return legacyExitTrailer }

const trailerPoll = 200 * time.Millisecond // how often a still-open stream is re-checked

const tailProbe = 256 // bytes of the file's end read to find the last line

// streamClosed reports whether path's last non-empty line is the exit trailer: the
// harness has exited. A pre-rename stream ends in the old relay-exit: form instead. // name-guard: legacy
func streamClosed(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	size := info.Size()
	n := int64(tailProbe)
	if size < n {
		n = size
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, size-n); err != nil && err != io.EOF {
		return false
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	last := lines[len(lines)-1]
	return strings.HasPrefix(last, spawn.ExitTrailer) || strings.HasPrefix(last, legacyExitTrailer)
}

// waitClosed blocks until the stream is closed or ctx is done.
func waitClosed(ctx context.Context, path string) bool {
	for {
		if streamClosed(path) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(trailerPoll):
		}
	}
}

func (r reader) readStream(ctx context.Context, src Source, wait bool) ([]Sample, string) {
	if _, err := os.Stat(src.StreamPath); err != nil {
		return readSealed(src)
	}
	closed := false
	if wait {
		closed = waitClosed(ctx, src.StreamPath)
	} else {
		closed = streamClosed(src.StreamPath)
	}
	samples, note := r.parseCached(src)
	if note != "" {
		return nil, note // "no reader for <harness>"
	}
	switch {
	case len(samples) == 0:
		return nil, "no usage events"
	case !closed:
		return samples, "stream still open"
	}
	return samples, ""
}

// readSealed reads a sealed round's stream through Source.ReadFile, whole.
func readSealed(src Source) ([]Sample, string) {
	if src.ReadFile == nil {
		return nil, "no stream"
	}
	data, err := src.ReadFile(src.StreamPath)
	if err != nil {
		return nil, "no stream"
	}
	c, ok := newSourceCarry(src)
	if !ok {
		return nil, "no reader for " + src.Harness
	}
	if src.StreamFrom <= int64(len(data)) {
		data = data[src.StreamFrom:]
	} else {
		data = nil
	}
	// Only whole lines are fed, as parseCached does.
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		data = data[:i+1]
	} else {
		data = nil
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) > 0 && line[0] == '{' && len(line) <= maxLine {
			c.feed(line)
		}
	}
	samples := c.samples()
	if len(samples) == 0 {
		return nil, "no usage events"
	}
	return samples, ""
}

// newSourceCarry resolves a codex model's effort the way every reader path must.
func newSourceCarry(src Source) (streamCarry, bool) {
	model := src.Model
	if src.Harness == "codex" {
		if id, _, err := harness.SplitEffort(src.Model); err == nil {
			model = id
		}
	}
	return newCarry(src.Harness, src.Provider, model)
}

// parseCached reads src's stream through the per-stream cache: an unchanged (size,
// mtime) is answered from the carry, an appended file from where the last read
// stopped, and a shrunken file resets.
func (r reader) parseCached(src Source) ([]Sample, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, err := os.Stat(src.StreamPath)
	if err != nil {
		return nil, "no stream"
	}
	// Keyed by path, harness and stream offset: a different harness on the same path
	// must not reuse the previous carry.
	key := src.StreamPath + "\x00" + src.Harness + "\x00" + strconv.FormatInt(src.StreamFrom, 10)
	e := r.cache[key]
	if e == nil || info.Size() < e.offset || info.Size() < src.StreamFrom {
		c, ok := newSourceCarry(src)
		if !ok {
			return nil, "no reader for " + src.Harness
		}
		e = &streamCache{carry: c, offset: src.StreamFrom}
		r.cache[key] = e
	}
	if info.Size() == e.size && info.ModTime().Equal(e.mtime) {
		return e.carry.samples(), ""
	}
	f, err := os.Open(src.StreamPath)
	if err != nil {
		return nil, "no stream"
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(e.offset, io.SeekStart); err != nil {
		return nil, "no stream"
	}
	read, err := io.ReadAll(f)
	if err != nil {
		return nil, "no stream"
	}
	buf := append(e.tail, read...)
	last := 0
	for i, b := range buf {
		if b != '\n' {
			continue
		}
		line := buf[last:i]
		last = i + 1
		if len(line) > 0 && line[0] == '{' && len(line) <= maxLine {
			e.carry.feed(line)
		}
	}
	e.tail = append([]byte(nil), buf[last:]...)
	if len(e.tail) > maxLine {
		e.tail = nil // a pathological line, dropped as scanLines drops one
	}
	// offset is the bytes already read from disk, tail included, so the held tail is
	// never re-read.
	e.offset = info.Size()
	e.size, e.mtime = info.Size(), info.ModTime()
	return e.carry.samples(), ""
}
