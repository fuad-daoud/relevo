package board

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultScene is the scene `relevo board` opens when no path is named.
const DefaultScene = "docs/boards/board.excalidraw"

// sceneExt is the only extension the verb accepts, so a scene path can never
// collide with a future subverb name.
const sceneExt = ".excalidraw"

// Resolve turns arg into the scene path the server reads and writes. An empty
// arg is the repo default, under repoRoot. A named path is resolved against
// cwd and must end in .excalidraw; after symlinks are resolved on its deepest
// existing ancestor it must sit under repoRoot, so neither ".." nor a
// symlinked parent can steer a write outside the repository. A missing file is
// accepted: it is a new scene, created on the first save.
func Resolve(repoRoot, cwd, arg string) (string, error) {
	root := filepath.Clean(repoRoot)
	if real, err := filepath.EvalSymlinks(repoRoot); err == nil {
		root = real
	}

	if arg == "" {
		return filepath.Join(root, filepath.FromSlash(DefaultScene)), nil
	}
	if !strings.HasSuffix(arg, sceneExt) {
		return "", usagef("scene path must end in %s: %s", sceneExt, arg)
	}

	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)

	tail, anchor, err := splitExisting(path)
	if err != nil {
		return "", usagef("%v", err)
	}
	real, err := filepath.EvalSymlinks(anchor)
	if err != nil {
		return "", usagef("resolve %s: %v", anchor, err)
	}
	full := filepath.Join(real, tail)
	if !underRoot(root, full) {
		return "", usagef("%s is outside the repository root %s", path, root)
	}
	return path, nil
}

// splitExisting returns the deepest existing ancestor of an absolute path and
// the not-yet-existing remainder beneath it.
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

// evalExisting resolves symlinks on path's deepest existing ancestor and keeps
// the not-yet-existing tail, so two spellings of one directory compare equal
// even when the deeper path does not exist yet. A bare EvalSymlinks on a missing
// path fails and falls back to the unresolved form, which compares two spellings
// of one directory as different -- the macOS /var vs /private/var symlink is the
// case that made the live-scope checks platform-dependent.
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
