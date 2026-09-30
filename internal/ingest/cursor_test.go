package ingest

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

func opener(path string) func() (io.ReadSeekCloser, error) {
	return func() (io.ReadSeekCloser, error) { return os.Open(path) }
}

func TestReadAppendOnlyFromZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	lines, startSeq, cur, reset, err := readAppendOnly(opener(path), path, db.Cursor{}, false)
	if err != nil {
		t.Fatalf("readAppendOnly: %v", err)
	}
	if reset {
		t.Error("reset = true on a first read, want false")
	}
	if startSeq != 0 {
		t.Errorf("startSeq = %d, want 0", startSeq)
	}
	if got := linesToStrings(lines); !equalStrings(got, []string{"a", "b", "c"}) {
		t.Errorf("lines = %v, want [a b c]", got)
	}
	if cur.ByteOffset != int64(len("a\nb\nc\n")) {
		t.Errorf("ByteOffset = %d, want %d", cur.ByteOffset, len("a\nb\nc\n"))
	}
}

func TestReadAppendOnlyResumes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, _, cur, _, err := readAppendOnly(opener(path), path, db.Cursor{}, false)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString("d\ne\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lines, startSeq, cur2, reset, err := readAppendOnly(opener(path), path, cur, true)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if reset {
		t.Error("reset = true on a plain append, want false")
	}
	if startSeq != 3 {
		t.Errorf("startSeq = %d, want 3", startSeq)
	}
	if got := linesToStrings(lines); !equalStrings(got, []string{"d", "e"}) {
		t.Errorf("lines = %v, want [d e]", got)
	}
	if cur2.ByteOffset != int64(len("a\nb\nc\nd\ne\n")) {
		t.Errorf("ByteOffset = %d, want %d", cur2.ByteOffset, len("a\nb\nc\nd\ne\n"))
	}
}

func TestReadAppendOnlyLeavesPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte("a\nb\npartial"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	lines, _, cur, _, err := readAppendOnly(opener(path), path, db.Cursor{}, false)
	if err != nil {
		t.Fatalf("readAppendOnly: %v", err)
	}
	if got := linesToStrings(lines); !equalStrings(got, []string{"a", "b"}) {
		t.Errorf("lines = %v, want [a b], the partial trailing line must wait", got)
	}
	if cur.ByteOffset != int64(len("a\nb\n")) {
		t.Errorf("ByteOffset = %d, want %d (must not include the partial line)", cur.ByteOffset, len("a\nb\n"))
	}

	lines2, _, _, reset, err := readAppendOnly(opener(path), path, cur, true)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if reset {
		t.Error("reset = true, want false")
	}
	if len(lines2) != 0 {
		t.Errorf("lines2 = %v, want none until the line completes", lines2)
	}
}

// TestIngestCursorReset pins that a file rewritten with different first bytes
// resets the cursor to 0 and returns every line again.
func TestIngestCursorReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, _, cur, _, err := readAppendOnly(opener(path), path, db.Cursor{}, false)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// Different content of the same length, so a size-only check would treat it
	// as an unread continuation.
	if err := os.WriteFile(path, []byte("x\ny\nz\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	lines, startSeq, _, reset, err := readAppendOnly(opener(path), path, cur, true)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if !reset {
		t.Fatal("reset = false, want true after a rewrite with a different head")
	}
	if startSeq != 0 {
		t.Errorf("startSeq = %d, want 0 after a reset", startSeq)
	}
	if got := linesToStrings(lines); !equalStrings(got, []string{"x", "y", "z"}) {
		t.Errorf("lines = %v, want every line of the rewritten file", got)
	}
}

func linesToStrings(lines [][]byte) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = string(l)
	}
	return out
}

// countingReadSeekCloser counts the bytes read through it, so a test can pin
// that an idle member is not read whole.
type countingReadSeekCloser struct {
	f    *os.File
	read int64
}

func (c *countingReadSeekCloser) Read(p []byte) (int, error) {
	n, err := c.f.Read(p)
	c.read += int64(n)
	return n, err
}

func (c *countingReadSeekCloser) Seek(offset int64, whence int) (int64, error) {
	return c.f.Seek(offset, whence)
}

func (c *countingReadSeekCloser) Close() error { return c.f.Close() }

// TestReadAppendOnlyIdleReadsOnlyTheHead pins the cost of a tick with nothing
// appended: the rewrite check's head sample, not the member. Before, every
// tick read and allocated the whole transcript of every binding, which is what
// made the daemon this expensive.
func TestReadAppendOnlyIdleReadsOnlyTheHead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	line := bytes.Repeat([]byte("x"), 63)
	big := bytes.Repeat(append(append([]byte{}, line...), '\n'), 16*1024)
	if err := os.WriteFile(path, big, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, _, cur, _, err := readAppendOnly(opener(path), path, db.Cursor{}, false)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	var counted *countingReadSeekCloser
	countedOpener := func() (io.ReadSeekCloser, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		counted = &countingReadSeekCloser{f: f}
		return counted, nil
	}
	lines, startSeq, cur2, reset, err := readAppendOnly(countedOpener, path, cur, true)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if reset {
		t.Error("reset = true on an unchanged member, want false")
	}
	if len(lines) != 0 || startSeq != 0 {
		t.Errorf("lines = %d, startSeq = %d, want none", len(lines), startSeq)
	}
	if cur2 != cur {
		t.Errorf("cursor = %+v, want it unchanged at %+v", cur2, cur)
	}
	if counted.read > headSampleBytes {
		t.Errorf("read %d bytes of a %d-byte member, want at most the %d-byte head sample",
			counted.read, len(big), headSampleBytes)
	}
}
