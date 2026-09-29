// Package upgrade moves the running daemon onto a replaced relevo binary,
// deciding behind a two-check debounce and a preflight when it is safe.
package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveExe returns the running executable's path, symlinks resolved and any
// trailing " (deleted)" stripped: a replaced binary's /proc link reports that
// suffix, and a re-exec must target the live path, not the deleted inode.
func ResolveExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("upgrade: resolve executable: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("upgrade: resolve %s: %w", exe, err)
	}

	return strings.TrimSuffix(resolved, " (deleted)"), nil
}
