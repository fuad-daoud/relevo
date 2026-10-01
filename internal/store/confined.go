package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// OutRoot opens the binding's out/ directory as an os.Root. Every name handed
// to the returned root resolves inside out/, so a nested NNN-<actor> entry
// swapped for a symlink out of out/ is refused before any read, unlink or
// rename. out/ itself is an entry of the root-owned binding directory and is
// not tenant-swappable; only its contents are, and those are what the root
// confines. The caller closes the root.
func (s *Store) OutRoot(name string) (*os.Root, error) {
	return os.OpenRoot(s.OutDir(name))
}

// WorktreeRoot opens the binding's .worktrees directory as an os.Root. Every
// name handed to the returned root resolves inside .worktrees/, so a .scratch
// entry swapped for a symlink out is refused before any read, unlink or rename.
// The caller closes the root.
func (s *Store) WorktreeRoot() (*os.Root, error) {
	return os.OpenRoot(s.WorktreeDir())
}

// outRelOf resolves path into the name an OutRoot root takes, when path lies
// under the binding's out/ directory. ok is false for the binding directory's
// own home -- the legacy flat layout, which stays raw because that directory is
// root-owned -- and for any path outside the binding.
func (s *Store) outRelOf(name, path string) (string, bool) {
	return relWithin(s.OutDir(name), path)
}

// relWithin reports path's name relative to dir, and whether path lies in dir.
// The comparison is lexical: a component that only looks like ".." does not
// escape, and a sibling whose name shares dir's prefix is outside.
func relWithin(dir, path string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(dir), path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// errNotRegular marks a path a read refuses because it is not a regular file: a
// symlink, a fifo or a directory. The out/ walk reports such a plant so the
// seal can name it and skip it rather than follow it; the reads report it as an
// error.
var errNotRegular = errors.New("not a regular file")

// rootRegularFile opens name inside root as a regular file, refusing a symlink
// or any other non-regular file. It Lstats first -- Root.Lstat does not follow
// the final component -- opens with O_NOFOLLOW, and stats the handle, so a
// final component swapped between the two refuses rather than follows and a
// planted fifo is never opened. ok is false with a nil error when name is
// absent.
func rootRegularFile(root *os.Root, name string) (f *os.File, info fs.FileInfo, ok bool, err error) {
	display := filepath.Join(root.Name(), filepath.FromSlash(name))
	fi, lerr := root.Lstat(name)
	if lerr != nil {
		if errors.Is(lerr, fs.ErrNotExist) {
			return nil, nil, false, nil
		}
		return nil, nil, false, lerr
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, false, fmt.Errorf("%s is %w", display, errNotRegular)
	}
	f, oerr := root.OpenFile(name, os.O_RDONLY|noFollow, 0)
	if oerr != nil {
		return nil, nil, false, oerr
	}
	hfi, serr := f.Stat()
	if serr != nil {
		_ = f.Close()
		return nil, nil, false, serr
	}
	if !hfi.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, false, fmt.Errorf("%s is %w", display, errNotRegular)
	}
	return f, hfi, true, nil
}

// rootReadRegularFile reads name inside root when it is a regular file. ok is
// false with a nil error when name is absent.
func rootReadRegularFile(root *os.Root, name string) ([]byte, bool, error) {
	f, _, ok, err := rootRegularFile(root, name)
	if err != nil || !ok {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	body, rerr := io.ReadAll(f)
	if rerr != nil {
		return nil, false, rerr
	}
	return body, true, nil
}

// rootStatRegularFile stats name inside root when it is a regular file,
// refusing a symlink or any other non-regular file. ok is false with a nil
// error when name is absent.
func rootStatRegularFile(root *os.Root, name string) (fs.FileInfo, bool, error) {
	fi, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is %w", filepath.Join(root.Name(), filepath.FromSlash(name)), errNotRegular)
	}
	return fi, true, nil
}

// rootReadDir lists the entries of dir inside root, read from the directory
// handle so a directory swapped for a symlink out of the root is refused rather
// than followed. A missing dir is (nil, nil).
func rootReadDir(root *os.Root, dir string) ([]fs.DirEntry, error) {
	f, err := root.Open(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, rerr := f.ReadDir(-1)
	if rerr != nil {
		return nil, rerr
	}
	return entries, nil
}
