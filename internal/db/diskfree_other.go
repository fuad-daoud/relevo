//go:build !unix

package db

import "errors"

// diskFree has no reading off unix, a build-only target, so a pass that needs
// room for a backup refuses there rather than guess.
func diskFree(string) (uint64, error) {
	return 0, errors.New("free space is not measured on this platform")
}
