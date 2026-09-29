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
// complete lines preceding the first returned one.
//
// The rewrite check hashes only the bytes the saved cursor already confirmed --
// min(headSampleBytes, cur.ByteOffset) -- never the current file's own first
// headSampleBytes: for a file still shorter than headSampleBytes, comparing
// against the live head would make every ordinary append look like a rewrite.
//
// reset is true when the content shrank below cur.ByteOffset or that prefix's
// hash changed. It is not an error: the caller logs at Info, and the UNIQUE keys
// on event and transcript make re-appending known rows a no-op.
func readAppendOnly(opener func() (io.ReadCloser, error), sourceKey string, cur db.Cursor, hasCursor bool) (lines [][]byte, startSeq int, next db.Cursor, reset bool, err error) {
	rc, err := opener()
	if err != nil {
		return nil, 0, db.Cursor{}, false, err
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, 0, db.Cursor{}, false, err
	}

	var offset int64
	if hasCursor {
		sample := cur.ByteOffset
		if sample > headSampleBytes {
			sample = headSampleBytes
		}
		if int64(len(data)) >= cur.ByteOffset && sha256Hex(data[:sample]) == cur.HeadSHA {
			offset = cur.ByteOffset
		} else {
			reset = true
		}
	}

	startSeq = bytes.Count(data[:offset], []byte{'\n'})

	newOffset := offset
	tail := data[offset:]
	if end := bytes.LastIndexByte(tail, '\n'); end >= 0 {
		lines = append(lines, bytes.Split(tail[:end], []byte{'\n'})...)
		newOffset = offset + int64(end) + 1
	}

	newSample := newOffset
	if newSample > headSampleBytes {
		newSample = headSampleBytes
	}
	newHeadSHA := sha256Hex(data[:newSample])

	return lines, startSeq, db.Cursor{Source: sourceKey, ByteOffset: newOffset, HeadSHA: newHeadSHA}, reset, nil
}
