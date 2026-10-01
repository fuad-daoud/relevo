package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// ReadFile returns path's bytes from disk, or from the sealed round_file row
// when a seal pass already moved a round file into the database. A reserved
// round-file name is row-only: relevo never writes one to disk, so a file with
// that name is a plant or a stale copy and the bytes come from the row, live or
// archived, or the call is a miss. A runner-output name (a flat report or done
// marker, or a nested artifact) resolves its two homes out/ first, refuses
// anything that is not a regular file, and then falls back to the row. A miss
// returns os.ReadFile's own error, so errors.Is(err, fs.ErrNotExist) and
// os.IsNotExist keep working.
func (s *Store) ReadFile(path string) ([]byte, error) {
	if _, name, ok := s.bindingRelOf(path); ok && reservedRoundFile(name) {
		body, _, found, err := s.sealedRoundFile(path)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
		}
		return body, nil
	}

	if binding, name, ok := s.bindingRelOf(path); ok && runnerOutputName(name) {
		return s.readRunnerOutput(binding, name, path)
	}

	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	body, _, found, lerr := s.sealedRoundFile(path)
	if lerr != nil {
		return nil, lerr
	}
	if !found {
		return nil, err
	}
	return body, nil
}

// readRunnerOutput reads a runner-output name from its two homes, out/ first,
// and then from the sealed row. Disk touches go through Lstat and O_NOFOLLOW so
// a symlink at either home is refused, never followed; a regular disk file wins
// over a row, as it always did.
func (s *Store) readRunnerOutput(binding, name, path string) ([]byte, error) {
	for _, p := range []string{s.runnerPath(binding, name, true), s.runnerPath(binding, name, false)} {
		data, ok, err := readRegularFile(p)
		if err != nil {
			return nil, err
		}
		if ok {
			return data, nil
		}
	}

	body, _, found, lerr := s.sealedRoundFile(path)
	if lerr != nil {
		return nil, lerr
	}
	if !found {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	return body, nil
}

// readRegularFile reads path when it is a regular file, refusing a symlink or
// any other non-regular file. ok is false with a nil error when path is absent.
func readRegularFile(path string) (data []byte, ok bool, err error) {
	fi, lerr := os.Lstat(path)
	if lerr != nil {
		if errors.Is(lerr, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, lerr
	}
	if !fi.Mode().IsRegular() {
		return nil, false, fmt.Errorf("runner output %s is not a regular file", path)
	}
	f, oerr := os.OpenFile(path, os.O_RDONLY|noFollow, 0)
	if oerr != nil {
		return nil, false, oerr
	}
	defer func() { _ = f.Close() }()
	body, rerr := io.ReadAll(f)
	if rerr != nil {
		return nil, false, rerr
	}
	return body, true, nil
}

// StatFile is ReadFile for os.Stat callers: the file's size and mtime when it
// is on disk, and the sealed row's when it was sealed. A reserved round-file
// name is row-only, so its size and mtime come from the row and never from a
// file: a plant cannot set them. A runner-output name resolves its two homes
// out/ first with a strict Lstat, so a symlink or other non-regular file is
// refused. A miss returns os.Stat's own error alongside ok == false.
func (s *Store) StatFile(path string) (size int64, mtime time.Time, ok bool, err error) {
	if _, name, resolved := s.bindingRelOf(path); resolved && reservedRoundFile(name) {
		body, mt, found, ferr := s.sealedRoundFile(path)
		if ferr != nil {
			return 0, time.Time{}, false, ferr
		}
		if !found {
			return 0, time.Time{}, false, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
		}
		return int64(len(body)), mt, true, nil
	}

	if binding, name, resolved := s.bindingRelOf(path); resolved && runnerOutputName(name) {
		return s.statRunnerOutput(binding, name, path)
	}

	info, err := os.Stat(path)
	if err == nil {
		return info.Size(), info.ModTime(), true, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return 0, time.Time{}, false, err
	}
	missErr := err

	body, mt, found, lerr := s.sealedRoundFile(path)
	if lerr != nil {
		return 0, time.Time{}, false, lerr
	}
	if !found {
		return 0, time.Time{}, false, missErr
	}
	return int64(len(body)), mt, true, nil
}

// statRunnerOutput stats a runner-output name's two homes, out/ first,
// refusing a non-regular file, then falls back to the sealed row.
func (s *Store) statRunnerOutput(binding, name, path string) (int64, time.Time, bool, error) {
	for _, p := range []string{s.runnerPath(binding, name, true), s.runnerPath(binding, name, false)} {
		fi, lerr := os.Lstat(p)
		if lerr == nil {
			if !fi.Mode().IsRegular() {
				return 0, time.Time{}, false, fmt.Errorf("runner output %s is not a regular file", p)
			}
			return fi.Size(), fi.ModTime(), true, nil
		}
		if !errors.Is(lerr, fs.ErrNotExist) {
			return 0, time.Time{}, false, lerr
		}
	}

	body, mt, found, lerr := s.sealedRoundFile(path)
	if lerr != nil {
		return 0, time.Time{}, false, lerr
	}
	if !found {
		return 0, time.Time{}, false, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
	}
	return int64(len(body)), mt, true, nil
}

// sealedRoundFile answers path from the row that holds it: the name's live
// record, or its most recently archived one, where archive() put its round
// files. found is false when no row holds the name, so the caller keeps its own
// not-exist error.
func (s *Store) sealedRoundFile(path string) (body []byte, mtime time.Time, found bool, err error) {
	d, recordID, name, ok, lerr := s.sealedLookup(path)
	if lerr != nil || !ok {
		return nil, time.Time{}, false, lerr
	}
	body, mtime, found, err = d.RoundFileGet(recordID, name)
	if err != nil {
		return nil, time.Time{}, false, err
	}
	return body, mtime, found, nil
}

// sealedLookup resolves path as a round file of the binding it names under
// s.root and returns the record id whose round_file rows hold it: the name's
// live record, or its most recently archived one, where archive() put its
// round files. A flat path resolves to its base name; a path inside a
// top-level NNN-<actor>/ directory resolves to its nested "NNN-<actor>/<rel>"
// name.
//
// found is false when the path is not such a path, when the name has neither a
// live nor an archived record, and when the root has no database at all, so a
// miss costs no error and leaves the caller's own ErrNotExist in place.
func (s *Store) sealedLookup(path string) (d *db.DB, recordID, name string, found bool, err error) {
	binding, name, ok := s.bindingRelOf(path)
	if !ok {
		return nil, "", "", false, nil
	}

	d, err = s.dbForRead()
	if err != nil || d == nil {
		return nil, "", "", false, err
	}
	rec, ok, err := d.RecordGet(s.owner, binding)
	if err != nil {
		return nil, "", "", false, err
	}
	if !ok {
		rec, ok, err = d.RecordGetArchivedByName(s.owner, binding)
		if err != nil || !ok {
			return nil, "", "", false, err
		}
	}
	return d, rec.ID, name, true, nil
}
