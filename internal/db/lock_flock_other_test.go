//go:build !unix

package db_test

import (
	"errors"
	"os"
)

// lockFileFlock refuses off unix: there is no flock, and the lock-helper
// child only ever runs under unix tests.
func lockFileFlock(*os.File) error {
	return errors.New("lock helper flock unavailable off unix")
}
