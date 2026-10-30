// Package tmpdir owns the state-root layout: the root itself, its mode, and
// the tmp tree of temporary files and directories under it.
package tmpdir

import (
	"fmt"
	"os"
	"path/filepath"
)

// StateRootMode is the state root's own mode: owner-only, because the root
// holds the database and every binding's files. MkdirAll never chmods, so a
// root that already exists keeps whatever mode it has.
const StateRootMode = 0o700

// DefaultRoot resolves $XDG_STATE_HOME/relevo, falling back to
// ~/.local/state/relevo.
func DefaultRoot() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "relevo"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	return filepath.Join(home, ".local", "state", "relevo"), nil
}

// TempDir returns <root>/tmp, created StateRootMode (0700). If root is empty, DefaultRoot is used.
func TempDir(root string) (string, error) {
	if root == "" {
		var err error
		root, err = DefaultRoot()
		if err != nil {
			return "", err
		}
	}
	dir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(dir, StateRootMode); err != nil {
		return "", fmt.Errorf("create state tmp dir: %w", err)
	}
	if err := os.Chmod(dir, StateRootMode); err != nil {
		return "", fmt.Errorf("chmod state tmp dir: %w", err)
	}
	return dir, nil
}

// CreateTemp creates a new temporary file in <root>/tmp. If root is empty, DefaultRoot is used.
func CreateTemp(root, pattern string) (*os.File, error) {
	dir, err := TempDir(root)
	if err != nil {
		return nil, err
	}
	return os.CreateTemp(dir, pattern)
}

// MkdirTemp creates a new temporary directory in <root>/tmp. If root is empty, DefaultRoot is used.
func MkdirTemp(root, pattern string) (string, error) {
	dir, err := TempDir(root)
	if err != nil {
		return "", err
	}
	return os.MkdirTemp(dir, pattern)
}
