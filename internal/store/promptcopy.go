package store

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// PromptCopyPath returns the path to the staged prompt copy inside the
// binding's out/ directory for the given round.
func (s *Store) PromptCopyPath(name string, round int) string {
	return filepath.Join(s.OutDir(name), fmt.Sprintf(".%03d-prompt.md", round))
}

// StagePromptCopy writes body to the round's prompt copy path inside out/
// through OutRoot, refusing to follow symlinks. If an existing non-regular entry
// is present, it is replaced only if root.Remove succeeds.
//
// out/ is created here, because staging runs while the launch's argv is being
// built, which is before startProcess's EnsureOutDir; a symlink or any other
// non-directory already at that path is refused, so the round fails rather than
// start with a prompt the runner cannot read.
func (s *Store) StagePromptCopy(name string, round int, body []byte) (string, error) {
	if err := s.EnsureOutDir(name); err != nil {
		return "", fmt.Errorf("ensure out dir %s: %w", s.OutDir(name), err)
	}
	root, err := s.OutRoot(name)
	if err != nil {
		return "", fmt.Errorf("open out dir %s: %w", s.OutDir(name), err)
	}
	defer func() { _ = root.Close() }()

	rel := fmt.Sprintf(".%03d-prompt.md", round)
	if fi, err := root.Lstat(rel); err == nil {
		if !fi.Mode().IsRegular() {
			if rerr := root.Remove(rel); rerr != nil {
				return "", fmt.Errorf("remove non-regular %s: %w", rel, rerr)
			}
		} else {
			if rerr := root.Remove(rel); rerr != nil {
				return "", fmt.Errorf("remove %s: %w", rel, rerr)
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return s.PromptCopyPath(name, round), nil
}

// removePromptCopy removes the round's prompt copy through the open root of out/.
// A missing copy is ignored; any other removal error is logged as a warning.
func removePromptCopy(root *os.Root, round int) {
	if root == nil {
		return
	}
	rel := fmt.Sprintf(".%03d-prompt.md", round)
	if err := root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("seal: could not remove prompt copy", "file", filepath.Join(root.Name(), rel), "err", err)
	}
}
