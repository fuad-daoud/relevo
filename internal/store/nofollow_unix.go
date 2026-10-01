//go:build unix

package store

import "syscall"

// noFollow is O_NOFOLLOW where the platform has it: the runner-output reads
// live in a runner-writable directory, and the flag is the race backstop behind
// the Lstat refusal, so a link swapped in after the check cannot be followed.
const noFollow = syscall.O_NOFOLLOW
