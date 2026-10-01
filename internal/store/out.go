package store

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// outDirName is the runner-writable state directory's name: a binding's report,
// done marker and artifact directories live under <binding>/out/.
const outDirName = "out"

// OutDir returns the binding's runner-writable state directory, <binding>/out.
// It is the only directory passed to a harness as its writable root.
func (s *Store) OutDir(name string) string { return filepath.Join(s.Dir(name), outDirName) }

// outRoundFile is roundFile with out/ as the parent: <binding>/out/NNN-suffix.ext.
func (s *Store) outRoundFile(name string, round int, suffix, ext string) string {
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return filepath.Join(s.OutDir(name), fmt.Sprintf("%03d-%s%s", round, suffix, ext))
}

// EnsureOutDir creates <binding>/out when it is absent. A symlink or any other
// non-directory already at that path is refused: the harness's writable root
// must be a real directory, never a link to somewhere else.
func (s *Store) EnsureOutDir(name string) error {
	dir := s.OutDir(name)
	fi, err := os.Lstat(dir)
	if err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("out dir %s is not a directory", dir)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, bindingDirMode); err != nil {
		return err
	}
	return s.chownCreated(dir)
}

// runnerOutputExists reports whether the exact runner-output path exists either
// as a regular file on disk or as a sealed round_file row. A non-regular file
// at the path is not "present": it is a plant the reads refuse.
func (s *Store) runnerOutputExists(path string) bool {
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() {
		return true
	}
	_, _, found, err := s.sealedRoundFile(path)
	return err == nil && found
}

// resolveRunnerOutput resolves a runner-output path the PromptPath/StreamPath
// way: out/ when it exists (on disk or as a sealed row), else the old flat path
// when it exists, else out/ -- the name a fresh round writes.
func (s *Store) resolveRunnerOutput(name string, round int, suffix, ext string) string {
	newPath := s.outRoundFile(name, round, suffix, ext)
	if s.runnerOutputExists(newPath) {
		return newPath
	}
	oldPath := s.roundFile(name, round, suffix, ext)
	if s.runnerOutputExists(oldPath) {
		return oldPath
	}
	return newPath
}

// runnerPath is the path of a named runner-output file in one of its two homes:
// out/ when out is true, the binding directory otherwise. name is a flat
// round_file name or a nested "NNN-<actor>/<rel>".
func (s *Store) runnerPath(binding, name string, out bool) string {
	root := s.Dir(binding)
	if out {
		root = s.OutDir(binding)
	}
	return filepath.Join(root, filepath.FromSlash(name))
}

// runnerOutputName reports whether name is a runner-output round file: a flat
// report or done marker, or any nested "NNN-<actor>/<rel>" artifact. These are
// the names whose two homes the reads resolve, out/ first.
func runnerOutputName(name string) bool {
	if strings.Contains(name, "/") {
		return true
	}
	return runnerOutputBase(name)
}

// runnerOutputBase reports whether base is a flat runner-output name: a round's
// report or its done marker.
func runnerOutputBase(base string) bool {
	return strings.HasSuffix(base, "-report.md") || strings.HasSuffix(base, "-done")
}

// migratableRunnerOutput reports whether a binding-directory entry is a
// runner-output file the out/ layout moves: a report, a done marker, or a
// top-level NNN-* artifact directory.
func migratableRunnerOutput(base string, isDir bool) bool {
	if base == outDirName || !roundBaseRe.MatchString(base) {
		return false
	}
	if isDir {
		return true
	}
	return runnerOutputBase(base)
}

// outLayoutWarned dedupes the leftover warning to once per process per path.
var outLayoutWarned sync.Map

// MigrateOutLayout moves a binding's runner-output files into <binding>/out/:
// the flat NNN-report.md and NNN-done files and every top-level NNN-* artifact
// directory. It returns how many entries it moved. A destination that already
// exists is never clobbered -- the old file is left in place, untouched, and
// warned about once per process. A failed rename leaves the file for the next
// tick. out/ being a child of the binding directory, the rename is
// same-filesystem by construction, so no copy fallback is needed.
func (s *Store) MigrateOutLayout(name string) (int, error) {
	if err := s.EnsureOutDir(name); err != nil {
		return 0, err
	}
	dir := s.Dir(name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}

	moved := 0
	for _, e := range entries {
		base := e.Name()
		if !migratableRunnerOutput(base, e.IsDir()) {
			continue
		}
		src := filepath.Join(dir, base)
		dst := filepath.Join(s.OutDir(name), base)
		if _, derr := os.Lstat(dst); derr == nil {
			if _, warned := outLayoutWarned.LoadOrStore(name+"\x00"+dst, struct{}{}); !warned {
				slog.Warn("out layout: both homes exist, leaving the old file", "binding", name, "file", base)
			}
			continue
		} else if !errors.Is(derr, fs.ErrNotExist) {
			return moved, derr
		}
		if rerr := os.Rename(src, dst); rerr != nil {
			if errors.Is(rerr, fs.ErrNotExist) {
				continue
			}
			slog.Warn("out layout: could not move runner file into out/", "binding", name, "file", base, "err", rerr)
			continue
		}
		moved++
	}
	return moved, nil
}
