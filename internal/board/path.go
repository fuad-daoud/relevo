package board

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func splitExisting(path string) (tail, anchor string, err error) {
	cur := path
	for {
		if _, statErr := os.Lstat(cur); statErr == nil {
			rel, relErr := filepath.Rel(cur, path)
			if relErr != nil {
				return "", "", relErr
			}
			if rel == "." {
				rel = ""
			}
			return rel, cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", "", fmt.Errorf("no existing ancestor of %s", path)
		}
		cur = parent
	}
}

// evalExisting resolves symlinks on path's deepest existing ancestor and
// keeps the not-yet-existing tail, so two spellings of one directory compare
// equal even when the deeper path does not exist yet. A bare EvalSymlinks on
// a missing path falls back to the unresolved form, which compares two
// spellings of one directory as different (macOS /var vs /private/var).
func evalExisting(path string) string {
	clean := filepath.Clean(path)
	tail, anchor, err := splitExisting(clean)
	if err != nil {
		return clean
	}
	real, err := filepath.EvalSymlinks(anchor)
	if err != nil {
		return clean
	}
	if tail == "" {
		return real
	}
	return filepath.Join(real, tail)
}

// underRoot reports whether path, a cleaned absolute path, is root itself or
// sits beneath it.
func underRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
