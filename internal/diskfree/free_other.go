//go:build !unix

package diskfree

import "errors"

// Free has no reading off unix, so a caller that needs room refuses there
// rather than guess.
func Free(string) (uint64, error) {
	return 0, errors.New("free space is not measured on this platform")
}
