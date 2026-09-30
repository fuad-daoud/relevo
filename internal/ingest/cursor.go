package ingest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/db"
)

// headSampleBytes is how much of an append-only member's start is hashed to
// detect a rewrite.
const headSampleBytes = 4096

// prefixChunkBytes is the buffer the confirmed prefix is counted through. The
// prefix is only ever counted, never kept, and only when a tail arrived, so an
// idle member costs one head sample per read, not its whole length.
const prefixChunkBytes = 64 << 10

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// cursorSourceKey is the ingest_cursor natural key for member of src.
func cursorSourceKey(src Source, member string) string {
	kind, path := src.Origin()
	if kind == "archive" {
		return path + "::" + member
	}
	return filepath.Join(path, member)
}

// readAppendOnly returns every complete line added since cur, plus the cursor to
// save. A trailing partial line waits for the next read; startSeq counts the
// complete lines preceding the first returned one, and is zero when no line was
// returned (its callers only use it to number the returned lines).
//
// The reader is seeked, never read whole: the rewrite check hashes only the
// bytes the saved cursor already confirmed -- min(headSampleBytes,
// cur.ByteOffset) -- and the confirmed prefix is counted in place, so a member
// nothing appended to costs one head sample instead of its whole length. The
// prefix is counted through a fixed buffer and only when a complete new line
// arrived, because that count is what numbers the new lines.
//
// The rewrite check never hashes the current member's own first
// headSampleBytes: for a file still shorter than headSampleBytes, comparing
// against the live head would make every ordinary append look like a rewrite.
//
// reset is true when the content shrank below cur.ByteOffset or that prefix's
// hash changed. It is not an error: the caller logs at Info, and the UNIQUE keys
// on event and transcript make re-appending known rows a no-op.
func readAppendOnly(opener func() (io.ReadSeekCloser, error), sourceKey string, cur db.Cursor, hasCursor bool) (lines [][]byte, startSeq int, next db.Cursor, reset bool, err error) {
	rc, err := opener()
	if err != nil {
		return nil, 0, db.Cursor{}, false, err
	}
	defer func() { _ = rc.Close() }()

	size, err := rc.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, 0, db.Cursor{}, false, err
	}

	var offset int64
	var head []byte
	if hasCursor {
		if size < cur.ByteOffset {
			reset = true
		} else {
			sample, herr := readHead(rc, min64(headSampleBytes, cur.ByteOffset))
			if herr != nil {
				return nil, 0, db.Cursor{}, false, herr
			}
			if sha256Hex(sample) == cur.HeadSHA {
				offset, head = cur.ByteOffset, sample
			} else {
				reset = true
			}
		}
	}

	tail, err := readFrom(rc, offset, size)
	if err != nil {
		return nil, 0, db.Cursor{}, false, err
	}

	newOffset := offset
	if end := bytes.LastIndexByte(tail, '\n'); end >= 0 {
		// The complete lines before offset number the ones returned, so the
		// prefix is counted here and only here: a member with no complete new
		// line pays nothing for a count no caller reads.
		if startSeq, err = countNewlines(rc, offset); err != nil {
			return nil, 0, db.Cursor{}, false, err
		}
		lines = append(lines, bytes.Split(tail[:end], []byte{'\n'})...)
		newOffset = offset + int64(end) + 1
	}

	// A member with no complete new line keeps the head sample the rewrite
	// check already read, so an idle read never reads more than that sample.
	if newOffset != offset || head == nil {
		if head, err = readHead(rc, min64(headSampleBytes, newOffset)); err != nil {
			return nil, 0, db.Cursor{}, false, err
		}
	}
	return lines, startSeq, db.Cursor{Source: sourceKey, ByteOffset: newOffset, HeadSHA: sha256Hex(head)}, reset, nil
}

// readHead reads the first n bytes of rc, for the rewrite check and the cursor
// it saves. A zero n is the empty member's own head.
func readHead(rc io.ReadSeeker, n int64) ([]byte, error) {
	if n == 0 {
		return nil, nil
	}
	if _, err := rc.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	head := make([]byte, n)
	if _, err := io.ReadFull(rc, head); err != nil {
		return nil, err
	}
	return head, nil
}

// readFrom returns the bytes from offset to end, which is the tail a cursor has
// not confirmed. It reads at most size-offset bytes: a member that shrank under
// an offset already known to be past its end returns nothing rather than
// failing.
func readFrom(rc io.ReadSeeker, offset, size int64) ([]byte, error) {
	if offset >= size {
		return nil, nil
	}
	if _, err := rc.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(rc, size-offset))
}

// countNewlines counts the newline bytes in the first n bytes of rc without
// keeping them.
func countNewlines(rc io.ReadSeeker, n int64) (int, error) {
	if n == 0 {
		return 0, nil
	}
	if _, err := rc.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	buf := make([]byte, prefixChunkBytes)
	count := 0
	for n > 0 {
		chunk := int64(len(buf))
		if chunk > n {
			chunk = n
		}
		m, err := io.ReadFull(rc, buf[:chunk])
		count += bytes.Count(buf[:m], []byte{'\n'})
		n -= int64(m)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return count, nil
			}
			return 0, err
		}
	}
	return count, nil
}

// min64 is min for the byte counts readAppendOnly handles.
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
