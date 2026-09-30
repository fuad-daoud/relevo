package relevo

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ensureRealDir creates dir under root after walking every component between
// them and refusing anything on the way that is not a real directory. MkdirAll
// on its own descends a symlink, so a runner who plants one inside its own
// state directory -- the deepest directory it may write -- would redirect the
// daemon's write out of the state directory. root is that deepest directory, so
// its own ancestors are never checked: a legitimate state root reached through
// a symlinked ancestor must not be refused. The walk stops at the first missing
// component and lets MkdirAll create the rest.
//
// The walk is the refusal; it cannot close the check-then-MkdirAll window, so a
// link swapped in after the check still wins. That is the honest limit of
// portable Go, which has no openat2/RESOLVE_NO_SYMLINKS.
func ensureRealDir(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside %s", dir, root)
	}
	prefix := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		prefix = filepath.Join(prefix, part)
		fi, err := os.Lstat(prefix)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", prefix)
		}
	}
	return os.MkdirAll(dir, 0o755)
}

// openAppendRegular opens path for appending, refusing anything that is not a
// regular file. A round's state directory is runner-writable, so a link planted
// at the log path must not be followed out of it. It duplicates proc's
// unexported openAppend because that one lives in another package and widening
// it would export an API with no caller here. The Lstat is the refusal and
// O_NOFOLLOW the race backstop behind it; the Lstat also keeps a planted fifo
// from blocking the open, which would wait for a reader that never comes.
func openAppendRegular(path string) (*os.File, error) {
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", path)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|oNoFollow, 0o644)
}
