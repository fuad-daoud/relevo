//go:build unix

package syncworker

import "syscall"

// OwnerOnlyFiles makes every file this process creates from now on owner-only.
// The driver names and creates the replica's own files inside its native
// library, where this package cannot choose a mode, so the process umask is the
// only place the mode can be set before they exist.
func OwnerOnlyFiles() { syscall.Umask(0o077) }
