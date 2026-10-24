package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tmpDirMode is the mode of every directory EnsureTmpEntry makes under a
// runtime's tmp root: owner-only, like the state tmp root itself, because a
// round's scratch can hold anything the builder wrote.
const tmpDirMode = 0o700

// TmpEntry is one direct child of a tmp root's subdirectory: its name and
// the mtime a sweep ages entries by.
type TmpEntry struct {
	Name    string
	ModTime time.Time
}

// tmpRelElems splits rel into the os.Root path elements beneath a tmp root,
// refusing empty, "." and ".." elements so an escape attempt is a named
// error here rather than an EINVAL from a syscall inside a root.
func tmpRelElems(rel string) ([]string, error) {
	elems := strings.Split(filepath.ToSlash(rel), "/")
	for _, e := range elems {
		if e == "" || e == "." || e == ".." {
			return nil, fmt.Errorf("tmp entry %q must stay inside the tmp root", rel)
		}
	}
	return elems, nil
}

// EnsureTmpEntry creates <tmpRoot>/<rel>, every missing element along the
// way, and returns the entry's absolute path. Stats and mkdirs run through
// os.OpenRoot(tmpRoot), so a planted symlink at rounds/ or checks/ is refused
// rather than followed: a sweep deletes entries by name, and the names it
// sees must belong to this root, never to a link's target outside it. Every
// directory created is handed to chownCreated -- in user mode that hands it
// to the tenant, the only writer inside it.
func (s *Store) EnsureTmpEntry(tmpRoot, rel string) (string, error) {
	elems, err := tmpRelElems(rel)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(tmpRoot)
	if err != nil {
		return "", fmt.Errorf("open tmp root: %w", err)
	}
	defer func() { _ = root.Close() }()

	for i := range elems {
		partial := strings.Join(elems[:i+1], "/")
		fi, lerr := root.Lstat(partial)
		if lerr == nil {
			if !fi.IsDir() {
				return "", fmt.Errorf("tmp entry %s is not a directory",
					filepath.Join(tmpRoot, filepath.FromSlash(partial)))
			}
			continue
		}
		if !errors.Is(lerr, fs.ErrNotExist) {
			return "", lerr
		}
		if err := root.Mkdir(partial, tmpDirMode); err != nil {
			return "", err
		}
		if err := s.chownCreated(filepath.Join(tmpRoot, filepath.FromSlash(partial))); err != nil {
			return "", err
		}
	}
	return filepath.Join(tmpRoot, filepath.FromSlash(rel)), nil
}

// RemoveTmpEntry removes <tmpRoot>/<rel> through the same confined root as
// EnsureTmpEntry, so a sweep can never be talked into deleting outside it. A
// missing entry is success, and so is a missing root: it holds nothing.
func (s *Store) RemoveTmpEntry(tmpRoot, rel string) error {
	elems, err := tmpRelElems(rel)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(tmpRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = root.Close() }()

	if err := root.RemoveAll(strings.Join(elems, "/")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ListTmpEntries returns the direct children of <tmpRoot>/<sub> with their
// mtimes, the worklist a sweep ages entries by. A missing root or sub holds
// nothing to sweep and reads empty. The listing opens the sub through the
// confined root, so a sub swapped for a symlink is refused rather than
// followed.
func (s *Store) ListTmpEntries(tmpRoot, sub string) ([]TmpEntry, error) {
	elems, err := tmpRelElems(sub)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(tmpRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = root.Close() }()

	entries, err := rootReadDir(root, strings.Join(elems, "/"))
	if err != nil {
		return nil, err
	}
	out := make([]TmpEntry, 0, len(entries))
	for _, e := range entries {
		fi, ierr := e.Info()
		if ierr != nil {
			return nil, ierr
		}
		out = append(out, TmpEntry{Name: e.Name(), ModTime: fi.ModTime()})
	}
	return out, nil
}
