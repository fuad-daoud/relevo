package ui

import "strings"

// shortVersion strips a trailing "-dirty" and a trailing "-g<7+ hex>" from v.
// Anything else is returned unchanged (§4).
func shortVersion(v string) string {
	if v == "" {
		return ""
	}
	s := strings.TrimSuffix(v, "-dirty")
	if i := strings.LastIndex(s, "-g"); i >= 0 {
		hash := s[i+2:]
		if isHex(hash) {
			s = s[:i]
		}
	}
	return s
}

func isHex(s string) bool {
	if len(s) < 7 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
