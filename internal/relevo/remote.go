package relevo

import (
	"errors"
)

// ErrServerPreTier is a client-side refusal that happens before any server
// state changes: the server does not advertise the "tier" feature, so a
// requested --tier has nowhere to land.
var ErrServerPreTier = errors.New("server does not carry a permission tier")

// ErrNoGitIdentity is the client-side refusal for an add --server whose repo
// has no effective git identity (#335): a remote builder commits as the
// client, so there is nobody to commit as. It is returned before anything
// is created, on the server or locally.
var ErrNoGitIdentity = errors.New("no git identity")

// orText returns s, or fallback when s is empty.
func orText(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

func is40Hex(s string) bool {
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
