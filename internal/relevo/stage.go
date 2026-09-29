package relevo

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// stagePlan writes a round's plan to path, creating it when it is absent and
// replacing an existing regular file in place. The plan path sits in a
// runner-writable binding directory, so a symlink planted at it must not be
// followed out of that directory: the Lstat is the refusal, and the flags are
// the race backstop behind it, the same discipline writeReaderOutput and
// replaceReaderOutput state. Without O_CREATE the mode is inert, so an existing
// file's mode and owner are left as they were.
func stagePlan(path string, data []byte) error {
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		return writeStagedPlan(path, os.O_WRONLY|os.O_TRUNC|oNoFollow, data)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return writeStagedPlan(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, data)
}

// writeStagedPlan is stagePlan's write half: open, write, close. A close error
// is the write's own and is surfaced rather than swallowed.
func writeStagedPlan(path string, flag int, data []byte) error {
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// openAppend opens path for appending, refusing anything that is not a regular
// file. The binding directory is runner-writable, so a symlink planted at a
// legacy log must not be followed out of it: the Lstat is the refusal and
// O_NOFOLLOW the race backstop behind it. The Lstat also keeps a planted fifo
// from blocking the open, which would wait for a reader that never comes.
func openAppend(path string) (*os.File, error) {
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", path)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|oNoFollow, 0o644)
}
