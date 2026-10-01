//go:build unix

package remote

import "syscall"

// noFollow is O_NOFOLLOW where the platform has it: the temp bundle directory
// may be tenant-writable, and the flag is the race backstop behind the handle
// Stat, so a link swapped in where the bundle was cannot redirect the read out
// of the temp directory.
const noFollow = syscall.O_NOFOLLOW
