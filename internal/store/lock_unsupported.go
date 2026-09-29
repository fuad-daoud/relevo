//go:build !unix

package store

import (
	"errors"
	"os"
	"runtime"
)

// tryLockExclusive has no implementation here: relevo targets Linux and macOS,
// and this file exists so the tree still compiles elsewhere. It refuses rather
// than running unlocked, because two writers in the state root is the
// corruption the lock exists to prevent.
func tryLockExclusive(*os.File) (bool, error) {
	return false, errors.New("relevo state locking is not implemented on " + runtime.GOOS +
		"; relevo supports Linux and macOS")
}
