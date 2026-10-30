package housekeep

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The GOFLAGS a child process builds Go with: a repository whose entry leaves
// trimpath unset runs its Go trees with -trimpath, and `off` opts them out --
// a compaction a repository can refuse, because trimmed paths read
// module-relative in panics and runtime.Caller traces.

// isGoTree reports whether dir's root carries a Go module or workspace file,
// the two places a go command running under dir finds its build context.
func isGoTree(dir string) bool {
	for _, name := range []string{"go.mod", "go.work"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

const (
	GoFlagsKey   = "GOFLAGS="
	trimpathFlag = "-trimpath"
)

// hasTrimpath reads the flag's presence off a GOFLAGS value: any
// "-trimpath=..." field counts, "-trimpath=false" included. The flag's value
// stays unread: an entry that names the flag at all, either way, is the
// caller's build choice, not this repository's to flip.
func hasTrimpath(value string) bool {
	for _, field := range strings.Fields(value) {
		if field == trimpathFlag || strings.HasPrefix(field, trimpathFlag+"=") {
			return true
		}
	}
	return false
}

// GoFlagsEntry is the env parent, with -trimpath carried by GOFLAGS when on
// and tree's root carries a go.mod or go.work. An existing GOFLAGS value is
// appended to rather than replaced, and no other entry is touched; a GOFLAGS
// already naming -trimpath leaves parent unchanged, as do an `off` setting
// and a tree that is not a Go tree, so a caller can hand parent to the child
// unchanged in every state this helper does not own.
func GoFlagsEntry(parent []string, tree string, on bool) []string {
	if !on || !isGoTree(tree) {
		return parent
	}
	for i, entry := range parent {
		rest, ok := strings.CutPrefix(entry, GoFlagsKey)
		if !ok {
			continue
		}
		if hasTrimpath(rest) {
			return parent
		}
		out := slices.Clone(parent)
		out[i] = GoFlagsKey + strings.TrimSpace(rest+" "+trimpathFlag)
		return out
	}
	return append(slices.Clone(parent), GoFlagsKey+trimpathFlag)
}
