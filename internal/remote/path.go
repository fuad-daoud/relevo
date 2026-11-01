package remote

import (
	"path/filepath"
	"strings"
)

// It is how a rel that escapes the artifact directory is refused.
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
