package chatlabel

import (
	"bytes"
	"io"
	"os"
)

// ReadTail returns the last max bytes of the file at path (max <= 0 means
// DefaultTailBytes), starting at a line boundary since the window is cut from
// the end of a long, appended-to transcript. Errors are returned as-is.
func ReadTail(path string, max int64) ([]byte, error) {
	if max <= 0 {
		max = DefaultTailBytes
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size <= max {
		return io.ReadAll(f)
	}

	if _, err := f.Seek(size-max, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, max)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}

	// The window opens mid-line; everything through the first newline is partial.
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		return buf[i+1:], nil
	}
	return []byte{}, nil
}
