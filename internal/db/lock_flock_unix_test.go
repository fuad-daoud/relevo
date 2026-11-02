//go:build unix

package db_test

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFileFlock takes an exclusive flock on f: the helper's "flock" mode
// holds the relevo open lock file with no engine at all, so a second open
// fails for the relevo lock rather than the engine's.
func lockFileFlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}
