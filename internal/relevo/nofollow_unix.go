//go:build unix

package relevo

import "syscall"

// oNoFollow is O_NOFOLLOW where the platform has it: the reader output lives in
// a runner-writable directory, and the flag is the race backstop behind the
// Lstat refusal, so a link swapped in after the check cannot be followed.
const oNoFollow = syscall.O_NOFOLLOW
