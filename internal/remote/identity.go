package remote

import (
	"errors"
)

// A remote builder commits as the client, so there is nobody to commit as.
// It is returned before anything is created, on the server or locally.
var ErrNoGitIdentity = errors.New("no git identity")

// A base takes this form after RefSHA, so a create can send it without resolving it again.
func Is40Hex(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
