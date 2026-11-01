// Package release answers what relevo asks about its own distribution: how
// the binary was installed, whether the daemon's cached check names a newer
// release, and the network fetch and verified download behind both.
package release

import (
	"strconv"
	"strings"
)

// Version is a parsed vMAJOR.MINOR.PATCH; pre-release and build metadata are
// kept verbatim in Suffix.
type Version struct {
	Major, Minor, Patch int
	Suffix              string
}

// ParseVersion accepts "v1.2.3", "1.2.3" and "v1.2.3-2-gabc1234". ok is false
// for "(devel)" and anything unparseable.
func ParseVersion(s string) (v Version, ok bool) {
	rest := strings.TrimPrefix(s, "v")

	num, suffix := rest, ""
	if i := strings.IndexAny(rest, "-+"); i >= 0 {
		num, suffix = rest[:i], rest[i:]
	}

	parts := strings.Split(num, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, false
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2], Suffix: suffix}, true
}

// IsReleaseTag reports whether s is exactly "v" followed by three
// dot-separated digit runs, checked byte by byte since it becomes a URL path
// segment. Leading zeros are allowed; a suffix or surrounding whitespace is not.
func IsReleaseTag(s string) bool {
	if len(s) < 5 || s[0] != 'v' {
		return false
	}
	dots, digits := 0, 0
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digits++
		case c == '.':
			if digits == 0 {
				return false
			}
			dots++
			digits = 0
		default:
			return false
		}
	}
	return dots == 2 && digits > 0
}

// Newer reports whether latest is strictly newer than running, false whenever
// either side is unparseable. On equal numbers nothing is newer: a suffix on
// running means it is commits past that tag, never older.
func Newer(running, latest Version) bool {
	switch {
	case running.Major != latest.Major:
		return running.Major < latest.Major
	case running.Minor != latest.Minor:
		return running.Minor < latest.Minor
	case running.Patch != latest.Patch:
		return running.Patch < latest.Patch
	}
	return false
}

func NewerStrings(running, latest string) bool {
	rv, rok := ParseVersion(running)
	lv, lok := ParseVersion(latest)
	return rok && lok && Newer(rv, lv)
}
