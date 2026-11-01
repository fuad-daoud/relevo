package remote

import (
	"errors"
)

// ErrNoGitIdentity is the client-side refusal for an add --server whose repo
// has no effective git identity. A remote builder commits as the client, so
// there is nobody to commit as. It is returned before anything is created, on
// the server or locally.
var ErrNoGitIdentity = errors.New("no git identity")

// Is40Hex reports whether s is already a resolved commit sha, the form a base
// takes after RefSHA, so a create can send it without resolving it again.
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
