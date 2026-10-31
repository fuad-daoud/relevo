package remote

import (
	"path/filepath"
	"strings"
)

// ContainsDotDotRel reports whether any element of rel is "..", before any
// cleaning: it is how a rel that escapes the artifact directory is refused.
func ContainsDotDotRel(rel string) bool {
	for _, part := range strings.FieldsFunc(rel, func(r rune) bool {
		return r == '/' || r == '\\' || r == filepath.Separator
	}) {
		if part == ".." {
			return true
		}
	}
	return false
}
