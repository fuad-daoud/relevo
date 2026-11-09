package board

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

// ErrConflict reports an If-Match that does not match the bytes on disk: the
// caller's view of the board is stale.
var ErrConflict = errors.New("conflict")

// ErrInvalid reports a body the board cannot read: a malformed document, a
// symlink where a file belongs, or an over-large board.
var ErrInvalid = errors.New("invalid")

// ErrNotFound reports a board the caller named but which is not on disk. A
// write refuses it rather than treating a missing file as a new board.
var ErrNotFound = errors.New("not found")

// notFoundError carries ErrNotFound while printing the refusal text alone.
type notFoundError struct{ msg string }

func (e *notFoundError) Error() string { return e.msg }
func (e *notFoundError) Unwrap() error { return ErrNotFound }

// Load reads the board at path. A missing file is not an error: it is a new
// board with an empty etag, and nothing is created until the first write.
func Load(path string) (data []byte, etag string, isNew bool, err error) {
	data, err = os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", true, nil
		}
		return nil, "", false, err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), false, nil
}

// renameFile is the rename writeAtomic steps through, a seam a test replaces
// to prove a crash before the rename leaves the old file and no temp file.
var renameFile = os.Rename

// writeAtomic writes data to a temp file in path's directory and renames it
// into place. A failure removes the temp file, so an interrupted save leaves
// the old file and no litter.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".board-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := renameFile(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
